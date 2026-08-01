package httpapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/breakfix/breakfix/internal/adapter/postgres"
	appauthoring "github.com/breakfix/breakfix/internal/application/authoring"
	generationapp "github.com/breakfix/breakfix/internal/application/generation"
	"github.com/breakfix/breakfix/internal/content/candidate"
	"github.com/breakfix/breakfix/internal/content/challenge"
	"github.com/breakfix/breakfix/internal/domain/agent"
	authoringdomain "github.com/breakfix/breakfix/internal/domain/authoring"
	"github.com/breakfix/breakfix/internal/domain/generation"
	api "github.com/breakfix/breakfix/internal/transport/httpapi/generated"
	"github.com/breakfix/breakfix/internal/transport/httpapi/stream"
	"github.com/gin-gonic/gin"
)

func (h *Handler) CreateAuthoringSession(c *gin.Context) {
	user := h.requireUser(c)
	if user == nil {
		return
	}
	session, err := h.authoring.Create(c.Request.Context(), user.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: fmt.Sprintf("create authoring session: %v", err)})
		return
	}
	h.writeAuthoringSession(c, user, session.ID)
}

func (h *Handler) GetAuthoringSession(c *gin.Context, sessionID string) {
	if user := h.requireUser(c); user != nil {
		h.writeAuthoringSession(c, user, sessionID)
	}
}

func (h *Handler) GetCurrentAuthoringSession(c *gin.Context) {
	user := h.requireUser(c)
	if user == nil {
		return
	}
	session, err := h.authoring.GetCurrent(c.Request.Context(), user.ID)
	if err != nil {
		h.writeAuthoringError(c, err)
		return
	}
	h.writeAuthoringSession(c, user, session.ID)
}

func (h *Handler) SendAuthoringMessage(c *gin.Context, sessionID string) {
	user := h.requireUser(c)
	if user == nil {
		return
	}
	var request api.AuthoringMessageRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, api.ErrorResponse{Error: err.Error()})
		return
	}
	_, run, err := h.authoring.StartTurn(c.Request.Context(), user.ID, sessionID, request.Content)
	if err != nil {
		h.writeAuthoringError(c, err)
		return
	}
	h.streamAuthoringTurn(c, run.ID)
}

type authoringStreamEvent struct {
	RunID   string `json:"run_id"`
	Content string `json:"content,omitempty"`
}

func (h *Handler) streamAuthoringTurn(c *gin.Context, runID string) {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)
	stream.WriteSSE(c, "ready", authoringStreamEvent{RunID: runID})

	content, err := h.authoring.RunTurn(c.Request.Context(), runID, func(event appauthoring.StreamEvent) {
		stream.WriteSSE(c, "delta", authoringStreamEvent{RunID: runID, Content: event.Content})
	})
	if err == nil {
		stream.WriteSSE(c, "complete", authoringStreamEvent{RunID: runID, Content: content})
		return
	}
	if failErr := h.authoring.FailTurn(context.Background(), runID, err.Error()); failErr != nil && !errors.Is(failErr, agent.ErrRunActive) {
		slog.Error("finalize direct authoring turn", "run_id", runID, "err", errors.Join(err, failErr))
	}
	if c.Request.Context().Err() == nil {
		stream.WriteSSE(c, "error", authoringStreamEvent{RunID: runID, Content: err.Error()})
	}
}

func (h *Handler) ConfirmAuthoringGeneration(c *gin.Context, sessionID string) {
	user := h.requireUser(c)
	if user == nil {
		return
	}
	session, _, _, err := h.authoring.Get(c.Request.Context(), user.ID, sessionID)
	if err != nil {
		h.writeAuthoringError(c, err)
		return
	}
	if session.State != authoringdomain.StateIntentReview {
		c.JSON(http.StatusConflict, api.ErrorResponse{Error: "the current intent is not ready to generate"})
		return
	}
	now := time.Now().UTC()
	active, activeErr := h.db.Generation.GetActiveGenerationWorkflow(c.Request.Context(), session.ID)
	switch {
	case errors.Is(activeErr, postgres.ErrGenerationWorkflowNotFound):
		_, err = h.db.Generation.CreateGenerationWorkflow(c.Request.Context(), session.ID, user.ID, session.CurrentRevision, now)
	case activeErr != nil:
		err = activeErr
	case active.State == generation.StateNeedsAuthorReview:
		_, err = h.db.Generation.ResumeGenerationForRevision(c.Request.Context(), session.ID, user.ID, session.CurrentRevision, now)
	default:
		err = authoringdomain.ErrInvalidState
	}
	if err != nil {
		h.writeAuthoringError(c, err)
		return
	}
	h.writeAuthoringSession(c, user, sessionID)
}

func (h *Handler) PublishAuthoringRevision(c *gin.Context, sessionID string) {
	user := h.requireUser(c)
	if user == nil {
		return
	}
	session, _, _, err := h.authoring.Get(c.Request.Context(), user.ID, sessionID)
	if err != nil {
		h.writeAuthoringError(c, err)
		return
	}
	workflow, err := h.db.Generation.GetActiveGenerationWorkflow(c.Request.Context(), session.ID)
	if err != nil {
		h.writeAuthoringError(c, err)
		return
	}
	if workflow.State != generation.StateNeedsAuthorReview || workflow.CandidateRevisionID == "" {
		h.writeAuthoringError(c, authoringdomain.ErrInvalidState)
		return
	}
	revision, archive, err := h.readCandidateArchive(c.Request.Context(), workflow.CandidateRevisionID)
	if err != nil {
		h.writeAuthoringError(c, err)
		return
	}
	if revision.Verification == nil || !revision.Verification.Passed || revision.Artifact == nil {
		h.writeAuthoringError(c, authoringdomain.ErrInvalidState)
		return
	}
	inspected, err := generationapp.InspectCandidateArchive(archive)
	if err != nil {
		h.writeAuthoringError(c, err)
		return
	}
	challengeID := challenge.NewID()
	now := time.Now().UTC()
	_, err = h.db.Generation.BeginGenerationPublication(c.Request.Context(), session.ID, user.ID, generation.Publication{
		ChallengeID: challengeID,
		SourceSlug:  challenge.SourceSlugFor(inspected.Entry.Title, challengeID),
		TargetPath:  challenge.SourceSlugFor(inspected.Entry.Title, challengeID),
		RequestedAt: now,
	}, now)
	if err != nil {
		h.writeAuthoringError(c, err)
		return
	}
	h.writeAuthoringSession(c, user, sessionID)
}

func (h *Handler) writeAuthoringSession(c *gin.Context, user *postgres.User, sessionID string) {
	session, revision, messages, err := h.authoring.Get(c.Request.Context(), user.ID, sessionID)
	if err != nil {
		h.writeAuthoringError(c, err)
		return
	}
	authoringTurnActive := false
	if active, err := h.db.Agent.GetActiveRunForSession(c.Request.Context(), session.RuntimeSessionID); err == nil {
		authoringTurnActive = active.Purpose == "authoring" && active.OwnerKind == "authoring-session" && active.OwnerRef == session.ID
	} else if !errors.Is(err, agent.ErrNotFound) {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: fmt.Sprintf("read active authoring run: %v", err)})
		return
	}

	var workflow *generation.Workflow
	workflow, err = h.db.Generation.GetActiveGenerationWorkflow(c.Request.Context(), session.ID)
	if errors.Is(err, postgres.ErrGenerationWorkflowNotFound) {
		workflow = nil
	} else if err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: fmt.Sprintf("read generation workflow: %v", err)})
		return
	}

	var visibleCandidate *generation.Revision
	var archive []byte
	if revision.CandidateRevisionID != "" {
		visibleCandidate, archive, err = h.readCandidateArchive(c.Request.Context(), revision.CandidateRevisionID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: err.Error()})
			return
		}
	}
	assets, err := appauthoring.ReadAssets(archive)
	if err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: err.Error()})
		return
	}
	var previousArchive []byte
	if revision.Number > 0 {
		previous, err := h.db.Generation.FindLatestCandidateBefore(c.Request.Context(), session.ID, revision.Number)
		if err != nil {
			c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: err.Error()})
			return
		}
		if previous != nil {
			previousArchive, err = candidate.ReadArchive(previous.ArchivePath, previous.ArchiveSHA256)
			if err != nil {
				c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: err.Error()})
				return
			}
		}
	}
	diff, err := appauthoring.DiffAssets(archive, previousArchive)
	if err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: err.Error()})
		return
	}
	verified, err := appauthoring.ReadVerifiedChallenge(archive)
	if err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: err.Error()})
		return
	}
	c.JSON(http.StatusOK, toAPIAuthoringSession(session, revision, visibleCandidate, workflow, authoringTurnActive, messages, assets, diff, verified))
}

func (h *Handler) readCandidateArchive(ctx context.Context, id string) (*generation.Revision, []byte, error) {
	revision, err := h.db.Generation.GetCandidateRevision(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	archive, err := candidate.ReadArchive(revision.ArchivePath, revision.ArchiveSHA256)
	if err != nil {
		return nil, nil, err
	}
	return revision, archive, nil
}

func toAPIAuthoringSession(session *authoringdomain.Session, revision *authoringdomain.Revision, visibleCandidate *generation.Revision, workflow *generation.Workflow, turnActive bool, messages []authoringdomain.Message, assets []appauthoring.Asset, diff []appauthoring.FileDiff, verified *authoringdomain.VerifiedChallenge) api.AuthoringSession {
	var verification *api.AuthoringVerificationReport
	if visibleCandidate != nil {
		verification = toAPIAuthoringVerificationReport(visibleCandidate.Verification)
	}
	return api.AuthoringSession{
		Assets: assetsToAPI(assets), AuthoringTurnActive: turnActive, Candidate: toAPIAuthoringCandidate(visibleCandidate),
		Diff: toAPIAuthoringFileDiffs(diff), Id: session.ID, Intent: toAPIAuthoringPlan(revision.Plan),
		IntentRevision: int(session.CurrentRevision), LastError: optionalString(session.LastError), Messages: toAPIAuthoringMessages(messages),
		PublishChallengeId: optionalString(session.PublishChallengeID), State: api.AuthoringSessionState(session.State),
		UpdatedAt: session.UpdatedAt.UTC(), Verification: verification, Verified: toAPIVerifiedChallenge(verified), VisibleRevision: int(revision.Number),
		Workflow: toAPIAuthoringGenerationWorkflow(workflow),
	}
}

func toAPIAuthoringCandidate(revision *generation.Revision) *api.AuthoringCandidate {
	if revision == nil {
		return nil
	}
	return &api.AuthoringCandidate{Id: revision.ID, GeneratorRunId: revision.GeneratorRunID, ArchiveSha256: revision.ArchiveSHA256}
}

func toAPIAuthoringGenerationWorkflow(workflow *generation.Workflow) *api.AuthoringGenerationWorkflow {
	if workflow == nil {
		return nil
	}
	var deadline *time.Time
	if workflow.DeadlineAt != nil {
		value := workflow.DeadlineAt.UTC()
		deadline = &value
	}
	return &api.AuthoringGenerationWorkflow{
		Id:                  workflow.ID,
		State:               api.AuthoringGenerationWorkflowState(workflow.State),
		StateAttempt:        workflow.StateAttempt,
		CandidateRevisionId: optionalString(workflow.CandidateRevisionID),
		DeadlineAt:          deadline,
		LastError:           optionalString(workflow.LastError),
		CreatedAt:           workflow.CreatedAt.UTC(),
		UpdatedAt:           workflow.UpdatedAt.UTC(),
	}
}

func toAPIAuthoringVerificationReport(report *generation.VerificationReport) *api.AuthoringVerificationReport {
	if report == nil {
		return nil
	}
	answers := make([]api.AuthoringExecutionResult, 0, len(report.Answers))
	for _, answer := range report.Answers {
		answers = append(answers, api.AuthoringExecutionResult{Location: answer.Location, ExitCode: answer.ExitCode, Stdout: optionalString(answer.Stdout), Stderr: optionalString(answer.Stderr)})
	}
	checkpoints := make([]api.AuthoringCheckpointResult, 0, len(report.Checkpoints))
	for _, checkpoint := range report.Checkpoints {
		checkpoints = append(checkpoints, api.AuthoringCheckpointResult{Id: checkpoint.ID, Passed: checkpoint.Passed, Summary: checkpoint.Summary, Details: optionalString(checkpoint.Details)})
	}
	return &api.AuthoringVerificationReport{Passed: report.Passed, Summary: report.Summary, Answers: answers, Checkpoints: checkpoints}
}

func toAPIAuthoringPlan(plan authoringdomain.Plan) api.AuthoringPlan {
	checkpoints := make([]api.AuthoringCheckpoint, 0, len(plan.Checkpoints))
	for _, checkpoint := range plan.Checkpoints {
		checkpoints = append(checkpoints, api.AuthoringCheckpoint{Id: checkpoint.ID, Markdown: checkpoint.Markdown, Position: checkpoint.Position, Title: checkpoint.Title})
	}
	return api.AuthoringPlan{Checkpoints: checkpoints, Metadata: toAPIAuthoringMetadata(plan.Metadata), Overview: plan.Overview}
}

func toAPIAuthoringMetadata(metadata authoringdomain.Metadata) api.AuthoringMetadata {
	return api.AuthoringMetadata{Description: metadata.Description, Difficulty: api.AuthoringMetadataDifficulty(metadata.Difficulty), Runtime: api.AuthoringMetadataRuntime(metadata.Runtime), Title: metadata.Title}
}

func toAPIVerifiedChallenge(value *authoringdomain.VerifiedChallenge) *api.VerifiedChallenge {
	if value == nil {
		return nil
	}
	checkpoints := make([]api.VerifiedCheckpoint, 0, len(value.Checkpoints))
	for _, checkpoint := range value.Checkpoints {
		checkpoints = append(checkpoints, api.VerifiedCheckpoint{Description: checkpoint.Description, Hint: optionalString(checkpoint.Hint), Id: checkpoint.ID, Node: optionalString(checkpoint.Node), Title: checkpoint.Title})
	}
	return &api.VerifiedChallenge{Checkpoints: checkpoints, Metadata: toAPIAuthoringMetadata(value.Metadata)}
}

func toAPIAuthoringMessages(messages []authoringdomain.Message) []api.AuthoringMessage {
	result := make([]api.AuthoringMessage, 0, len(messages))
	for _, message := range messages {
		changes := make([]api.AuthoringChange, 0, len(message.Changes))
		for _, change := range message.Changes {
			changes = append(changes, api.AuthoringChange{DifficultyImpact: change.DifficultyImpact, Kind: change.Kind, Revision: int(change.Revision), Summary: change.Summary})
		}
		result = append(result, api.AuthoringMessage{Changes: optionalSlice(changes), Content: message.Content, CreatedAt: message.CreatedAt.UTC(), Id: message.ID, Role: api.AuthoringMessageRole(message.Role)})
	}
	return result
}

func assetsToAPI(assets []appauthoring.Asset) []api.AuthoringAsset {
	result := make([]api.AuthoringAsset, 0, len(assets))
	for _, asset := range assets {
		result = append(result, api.AuthoringAsset{Content: asset.Content, Path: asset.Path})
	}
	return result
}

func toAPIAuthoringFileDiffs(diff []appauthoring.FileDiff) []api.AuthoringFileDiff {
	result := make([]api.AuthoringFileDiff, 0, len(diff))
	for _, entry := range diff {
		result = append(result, api.AuthoringFileDiff{Diff: entry.Diff, Path: entry.Path})
	}
	return result
}

func optionalString(value string) *string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return &value
}

func optionalSlice[T any](values []T) *[]T {
	if len(values) == 0 {
		return nil
	}
	return &values
}

func (h *Handler) writeAuthoringError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, authoringdomain.ErrNotFound), errors.Is(err, generation.ErrCandidateNotFound), errors.Is(err, postgres.ErrGenerationWorkflowNotFound):
		c.JSON(http.StatusNotFound, api.ErrorResponse{Error: "authoring session, generation workflow, or candidate not found"})
	case errors.Is(err, authoringdomain.ErrVersionConflict), errors.Is(err, authoringdomain.ErrInvalidState), errors.Is(err, generation.ErrCandidateInvalidState):
		c.JSON(http.StatusConflict, api.ErrorResponse{Error: err.Error()})
	default:
		c.JSON(http.StatusBadRequest, api.ErrorResponse{Error: err.Error()})
	}
}
