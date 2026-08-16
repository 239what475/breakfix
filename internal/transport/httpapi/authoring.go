package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/breakfix/breakfix/internal/adapter/postgres"
	appauthoring "github.com/breakfix/breakfix/internal/application/authoring"
	"github.com/breakfix/breakfix/internal/domain/agent"
	authoringdomain "github.com/breakfix/breakfix/internal/domain/authoring"
	challengedomain "github.com/breakfix/breakfix/internal/domain/challenge"
	"github.com/breakfix/breakfix/internal/domain/generation"
	"github.com/breakfix/breakfix/internal/domain/roadmap"
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

// CreateAuthoringChallengeRevision opens a fresh authoring conversation for
// an already published Challenge. The new session is only a proposal; it must
// complete the same generation and verification lifecycle before activation.
func (h *Handler) CreateAuthoringChallengeRevision(c *gin.Context, challengeID string) {
	user := h.requireUser(c)
	if user == nil {
		return
	}
	session, err := h.authoring.CreateRevision(c.Request.Context(), user.ID, challengeID)
	if err != nil {
		h.writeAuthoringError(c, err)
		return
	}
	h.writeAuthoringSession(c, user, session.ID)
}

// DeprecateAuthoringChallenge hides an author-owned Challenge from the active
// Catalog and rejects new Environments without deleting its immutable history.
func (h *Handler) DeprecateAuthoringChallenge(c *gin.Context, challengeID string) {
	user := h.requireUser(c)
	if user == nil {
		return
	}
	if h.db == nil {
		c.JSON(http.StatusServiceUnavailable, api.ErrorResponse{Error: "challenge lifecycle is unavailable"})
		return
	}
	if _, err := h.db.Challenge.DeprecateAuthoringChallenge(c.Request.Context(), user.ID, challengeID, time.Now().UTC()); err != nil {
		h.writeAuthoringError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
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

	requestCtx := c.Request.Context()
	content, err := h.authoring.RunTurn(h.agentRuntimeContext(), runID, func(event appauthoring.StreamEvent) {
		if requestCtx.Err() == nil {
			stream.WriteSSE(c, "delta", authoringStreamEvent{RunID: runID, Content: event.Content})
		}
	})
	if err == nil {
		if requestCtx.Err() == nil {
			stream.WriteSSE(c, "complete", authoringStreamEvent{RunID: runID, Content: content})
		}
		return
	}
	if requestCtx.Err() == nil {
		stream.WriteSSE(c, "error", authoringStreamEvent{RunID: runID, Content: err.Error()})
	}
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

	workflows := make([]api.GeneratorWorkflow, 0)
	if h.generator != nil {
		values, listErr := h.generator.ListActiveGenerations(c.Request.Context(), user.ID)
		if listErr != nil {
			c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: fmt.Sprintf("list authoring workflows: %v", listErr)})
			return
		}
		for _, workflow := range values {
			if workflow.Source.Ref == session.ID {
				workflows = append(workflows, toAPIGeneratorWorkflow(workflow))
			}
		}
	}
	c.JSON(http.StatusOK, toAPIAuthoringSession(session, revision, authoringTurnActive, messages, workflows))
}

func toAPIAuthoringSession(session *authoringdomain.Session, revision *authoringdomain.Revision, turnActive bool, messages []authoringdomain.Message, workflows []api.GeneratorWorkflow) api.AuthoringSession {
	return api.AuthoringSession{
		AuthoringTurnActive: turnActive, Id: session.ID, Intent: toAPIAuthoringPlan(revision.Plan),
		IntentRevision: int(session.CurrentRevision), LastError: optionalString(session.LastError), Messages: toAPIAuthoringMessages(messages),
		PublishChallengeId: optionalString(session.PublishChallengeID), State: api.AuthoringSessionState(session.State),
		RevisionChallengeId: optionalString(session.RevisionChallengeID), RevisionBaseActiveRevisionId: optionalString(session.RevisionBaseActiveRevisionID),
		UpdatedAt: session.UpdatedAt.UTC(), VisibleRevision: int(revision.Number), Workflows: workflows,
	}
}

func toAPIAuthoringCandidate(revision *generation.Revision) *api.AuthoringCandidate {
	if revision == nil {
		return nil
	}
	candidate := &api.AuthoringCandidate{Id: revision.ID, ArchiveSha256: revision.ArchiveSHA256}
	if revision.Failure != nil {
		failure := api.AuthoringCandidateFailure{
			Class:   api.AuthoringCandidateFailureClass(revision.Failure.Class),
			Code:    revision.Failure.Code,
			Summary: revision.Failure.Summary,
		}
		candidate.Failure = &failure
	}
	return candidate
}

func toAPIGeneratorWorkflow(workflow generation.Workflow) api.GeneratorWorkflow {
	var finalizerCategory *api.GeneratorWorkflowFinalizerErrorCategory
	if workflow.FinalizerErrorCategory.Valid() {
		value := api.GeneratorWorkflowFinalizerErrorCategory(workflow.FinalizerErrorCategory)
		finalizerCategory = &value
	}
	return api.GeneratorWorkflow{
		Id:                            workflow.ID,
		SessionId:                     workflow.Source.Ref,
		PlanRevision:                  generatorPlanRevision(workflow.SourceRevision),
		State:                         api.GeneratorWorkflowState(workflow.State),
		StateVersion:                  workflow.StateVersion,
		RuntimeAttempt:                workflow.RuntimeAttempt,
		CandidateRevisionId:           optionalString(workflow.CandidateRevisionID),
		ClassificationRoadmapRevision: optionalString(workflow.ClassificationRoadmapRevision),
		LastError:                     optionalString(workflow.LastError),
		FinalizerErrorCategory:        finalizerCategory,
		FinalizerLastError:            optionalString(workflow.FinalizerLastError),
		FinalizerLastAttemptedAt:      workflow.FinalizerLastAttemptedAt,
		FinalizerNextRetryAt:          workflow.FinalizerNextRetryAt,
		CreatedAt:                     workflow.CreatedAt.UTC(),
		UpdatedAt:                     workflow.UpdatedAt.UTC(),
	}
}

func generatorPlanRevision(value string) int64 {
	revision, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil || revision < 0 {
		return 0
	}
	return revision
}

func (h *Handler) authoringClassificationProposal(ctx context.Context, value *generation.ClassificationProposal) (*api.AuthoringClassificationProposal, error) {
	if value == nil {
		return nil, nil
	}
	var source *roadmap.Revision
	if value.Result == generation.ClassificationProposed {
		if h == nil || h.db == nil {
			return nil, errors.New("roadmap repository is unavailable for classification review")
		}
		var err error
		source, err = h.db.Roadmap.RoadmapRevision(ctx, value.RoadmapRevision)
		if err != nil {
			return nil, fmt.Errorf("load classification roadmap revision: %w", err)
		}
	}
	return toAPIAuthoringClassificationProposal(value, source)
}

func toAPIAuthoringClassificationProposal(value *generation.ClassificationProposal, source *roadmap.Revision) (*api.AuthoringClassificationProposal, error) {
	if value == nil {
		return nil, nil
	}
	result := &api.AuthoringClassificationProposal{
		Revision:             value.Revision,
		CandidateRevisionId:  value.CandidateRevisionID,
		RoadmapRevision:      value.RoadmapRevision,
		Result:               api.AuthoringClassificationProposalResult(value.Result),
		Tags:                 make([]api.AuthoringClassificationTag, 0, len(value.Tags)),
		UnclassifiableReason: optionalString(value.UnclassifiableReason),
		AdjustmentSuggestion: optionalString(value.AdjustmentSuggestion),
		UpdatedAt:            value.UpdatedAt.UTC(),
	}
	if value.Topic != nil {
		topic, err := toAPIAuthoringClassificationTopic(*value.Topic, source)
		if err != nil {
			return nil, err
		}
		result.Topic = topic
	}
	for _, tag := range value.Tags {
		converted, err := toAPIAuthoringClassificationTag(tag, source)
		if err != nil {
			return nil, err
		}
		result.Tags = append(result.Tags, converted)
	}
	return result, nil
}

func toAPIAuthoringClassificationTopic(value generation.TopicProposal, source *roadmap.Revision) (*api.AuthoringClassificationTopic, error) {
	result := &api.AuthoringClassificationTopic{Reason: value.Reason}
	if value.Existing != nil {
		topic, err := classificationTopicDefinition(source, *value.Existing)
		if err != nil {
			return nil, err
		}
		converted := toAPIRoadmapTopic(*topic)
		result.Existing = &converted
	}
	if value.New != nil {
		result.New = &api.AuthoringClassificationNewTopic{
			Domain:            toAPIRoadmapReference(value.New.Domain),
			Title:             value.New.Title,
			Definition:        value.New.Definition,
			Scope:             value.New.Scope,
			NonGoals:          value.New.NonGoals,
			ChallengeGuidance: value.New.ChallengeGuidance,
		}
	}
	return result, nil
}

func toAPIAuthoringClassificationTag(value generation.TagProposal, source *roadmap.Revision) (api.AuthoringClassificationTag, error) {
	result := api.AuthoringClassificationTag{Reason: value.Reason}
	if value.Existing != nil {
		tag, err := classificationTagDefinition(source, *value.Existing)
		if err != nil {
			return api.AuthoringClassificationTag{}, err
		}
		converted := toAPIRoadmapTag(*tag)
		result.Existing = &converted
	}
	if value.New != nil {
		result.New = &api.AuthoringClassificationNewTag{Title: value.New.Title, Description: value.New.Description}
	}
	return result, nil
}

func classificationTopicDefinition(source *roadmap.Revision, reference roadmap.Ref) (*roadmap.Topic, error) {
	if source == nil {
		return nil, errors.New("classification proposal has no roadmap revision")
	}
	for _, topic := range source.Topics {
		if topic.ID == reference.ID && topic.SourceRef == reference.SourceRef && topic.Title == reference.Title {
			value := topic
			return &value, nil
		}
	}
	return nil, fmt.Errorf("classification Topic %q is absent from revision %s", reference.SourceRef, source.Revision)
}

func classificationTagDefinition(source *roadmap.Revision, reference roadmap.Ref) (*roadmap.Tag, error) {
	if source == nil {
		return nil, errors.New("classification proposal has no roadmap revision")
	}
	for _, tag := range source.Tags {
		if tag.ID == reference.ID && tag.SourceRef == reference.SourceRef && tag.Title == reference.Title {
			value := tag
			return &value, nil
		}
	}
	return nil, fmt.Errorf("classification Tag %q is absent from revision %s", reference.SourceRef, source.Revision)
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
	case errors.Is(err, authoringdomain.ErrNotFound), errors.Is(err, challengedomain.ErrNotFound), errors.Is(err, generation.ErrCandidateNotFound), errors.Is(err, postgres.ErrGenerationWorkflowNotFound):
		c.JSON(http.StatusNotFound, api.ErrorResponse{Error: "authoring session, generation workflow, or candidate not found"})
	case errors.Is(err, authoringdomain.ErrVersionConflict), errors.Is(err, authoringdomain.ErrInvalidState), errors.Is(err, generation.ErrCandidateInvalidState),
		errors.Is(err, generation.ErrClassificationConflict), errors.Is(err, generation.ErrChallengeSourceRefConflict),
		errors.Is(err, challengedomain.ErrRevisionConflict), errors.Is(err, challengedomain.ErrNotMutable):
		c.JSON(http.StatusConflict, api.ErrorResponse{Error: err.Error()})
	default:
		c.JSON(http.StatusBadRequest, api.ErrorResponse{Error: err.Error()})
	}
}
