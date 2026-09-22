package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"

	runtimev2 "github.com/breakfix/breakfix/api/v2"
	"github.com/breakfix/breakfix/internal/adapter/postgres"
	"github.com/breakfix/breakfix/internal/content/scenario"
	"github.com/breakfix/breakfix/internal/domain/authoring"
	api "github.com/breakfix/breakfix/internal/transport/httpapi/generated"
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

func (h *Handler) mySpace(ctx context.Context, user *postgres.User, learningLimit int) (api.MySpace, error) {
	if h.db == nil || h.k8s == nil {
		return api.MySpace{}, fmt.Errorf("user space dependencies are not configured")
	}
	now := time.Now().UTC()
	createdAt := user.CreatedAt.UTC()
	if createdAt.IsZero() {
		return api.MySpace{}, fmt.Errorf("user creation time is missing")
	}
	catalog, err := h.catalogEntries(ctx)
	if err != nil {
		return api.MySpace{}, fmt.Errorf("list scenario catalog: %w", err)
	}

	learning, err := h.db.Environment.LearningSummary(ctx, user.ID, now)
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
		// The playground binds the user alone: it shows as its own kind with a
		// state badge and expiry but no scenario identity or checkpoint
		// progress. Provisioning sessions are already visible — the same live
		// phases the floating ball polls.
		if env.Blank && env.ScenarioRef == "" && env.SourceRevision == "" {
			if !isLiveEnvironmentPhase(env.Phase) {
				continue
			}
			runtimeName := env.Runtime
			if runtimeName == "" {
				runtimeName = playgroundProvider
			}
			var expiresAt *time.Time
			if env.ExpiresAt != nil {
				expires := env.ExpiresAt.UTC()
				expiresAt = &expires
			}
			playground := api.MySpaceActiveEnvironment{
				EnvironmentId: env.Name,
				Kind:          api.MySpaceActiveEnvironmentKindPlayground,
				Runtime:       api.MySpaceActiveEnvironmentRuntime(runtimeName),
				Phase:         string(env.Phase),
				ExpiresAt:     expiresAt,
			}
			// A reset keeps the stale Ready phase until the rebuilt terminal
			// reports Ready; the row must read as resetting, not ready.
			if env.Operation == runtimev2.OperationResetting {
				operation := api.MySpaceActiveEnvironmentOperation(env.Operation)
				playground.Operation = &operation
			}
			active = append(active, playground)
			continue
		}
		if env.Phase != runtimev2.PhaseReady && env.Phase != runtimev2.PhaseDraining {
			continue
		}
		entry, err := h.entryForScenarioRevision(ctx, catalog, env.ScenarioRef, env.SourceRevision)
		if err != nil {
			continue
		}
		checkpoints, err := h.checkpointStatus(ctx, &env, entry)
		if err != nil {
			return api.MySpace{}, fmt.Errorf("read environment progress: %w", err)
		}
		var expiresAt *time.Time
		if env.ExpiresAt != nil {
			expires := env.ExpiresAt.UTC()
			expiresAt = &expires
		}
		scenario := mySpaceScenario(*entry)
		progress := checkpointProgressSummary(checkpoints, len(entry.Checkpoints))
		active = append(active, api.MySpaceActiveEnvironment{
			EnvironmentId:      env.Name,
			Kind:               api.MySpaceActiveEnvironmentKindOperations,
			Scenario:           &scenario,
			Runtime:            api.MySpaceActiveEnvironmentRuntime(env.Runtime),
			Phase:              string(env.Phase),
			CheckpointProgress: &progress,
			ExpiresAt:          expiresAt,
		})
	}

	page, err := h.mySpaceLearningFromCatalog(ctx, user.ID, nil, postgres.LearningHistoryFilter{}, learningLimit, now, catalog)
	if err != nil {
		return api.MySpace{}, err
	}
	authoringView, authoringCount, publishedCount, err := h.mySpaceAuthoring(ctx, user.ID)
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
	environments, err := h.k8s.ListRuntimeEnvironments(ctx, h.crdNamespace, selector)
	if err != nil {
		return 0, err
	}
	occupied := 0
	for _, environment := range environments.Items {
		if environment.Status.Phase != "Released" {
			occupied++
		}
	}
	return occupied, nil
}

func (h *Handler) mySpaceLearning(ctx context.Context, userID string, cursor *postgres.LearningHistoryCursor, filter postgres.LearningHistoryFilter, limit int) (api.MySpaceLearningPage, error) {
	catalog, err := h.catalogEntries(ctx)
	if err != nil {
		return api.MySpaceLearningPage{}, fmt.Errorf("list scenario catalog: %w", err)
	}
	return h.mySpaceLearningFromCatalog(ctx, userID, cursor, filter, limit, time.Now().UTC(), catalog)
}

func (h *Handler) mySpaceLearningFromCatalog(ctx context.Context, userID string, cursor *postgres.LearningHistoryCursor, filter postgres.LearningHistoryFilter, limit int, now time.Time, catalog map[string]scenario.Entry) (api.MySpaceLearningPage, error) {
	// Do not filter by the current Catalog. Deprecated Scenarios and older
	// revisions remain valid learning history and are resolved below through
	// their durable scenario revision.
	items, err := h.db.Environment.ListLearningHistory(ctx, userID, filter, limit+1, cursor, now)
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
	firstPasses, err := h.db.Environment.ListCheckpointFirstPasses(ctx, environmentUIDs)
	if err != nil {
		return api.MySpaceLearningPage{}, err
	}
	for _, item := range items {
		entry, err := h.entryForScenarioRevision(ctx, catalog, item.ScenarioID, item.ScenarioRevision)
		if err != nil {
			return api.MySpaceLearningPage{}, fmt.Errorf("resolve learning scenario %q revision %q: %w", item.ScenarioID, item.ScenarioRevision, err)
		}
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
			Scenario:              mySpaceScenario(*entry),
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

func parseMySpaceLearningCursor(raw *string) (*postgres.LearningHistoryCursor, error) {
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
	return &postgres.LearningHistoryCursor{ReadyAt: readyAt.UTC(), EnvironmentUID: token.EnvironmentUID}, nil
}

func mySpaceLearningFilter(params api.GetMySpaceLearningParams) (postgres.LearningHistoryFilter, error) {
	filter := postgres.LearningHistoryFilter{}
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

func (h *Handler) mySpaceAuthoring(ctx context.Context, userID string) (api.MySpaceAuthoring, int, int, error) {
	sessions, err := h.db.Reporting.ListAuthoringSpaceSessions(ctx, userID)
	if err != nil {
		return api.MySpaceAuthoring{}, 0, 0, err
	}
	view := api.MySpaceAuthoring{Drafts: make([]api.MySpaceAuthoringDraft, 0), Published: make([]api.MySpacePublishedScenario, 0)}
	type authoredPublished struct {
		scenarioID  string
		revisionID  string
		state       string
		entry       scenario.Entry
		publishedAt time.Time
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
	}
	scenarios, err := h.db.Scenario.ListAuthoringScenarios(ctx, userID)
	if err != nil {
		return api.MySpaceAuthoring{}, 0, 0, fmt.Errorf("list authored scenarios: %w", err)
	}
	for _, authored := range scenarios {
		revision, err := h.db.Scenario.GetScenarioRevision(ctx, authored.ID, authored.ActiveRevisionID)
		if err != nil {
			return api.MySpaceAuthoring{}, 0, 0, fmt.Errorf("read authored scenario %q revision: %w", authored.ID, err)
		}
		entry, err := h.catalog.HistoricalEntry(ctx, authored.ID, authored.ActiveRevisionID)
		if err != nil {
			return api.MySpaceAuthoring{}, 0, 0, fmt.Errorf("read authored scenario %q content: %w", authored.ID, err)
		}
		published = append(published, authoredPublished{
			scenarioID: authored.ID, revisionID: revision.ID, state: string(authored.State), entry: *entry, publishedAt: revision.PublishedAt,
		})
	}
	scenarioIDs := make([]string, 0, len(published))
	for _, item := range published {
		scenarioIDs = append(scenarioIDs, item.scenarioID)
	}
	counts, err := h.db.Reporting.ScenarioAudienceCounts(ctx, scenarioIDs)
	if err != nil {
		return api.MySpaceAuthoring{}, 0, 0, err
	}
	for _, item := range published {
		count := counts[item.scenarioID]
		card := api.MySpacePublishedScenario{
			Scenario:       mySpaceScenario(item.entry),
			RevisionId:     item.revisionID,
			State:          api.MySpacePublishedScenarioState(item.state),
			PublishedAt:    item.publishedAt,
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

func mySpaceScenario(entry scenario.Entry) api.MySpaceScenario {
	return api.MySpaceScenario{
		Id:            entry.ID,
		Title:         entry.Title,
		Runtime:       api.MySpaceScenarioRuntime(entry.Runtime),
		ContentSource: mySpaceContentSource(entry.Type),
	}
}

func mySpaceContentSource(scenarioType scenario.ScenarioType) api.MySpaceScenarioContentSource {
	if scenarioType == scenario.ScenarioDocumentationExample {
		return api.MySpaceScenarioContentSourceDocumentation
	}
	return api.MySpaceScenarioContentSourceOperations
}

func authoringSessionTitle(title string) string {
	if title == "" {
		return "Untitled scenario"
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
