package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/breakfix/breakfix/internal/agentruntime"
	"github.com/breakfix/breakfix/internal/api"
	"github.com/breakfix/breakfix/internal/authoring"
	"github.com/breakfix/breakfix/internal/candidate"
	"github.com/breakfix/breakfix/internal/challenge"
	"github.com/breakfix/breakfix/internal/db"
	"github.com/breakfix/breakfix/internal/generator"
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
	user := h.requireUser(c)
	if user != nil {
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
	if _, _, err := h.authoring.StartTurn(c.Request.Context(), user.ID, sessionID, request.Content); err != nil {
		h.writeAuthoringError(c, err)
		return
	}
	h.writeAuthoringSession(c, user, sessionID)
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
	if session.State != authoring.StateIntentReview && session.State != authoring.StateRevisingAndVerifying {
		c.JSON(http.StatusConflict, api.ErrorResponse{Error: "the current intent is not ready to generate"})
		return
	}
	revision, err := h.db.GetAuthoringRevision(c.Request.Context(), session.ID, session.CurrentRevision)
	if err != nil {
		h.writeAuthoringError(c, err)
		return
	}
	input := generator.RunInput{}
	if session.State == authoring.StateRevisingAndVerifying {
		previous, err := h.db.FindLatestAuthoringCandidate(c.Request.Context(), session.ID, revision.Number-1)
		if err != nil {
			h.writeAuthoringError(c, err)
			return
		}
		if previous == nil {
			h.writeAuthoringError(c, authoring.ErrInvalidState)
			return
		}
		input.SeedCandidateRevisionID = previous.ID
	}
	if err := h.startAuthoringGeneratorRun(c.Request.Context(), user, session, revision, input); err != nil {
		h.writeAuthoringError(c, err)
		return
	}
	h.writeAuthoringSession(c, user, sessionID)
}

func (h *Handler) startAuthoringGeneratorRun(ctx context.Context, user *db.User, session *authoring.Session, revision *authoring.Revision, input generator.RunInput) error {
	if h.db == nil || h.generatorWorkspace == nil || h.generatorSandbox == nil {
		return errors.New("generator runtime is unavailable")
	}
	if user == nil || session == nil || revision == nil || revision.Number != session.CurrentRevision {
		return authoring.ErrInvalidState
	}
	if err := revision.Plan.ValidateForGeneration(); err != nil {
		return err
	}
	input.AuthoringSessionID = session.ID
	input.Revision = revision.Number
	_, _, err := h.db.StartGeneratorRun(ctx, session.ID, user.ID, revision.Number, agentruntime.CreateRun{
		ID: generator.NewRunID(), Purpose: generator.RuntimePurpose, OwnerKind: "authoring-session", OwnerRef: session.ID,
		Model: h.llm.Model, PromptVersion: generator.PromptVersion, ExecutionTimeout: generator.RunDeadline,
	}, input)
	return err
}

func (h *Handler) PublishAuthoringRevision(c *gin.Context, sessionID string) {
	user := h.requireUser(c)
	if user == nil {
		return
	}
	session, revision, _, err := h.authoring.Get(c.Request.Context(), user.ID, sessionID)
	if err != nil {
		h.writeAuthoringError(c, err)
		return
	}
	if session.State != authoring.StateAwaitingVerifiedReview || revision.CandidateRevisionID == "" || session.CandidateRevisionID != revision.CandidateRevisionID {
		c.JSON(http.StatusConflict, api.ErrorResponse{Error: "the current revision is not verified and ready to publish"})
		return
	}
	candidateRevision, archive, err := h.readCandidateArchive(c.Request.Context(), revision.CandidateRevisionID)
	if err != nil {
		h.writeAuthoringError(c, err)
		return
	}
	if candidateRevision.State != candidate.StateVerified || candidateRevision.Verification == nil || !candidateRevision.Verification.Passed {
		h.writeAuthoringError(c, authoring.ErrInvalidState)
		return
	}
	inspected, err := generator.InspectCandidateArchive(archive)
	if err != nil {
		h.writeAuthoringError(c, err)
		return
	}
	challengeID := challenge.NewID()
	sourceSlug := challenge.SourceSlugFor(inspected.Entry.Title, challengeID)
	now := time.Now().UTC()
	if err := h.db.BeginAuthoringCandidatePublish(c.Request.Context(), session.ID, user.ID, candidateRevision.ID, candidate.Publication{
		ChallengeID: challengeID, SourceSlug: sourceSlug, TargetPath: sourceSlug, RequestedAt: now,
	}, now); err != nil {
		h.writeAuthoringError(c, err)
		return
	}
	h.writeAuthoringSession(c, user, sessionID)
}

func (h *Handler) writeAuthoringSession(c *gin.Context, user *db.User, sessionID string) {
	session, revision, messages, err := h.authoring.Get(c.Request.Context(), user.ID, sessionID)
	if err != nil {
		h.writeAuthoringError(c, err)
		return
	}
	authoringTurnActive := false
	if active, err := h.db.GetActiveRunForSession(c.Request.Context(), session.RuntimeSessionID); err == nil {
		authoringTurnActive = active.Purpose == "authoring" && active.OwnerKind == "authoring-session" && active.OwnerRef == session.ID
	} else if !errors.Is(err, agentruntime.ErrNotFound) {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: fmt.Sprintf("read active authoring run: %v", err)})
		return
	}

	var visibleCandidate *candidate.Revision
	var archive []byte
	if revision.CandidateRevisionID != "" {
		visibleCandidate, archive, err = h.readCandidateArchive(c.Request.Context(), revision.CandidateRevisionID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: err.Error()})
			return
		}
	}
	assets, err := authoring.ReadAssets(archive)
	if err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: err.Error()})
		return
	}
	var previousArchive []byte
	if revision.Number > 0 {
		previous, err := h.db.FindLatestAuthoringCandidate(c.Request.Context(), session.ID, revision.Number-1)
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
	diff, err := authoring.DiffAssets(archive, previousArchive)
	if err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: err.Error()})
		return
	}
	verified, err := authoring.ReadVerifiedChallenge(archive)
	if err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: err.Error()})
		return
	}
	var pipelineState *api.AuthoringSessionPipelineState
	if session.CandidateRevisionID != "" {
		active, err := h.db.GetCandidateRevision(c.Request.Context(), session.CandidateRevisionID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: err.Error()})
			return
		}
		value := api.AuthoringSessionPipelineState(active.State)
		pipelineState = &value
	}
	c.JSON(http.StatusOK, toAPIAuthoringSession(session, revision, visibleCandidate, pipelineState, authoringTurnActive, messages, assets, diff, verified))
}

func (h *Handler) readCandidateArchive(ctx context.Context, id string) (*candidate.Revision, []byte, error) {
	revision, err := h.db.GetCandidateRevision(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	archive, err := candidate.ReadArchive(revision.ArchivePath, revision.ArchiveSHA256)
	if err != nil {
		return nil, nil, err
	}
	return revision, archive, nil
}

func toAPIAuthoringSession(session *authoring.Session, revision *authoring.Revision, visibleCandidate *candidate.Revision, pipelineState *api.AuthoringSessionPipelineState, turnActive bool, messages []authoring.Message, assets []authoring.Asset, diff []authoring.FileDiff, verified *authoring.VerifiedChallenge) api.AuthoringSession {
	var verification *api.AuthoringVerificationReport
	if visibleCandidate != nil {
		verification = toAPIAuthoringVerificationReport(visibleCandidate.Verification)
	}
	return api.AuthoringSession{
		Assets: assetsToAPI(assets), AuthoringTurnActive: turnActive, Candidate: toAPIAuthoringCandidate(visibleCandidate),
		Diff: toAPIAuthoringFileDiffs(diff), GeneratorRunId: optionalString(session.GeneratorRunID), Id: session.ID,
		Intent: toAPIAuthoringPlan(revision.Plan), IntentRevision: int(session.CurrentRevision), LastError: optionalString(session.LastError),
		Messages: toAPIAuthoringMessages(messages), PipelineState: pipelineState, PublishChallengeId: optionalString(session.PublishChallengeID),
		State: api.AuthoringSessionState(session.State), UpdatedAt: session.UpdatedAt.UTC(), Verification: verification,
		Verified: toAPIVerifiedChallenge(verified), VisibleRevision: int(revision.Number),
	}
}

func toAPIAuthoringCandidate(revision *candidate.Revision) *api.AuthoringCandidate {
	if revision == nil {
		return nil
	}
	return &api.AuthoringCandidate{Id: revision.ID, GeneratorRunId: revision.GeneratorRunID, ArchiveSha256: revision.ArchiveSHA256, State: api.AuthoringCandidateState(revision.State)}
}

func toAPIAuthoringVerificationReport(report *candidate.VerificationReport) *api.AuthoringVerificationReport {
	if report == nil {
		return nil
	}
	answers := make([]api.AuthoringExecutionResult, 0, len(report.Answers))
	for _, answer := range report.Answers {
		answers = append(answers, api.AuthoringExecutionResult{
			Location: answer.Location, ExitCode: answer.ExitCode, Stdout: optionalString(answer.Stdout), Stderr: optionalString(answer.Stderr),
		})
	}
	checkpoints := make([]api.AuthoringCheckpointResult, 0, len(report.Checkpoints))
	for _, checkpoint := range report.Checkpoints {
		checkpoints = append(checkpoints, api.AuthoringCheckpointResult{
			Id: checkpoint.ID, Passed: checkpoint.Passed, Summary: checkpoint.Summary, Details: optionalString(checkpoint.Details),
		})
	}
	return &api.AuthoringVerificationReport{Passed: report.Passed, Summary: report.Summary, Answers: answers, Checkpoints: checkpoints}
}

func toAPIAuthoringPlan(plan authoring.Plan) api.AuthoringPlan {
	checkpoints := make([]api.AuthoringCheckpoint, 0, len(plan.Checkpoints))
	for _, checkpoint := range plan.Checkpoints {
		checkpoints = append(checkpoints, api.AuthoringCheckpoint{Id: checkpoint.ID, Markdown: checkpoint.Markdown, Position: checkpoint.Position, Title: checkpoint.Title})
	}
	return api.AuthoringPlan{Checkpoints: checkpoints, Metadata: toAPIAuthoringMetadata(plan.Metadata), Overview: plan.Overview}
}

func toAPIAuthoringMetadata(metadata authoring.Metadata) api.AuthoringMetadata {
	return api.AuthoringMetadata{Description: metadata.Description, Difficulty: api.AuthoringMetadataDifficulty(metadata.Difficulty), Runtime: api.AuthoringMetadataRuntime(metadata.Runtime), Title: metadata.Title}
}

func toAPIVerifiedChallenge(value *authoring.VerifiedChallenge) *api.VerifiedChallenge {
	if value == nil {
		return nil
	}
	checkpoints := make([]api.VerifiedCheckpoint, 0, len(value.Checkpoints))
	for _, checkpoint := range value.Checkpoints {
		checkpoints = append(checkpoints, api.VerifiedCheckpoint{Description: checkpoint.Description, Hint: optionalString(checkpoint.Hint), Id: checkpoint.ID, Node: optionalString(checkpoint.Node), Title: checkpoint.Title})
	}
	return &api.VerifiedChallenge{Checkpoints: checkpoints, Metadata: toAPIAuthoringMetadata(value.Metadata)}
}

func toAPIAuthoringMessages(messages []authoring.Message) []api.AuthoringMessage {
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

func assetsToAPI(assets []authoring.Asset) []api.AuthoringAsset {
	result := make([]api.AuthoringAsset, 0, len(assets))
	for _, asset := range assets {
		result = append(result, api.AuthoringAsset{Content: asset.Content, Path: asset.Path})
	}
	return result
}

func toAPIAuthoringFileDiffs(diff []authoring.FileDiff) []api.AuthoringFileDiff {
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
	case errors.Is(err, authoring.ErrNotFound), errors.Is(err, candidate.ErrNotFound):
		c.JSON(http.StatusNotFound, api.ErrorResponse{Error: "authoring session or candidate not found"})
	case errors.Is(err, authoring.ErrVersionConflict), errors.Is(err, authoring.ErrInvalidState), errors.Is(err, candidate.ErrInvalidState):
		c.JSON(http.StatusConflict, api.ErrorResponse{Error: err.Error()})
	default:
		c.JSON(http.StatusBadRequest, api.ErrorResponse{Error: err.Error()})
	}
}
