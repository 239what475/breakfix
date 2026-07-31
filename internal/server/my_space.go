package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/breakfix/breakfix/internal/api"
	"github.com/breakfix/breakfix/internal/authoring"
	"github.com/breakfix/breakfix/internal/challenge"
	"github.com/breakfix/breakfix/internal/db"
	breakfixv1 "github.com/breakfix/breakfix/internal/k8s/apis/breakfix/v1"
	"github.com/breakfix/breakfix/internal/taxonomy"
	"github.com/gin-gonic/gin"
)

const mySpaceInitialLearningLimit = 20

func (h *Handler) GetMySpace(c *gin.Context) {
	user := h.requireUser(c)
	if user == nil {
		return
	}
	space, err := h.mySpace(c.Request.Context(), user, mySpaceInitialLearningLimit)
	if err != nil {
		c.JSON(500, api.ErrorResponse{Error: err.Error()})
		return
	}
	c.JSON(200, space)
}

func (h *Handler) GetMySpaceLearning(c *gin.Context, params api.GetMySpaceLearningParams) {
	user := h.requireUser(c)
	if user == nil {
		return
	}
	limit := mySpaceInitialLearningLimit
	if params.Limit != nil {
		limit = *params.Limit
	}
	if limit < 1 || limit > 100 {
		c.JSON(400, api.ErrorResponse{Error: "learning history limit must be between 1 and 100"})
		return
	}
	cursor, err := parseMySpaceLearningCursor(params.Cursor)
	if err != nil {
		c.JSON(400, api.ErrorResponse{Error: err.Error()})
		return
	}
	filter, err := mySpaceLearningFilter(params)
	if err != nil {
		c.JSON(400, api.ErrorResponse{Error: err.Error()})
		return
	}
	page, err := h.mySpaceLearning(c.Request.Context(), user.ID, cursor, filter, limit)
	if err != nil {
		c.JSON(500, api.ErrorResponse{Error: err.Error()})
		return
	}
	c.JSON(200, page)
}

func (h *Handler) mySpace(ctx context.Context, user *db.User, learningLimit int) (api.MySpace, error) {
	if h.db == nil || h.k8s == nil {
		return api.MySpace{}, fmt.Errorf("user space dependencies are not configured")
	}
	now := time.Now().UTC()
	createdAt := user.CreatedAt.UTC()
	if createdAt.IsZero() {
		return api.MySpace{}, fmt.Errorf("user creation time is missing")
	}
	entries, err := challenge.List(h.challengesDir)
	if err != nil {
		return api.MySpace{}, fmt.Errorf("list challenge catalog: %w", err)
	}
	catalog := make(map[string]challenge.Entry, len(entries))
	for _, entry := range entries {
		catalog[entry.ID] = entry
	}

	learning, err := h.db.LearningSummary(ctx, user.ID, now)
	if err != nil {
		return api.MySpace{}, err
	}
	environments, err := h.listActiveEnvironments(ctx, user.ID)
	if err != nil {
		return api.MySpace{}, fmt.Errorf("list user environments: %w", err)
	}
	occupied, err := h.occupiedEnvironmentCount(ctx, user.ID)
	if err != nil {
		return api.MySpace{}, fmt.Errorf("count occupied environments: %w", err)
	}
	active := make([]api.MySpaceActiveEnvironment, 0, len(environments))
	for _, env := range environments {
		if env.Phase != breakfixv1.EnvironmentReady && env.Phase != breakfixv1.EnvironmentDraining {
			continue
		}
		entry, ok := catalog[env.ChallengeRef]
		if !ok {
			continue
		}
		var expiresAt *time.Time
		if env.ExpiresAt != nil {
			expires := env.ExpiresAt.UTC()
			expiresAt = &expires
		}
		active = append(active, api.MySpaceActiveEnvironment{
			EnvironmentId:      env.Name,
			Challenge:          mySpaceChallenge(entry),
			Runtime:            api.MySpaceActiveEnvironmentRuntime(env.Runtime),
			Phase:              string(env.Phase),
			CheckpointProgress: checkpointProgressSummary(env.Checkpoints, len(entry.Checkpoints)),
			ExpiresAt:          expiresAt,
		})
	}

	page, err := h.mySpaceLearningFromCatalog(ctx, user.ID, nil, db.LearningHistoryFilter{}, learningLimit, now, catalog)
	if err != nil {
		return api.MySpace{}, err
	}
	authoringView, authoringCount, publishedCount, err := h.mySpaceAuthoring(ctx, user.ID, catalog)
	if err != nil {
		return api.MySpace{}, err
	}
	return api.MySpace{
		Profile: api.MySpaceProfile{
			Id:        user.ID,
			Name:      user.Name,
			CreatedAt: createdAt,
		},
		Summary: api.MySpaceSummary{
			CompletedCount:             learning.CompletedCount,
			AttemptedCount:             learning.AttemptedCount,
			InProgressEnvironmentCount: len(active),
			TerminalLearningSeconds:    safeInt(learning.TerminalLearningSecond),
			AuthoringCount:             authoringCount,
			PublishedCount:             publishedCount,
			EnvironmentQuota: api.MySpaceEnvironmentQuota{
				Occupied: occupied,
				Maximum:  nil,
			},
		},
		ActiveEnvironments:       active,
		RecentLearning:           page.Items,
		RecentLearningNextCursor: page.NextCursor,
		Authoring:                authoringView,
	}, nil
}

// occupiedEnvironmentCount intentionally does not reuse listActiveEnvironments:
// a CRD with a deletion timestamp is no longer resumable, but may still own a
// namespace, Pod, or vcluster until the controller finalizer finishes.
func (h *Handler) occupiedEnvironmentCount(ctx context.Context, userID string) (int, error) {
	selector := fmt.Sprintf("breakfix.dev/user=%s", userID)
	nodeEnvironments, err := h.k8s.ListNodeEnvironments(ctx, h.crdNamespace, selector)
	if err != nil {
		return 0, err
	}
	vk8sEnvironments, err := h.k8s.ListVK8sEnvironments(ctx, h.crdNamespace, selector)
	if err != nil {
		return 0, err
	}
	occupied := 0
	for _, environment := range nodeEnvironments.Items {
		if environment.Status.Environment.Phase != breakfixv1.EnvironmentDestroyed {
			occupied++
		}
	}
	for _, environment := range vk8sEnvironments.Items {
		if environment.Status.Environment.Phase != breakfixv1.EnvironmentDestroyed {
			occupied++
		}
	}
	return occupied, nil
}

func (h *Handler) mySpaceLearning(ctx context.Context, userID string, cursor *db.LearningHistoryCursor, filter db.LearningHistoryFilter, limit int) (api.MySpaceLearningPage, error) {
	entries, err := challenge.List(h.challengesDir)
	if err != nil {
		return api.MySpaceLearningPage{}, fmt.Errorf("list challenge catalog: %w", err)
	}
	catalog := make(map[string]challenge.Entry, len(entries))
	for _, entry := range entries {
		catalog[entry.ID] = entry
	}
	return h.mySpaceLearningFromCatalog(ctx, userID, cursor, filter, limit, time.Now().UTC(), catalog)
}

func (h *Handler) mySpaceLearningFromCatalog(ctx context.Context, userID string, cursor *db.LearningHistoryCursor, filter db.LearningHistoryFilter, limit int, now time.Time, catalog map[string]challenge.Entry) (api.MySpaceLearningPage, error) {
	filter.ChallengeIDs = make([]string, 0, len(catalog))
	for challengeID := range catalog {
		filter.ChallengeIDs = append(filter.ChallengeIDs, challengeID)
	}
	items, err := h.db.ListLearningHistory(ctx, userID, filter, limit+1, cursor, now)
	if err != nil {
		return api.MySpaceLearningPage{}, err
	}
	page := api.MySpaceLearningPage{Items: make([]api.MySpaceLearningHistory, 0, min(limit, len(items)))}
	if len(items) > limit {
		last := items[limit-1]
		page.NextCursor = encodeMySpaceLearningCursor(last.ReadyAt, last.EnvironmentUID)
		items = items[:limit]
	}
	environmentUIDs := make([]string, 0, len(items))
	for _, item := range items {
		environmentUIDs = append(environmentUIDs, item.EnvironmentUID)
	}
	firstPasses, err := h.db.ListCheckpointFirstPasses(ctx, environmentUIDs)
	if err != nil {
		return api.MySpaceLearningPage{}, err
	}
	for _, item := range items {
		entry := catalog[item.ChallengeID]
		events := firstPasses[item.EnvironmentUID]
		checkpointFirstPasses := make([]api.CheckpointFirstPass, 0, len(events))
		for _, event := range events {
			checkpointFirstPasses = append(checkpointFirstPasses, api.CheckpointFirstPass{
				CheckpointId:  event.CheckpointID,
				FirstPassedAt: event.FirstPassedAt,
				Summary:       event.Summary,
			})
		}
		page.Items = append(page.Items, api.MySpaceLearningHistory{
			Challenge:             mySpaceChallenge(entry),
			CheckpointFirstPasses: checkpointFirstPasses,
			ReadyAt:               item.ReadyAt,
			CompletedAt:           item.CompletedAt,
			LearningSeconds:       safeInt(item.LearningSeconds),
			State:                 api.MySpaceLearningHistoryState(item.Outcome),
		})
	}
	return page, nil
}

type mySpaceLearningCursorToken struct {
	ReadyAt        string `json:"r"`
	EnvironmentUID string `json:"e"`
}

func encodeMySpaceLearningCursor(readyAt time.Time, environmentUID string) *string {
	payload, err := json.Marshal(mySpaceLearningCursorToken{ReadyAt: readyAt.UTC().Format(time.RFC3339Nano), EnvironmentUID: environmentUID})
	if err != nil {
		return nil
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	return &encoded
}

func parseMySpaceLearningCursor(raw *string) (*db.LearningHistoryCursor, error) {
	if raw == nil || *raw == "" {
		return nil, nil
	}
	payload, err := base64.RawURLEncoding.DecodeString(*raw)
	if err != nil {
		return nil, fmt.Errorf("learning history cursor is invalid")
	}
	var token mySpaceLearningCursorToken
	if err := json.Unmarshal(payload, &token); err != nil {
		return nil, fmt.Errorf("learning history cursor is invalid")
	}
	readyAt, err := time.Parse(time.RFC3339Nano, token.ReadyAt)
	if err != nil || readyAt.IsZero() || token.EnvironmentUID == "" {
		return nil, fmt.Errorf("learning history cursor is invalid")
	}
	return &db.LearningHistoryCursor{ReadyAt: readyAt.UTC(), EnvironmentUID: token.EnvironmentUID}, nil
}

func mySpaceLearningFilter(params api.GetMySpaceLearningParams) (db.LearningHistoryFilter, error) {
	filter := db.LearningHistoryFilter{}
	if params.State != nil {
		if !params.State.Valid() {
			return filter, fmt.Errorf("learning history state is invalid")
		}
		filter.State = string(*params.State)
	}
	if params.Runtime != nil {
		if !params.Runtime.Valid() {
			return filter, fmt.Errorf("learning history runtime is invalid")
		}
		filter.Runtime = string(*params.Runtime)
	}
	return filter, nil
}

func (h *Handler) mySpaceAuthoring(ctx context.Context, userID string, catalog map[string]challenge.Entry) (api.MySpaceAuthoring, int, int, error) {
	sessions, err := h.db.ListAuthoringSpaceSessions(ctx, userID)
	if err != nil {
		return api.MySpaceAuthoring{}, 0, 0, err
	}
	view := api.MySpaceAuthoring{Drafts: make([]api.MySpaceAuthoringDraft, 0), Published: make([]api.MySpacePublishedChallenge, 0)}
	type authoredPublished struct {
		session        db.AuthoringSpaceSession
		entry          challenge.Entry
		taxonomyStatus api.MySpacePublishedChallengeTaxonomyStatus
	}
	published := make([]authoredPublished, 0)
	for _, session := range sessions {
		if session.State != authoring.StatePublished {
			view.Drafts = append(view.Drafts, api.MySpaceAuthoringDraft{
				SessionId: session.ID,
				Title:     authoringSessionTitle(session.Title),
				State:     api.MySpaceAuthoringDraftState(session.State),
				UpdatedAt: session.UpdatedAt,
			})
			continue
		}
		if entry, ok := catalog[session.PublishChallengeID]; ok && session.PublishChallengeID != "" {
			status, err := h.authoringTaxonomyStatus(ctx, entry)
			if err != nil {
				return api.MySpaceAuthoring{}, 0, 0, err
			}
			published = append(published, authoredPublished{session: session, entry: entry, taxonomyStatus: status})
		}
	}
	challengeIDs := make([]string, 0, len(published))
	for _, item := range published {
		challengeIDs = append(challengeIDs, item.session.PublishChallengeID)
	}
	counts, err := h.db.ChallengeAudienceCounts(ctx, challengeIDs)
	if err != nil {
		return api.MySpaceAuthoring{}, 0, 0, err
	}
	for _, item := range published {
		count := counts[item.session.PublishChallengeID]
		card := api.MySpacePublishedChallenge{
			Challenge:      mySpaceChallenge(item.entry),
			PublishedAt:    item.entry.PublishedAt,
			TaxonomyStatus: item.taxonomyStatus,
			AttemptedUsers: count.AttemptedUsers,
			CompletedUsers: count.CompletedUsers,
		}
		if count.AttemptedUsers > 0 {
			rate := float32(count.CompletedUsers) / float32(count.AttemptedUsers)
			card.PassRate = &rate
		}
		view.Published = append(view.Published, card)
	}
	return view, len(view.Drafts), len(view.Published), nil
}

func (h *Handler) authoringTaxonomyStatus(ctx context.Context, entry challenge.Entry) (api.MySpacePublishedChallengeTaxonomyStatus, error) {
	if h.taxonomy != nil {
		snapshot, err := h.taxonomy.LoadCurrent()
		if err != nil && !errors.Is(err, taxonomy.ErrNoCurrentRevision) {
			return "", fmt.Errorf("load current taxonomy for authoring status: %w", err)
		}
		if snapshot != nil {
			index, err := taxonomy.NewCatalogIndex(*snapshot, []challenge.Entry{entry})
			if err != nil {
				return "", fmt.Errorf("build taxonomy index for authoring status: %w", err)
			}
			if _, mapped := index.Mapping(entry.ID); mapped {
				return api.MySpacePublishedChallengeTaxonomyStatus("mapped"), nil
			}
		}
	}

	mapping, err := h.db.GetTaxonomyMappingByChallenge(ctx, entry.ID, entry.Revision)
	if errors.Is(err, db.ErrTaxonomyMappingNotFound) {
		return api.MySpacePublishedChallengeTaxonomyStatus("mapping"), nil
	}
	if err != nil {
		return "", fmt.Errorf("read taxonomy work for authoring status: %w", err)
	}
	switch mapping.State {
	case taxonomy.MappingFailed, taxonomy.MappingCancelled:
		return api.MySpacePublishedChallengeTaxonomyStatus("blocked"), nil
	case taxonomy.MappingPending:
		if strings.TrimSpace(mapping.LastError) != "" {
			return api.MySpacePublishedChallengeTaxonomyStatus("retrying"), nil
		}
	}
	return api.MySpacePublishedChallengeTaxonomyStatus("mapping"), nil
}

func mySpaceChallenge(entry challenge.Entry) api.MySpaceChallenge {
	return api.MySpaceChallenge{
		Id:         entry.ID,
		Title:      entry.Title,
		Runtime:    api.MySpaceChallengeRuntime(entry.Runtime),
		Difficulty: entry.Difficulty,
	}
}

func authoringSessionTitle(title string) string {
	if title == "" {
		return "Untitled challenge"
	}
	return title
}

func safeInt(value int64) int {
	maxInt := int64(^uint(0) >> 1)
	if value > maxInt {
		return int(maxInt)
	}
	if value < 0 {
		return 0
	}
	return int(value)
}
