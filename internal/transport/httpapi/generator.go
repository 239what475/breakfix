package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	appauthoring "github.com/breakfix/breakfix/internal/application/authoring"
	appgeneration "github.com/breakfix/breakfix/internal/application/generation"
	"github.com/breakfix/breakfix/internal/content/candidate"
	authoringdomain "github.com/breakfix/breakfix/internal/domain/authoring"
	"github.com/breakfix/breakfix/internal/domain/generation"
	api "github.com/breakfix/breakfix/internal/transport/httpapi/generated"
	"github.com/gin-gonic/gin"
)

func (h *Handler) SetGenerationPlan(c *gin.Context) {
	user := h.requireUser(c)
	if user == nil {
		return
	}
	service, ok := h.requireGenerator(c)
	if !ok {
		return
	}
	var request api.GeneratorPlanRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.writeGeneratorError(c, err)
		return
	}
	sessionID := ""
	if request.SessionId != nil {
		sessionID = *request.SessionId
	}
	session, revision, err := service.SetGenerationPlan(c.Request.Context(), user.ID, sessionID, request.ExpectedRevision, request.IdempotencyKey, fromAPIAuthoringPlan(request.Plan))
	if err != nil {
		h.writeGeneratorError(c, err)
		return
	}
	c.JSON(http.StatusOK, api.GeneratorPlanResponse{
		SessionId: session.ID, PlanRevision: revision.Number, Plan: toAPIAuthoringPlan(revision.Plan),
	})
}

func (h *Handler) ListActiveGenerations(c *gin.Context) {
	user := h.requireUser(c)
	if user == nil {
		return
	}
	service, ok := h.requireGenerator(c)
	if !ok {
		return
	}
	workflows, err := service.ListActiveGenerations(c.Request.Context(), user.ID)
	if err != nil {
		h.writeGeneratorError(c, err)
		return
	}
	result := make([]api.GeneratorWorkflow, 0, len(workflows))
	for _, workflow := range workflows {
		result = append(result, toAPIGeneratorWorkflow(workflow))
	}
	c.JSON(http.StatusOK, api.GeneratorWorkflowList{Workflows: result})
}

func (h *Handler) ConfirmGeneration(c *gin.Context) {
	user := h.requireUser(c)
	if user == nil {
		return
	}
	service, ok := h.requireGenerator(c)
	if !ok {
		return
	}
	var request api.GeneratorGenerationConfirmationRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.writeGeneratorError(c, err)
		return
	}
	workflow, err := service.ConfirmGeneration(c.Request.Context(), user.ID, request.SessionId, generation.StartConfirmation{
		PlanRevision: request.PlanRevision, IdempotencyKey: request.IdempotencyKey,
	})
	if err != nil {
		h.writeGeneratorError(c, err)
		return
	}
	c.JSON(http.StatusOK, toAPIGeneratorWorkflow(*workflow))
}

func (h *Handler) GetGeneration(c *gin.Context, workflowID string) {
	user := h.requireUser(c)
	if user == nil {
		return
	}
	service, ok := h.requireGenerator(c)
	if !ok {
		return
	}
	view, err := service.GetGeneration(c.Request.Context(), user.ID, workflowID)
	if err != nil {
		h.writeGeneratorError(c, err)
		return
	}
	response, err := h.toAPIGeneratorGeneration(c.Request.Context(), view.Workflow, view.Candidate)
	if err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: fmt.Sprintf("read generator review: %v", err)})
		return
	}
	c.JSON(http.StatusOK, response)
}

func (h *Handler) StartGeneratorWorkspaceTurn(c *gin.Context, workflowID string) {
	user := h.requireUser(c)
	if user == nil {
		return
	}
	service, ok := h.requireGenerator(c)
	if !ok {
		return
	}
	var request api.GeneratorWorkspaceTurnRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.writeGeneratorError(c, err)
		return
	}
	turn := generatorWorkspaceTurn(workflowID, request.TurnId)
	if err := service.StartWorkspaceTurn(c.Request.Context(), user.ID, turn); err != nil {
		h.writeGeneratorError(c, err)
		return
	}
	c.JSON(http.StatusOK, api.GeneratorWorkspaceTurn{WorkflowId: workflowID, TurnId: request.TurnId})
}

func (h *Handler) EndGeneratorWorkspaceTurn(c *gin.Context, workflowID string) {
	user := h.requireUser(c)
	if user == nil {
		return
	}
	service, ok := h.requireGenerator(c)
	if !ok {
		return
	}
	var request api.GeneratorWorkspaceTurnRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.writeGeneratorError(c, err)
		return
	}
	if err := service.EndWorkspaceTurn(c.Request.Context(), user.ID, generatorWorkspaceTurn(workflowID, request.TurnId)); err != nil {
		h.writeGeneratorError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) ListGeneratorWorkspaceFiles(c *gin.Context, workflowID string, params api.ListGeneratorWorkspaceFilesParams) {
	user := h.requireUser(c)
	if user == nil {
		return
	}
	service, ok := h.requireGenerator(c)
	if !ok {
		return
	}
	files, err := service.ListWorkspaceFiles(c.Request.Context(), user.ID, generatorWorkspaceTurn(workflowID, params.TurnId))
	if err != nil {
		h.writeGeneratorError(c, err)
		return
	}
	result := make([]api.GeneratorWorkspaceFile, 0, len(files))
	for _, file := range files {
		result = append(result, api.GeneratorWorkspaceFile{Path: file.Path, Directory: file.Directory, Size: file.Size})
	}
	c.JSON(http.StatusOK, api.GeneratorWorkspaceFileList{WorkflowId: workflowID, TurnId: params.TurnId, Files: result})
}

func (h *Handler) ReadGeneratorWorkspaceFile(c *gin.Context, workflowID string, params api.ReadGeneratorWorkspaceFileParams) {
	user := h.requireUser(c)
	if user == nil {
		return
	}
	service, ok := h.requireGenerator(c)
	if !ok {
		return
	}
	offset, limit := 0, 0
	if params.Offset != nil {
		offset = *params.Offset
	}
	if params.Limit != nil {
		limit = *params.Limit
	}
	value, err := service.ReadWorkspaceFile(c.Request.Context(), user.ID, generatorWorkspaceTurn(workflowID, params.TurnId), params.Path, offset, limit)
	if err != nil {
		h.writeGeneratorError(c, err)
		return
	}
	c.JSON(http.StatusOK, api.GeneratorWorkspaceFileRead{WorkflowId: workflowID, TurnId: params.TurnId, Path: params.Path, Content: value.Content})
}

func (h *Handler) WriteGeneratorWorkspaceFile(c *gin.Context, workflowID string) {
	user := h.requireUser(c)
	if user == nil {
		return
	}
	service, ok := h.requireGenerator(c)
	if !ok {
		return
	}
	var request api.GeneratorWorkspaceFileWriteRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.writeGeneratorError(c, err)
		return
	}
	if err := service.WriteWorkspaceFile(c.Request.Context(), user.ID, generatorWorkspaceTurn(workflowID, request.TurnId), request.Path, request.Content); err != nil {
		h.writeGeneratorError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) RunGeneratorWorkspaceCommand(c *gin.Context, workflowID string) {
	user := h.requireUser(c)
	if user == nil {
		return
	}
	service, ok := h.requireGenerator(c)
	if !ok {
		return
	}
	var request api.GeneratorWorkspaceCommandRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.writeGeneratorError(c, err)
		return
	}
	result, err := service.ExecuteWorkspaceCommand(c.Request.Context(), user.ID, generatorWorkspaceTurn(workflowID, request.TurnId), request.Command)
	if err != nil {
		h.writeGeneratorError(c, err)
		return
	}
	var command appgeneration.WorkspaceCommand
	if len(result.Data) > 0 {
		if err := json.Unmarshal(result.Data, &command); err != nil {
			c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: "invalid workspace command result"})
			return
		}
	}
	if command.WorkflowID == "" {
		command.WorkflowID = workflowID
	}
	exitCode, output := command.ExitCode, command.Output
	var commandError *string
	if strings.TrimSpace(result.Error) != "" {
		value := result.Error
		commandError = &value
	}
	c.JSON(http.StatusOK, api.GeneratorWorkspaceCommandResult{
		WorkflowId: command.WorkflowID,
		TurnId:     request.TurnId,
		Status:     api.GeneratorWorkspaceCommandResultStatus(result.Status),
		ExitCode:   &exitCode,
		Output:     &output,
		Error:      commandError,
	})
}

func (h *Handler) SubmitGeneratorCandidate(c *gin.Context, workflowID string) {
	user := h.requireUser(c)
	if user == nil {
		return
	}
	service, ok := h.requireGenerator(c)
	if !ok {
		return
	}
	var request api.GeneratorCandidateSubmissionRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.writeGeneratorError(c, err)
		return
	}
	revision, err := service.SubmitCandidate(c.Request.Context(), user.ID, generation.CandidateSubmission{
		WorkflowID: workflowID, TurnID: request.TurnId, IdempotencyKey: request.IdempotencyKey,
	})
	if err != nil {
		h.writeGeneratorError(c, err)
		return
	}
	workflow, err := service.GetGenerationWorkflow(c.Request.Context(), user.ID, workflowID)
	if err != nil {
		h.writeGeneratorError(c, err)
		return
	}
	response, err := h.toAPIGeneratorGeneration(c.Request.Context(), *workflow, revision)
	if err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: fmt.Sprintf("read submitted candidate: %v", err)})
		return
	}
	c.JSON(http.StatusOK, response)
}

func (h *Handler) ConfirmGeneratorContent(c *gin.Context, workflowID string) {
	user := h.requireUser(c)
	if user == nil {
		return
	}
	service, ok := h.requireGenerator(c)
	if !ok {
		return
	}
	var request api.GeneratorContentConfirmationRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.writeGeneratorError(c, err)
		return
	}
	workflow, err := service.ConfirmContent(c.Request.Context(), user.ID, generation.ContentConfirmation{
		WorkflowID: workflowID, CandidateRevisionID: request.CandidateRevisionId, IdempotencyKey: request.IdempotencyKey,
	})
	if err != nil {
		h.writeGeneratorError(c, err)
		return
	}
	c.JSON(http.StatusOK, toAPIGeneratorWorkflow(*workflow))
}

func (h *Handler) RequestGeneratorContentChanges(c *gin.Context, workflowID string) {
	user := h.requireUser(c)
	if user == nil {
		return
	}
	service, ok := h.requireGenerator(c)
	if !ok {
		return
	}
	var request api.GeneratorContentChangeRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.writeGeneratorError(c, err)
		return
	}
	workflow, err := service.RequestContentChanges(c.Request.Context(), user.ID, generation.ContentChangeRequest{
		WorkflowID: workflowID, CandidateRevisionID: request.CandidateRevisionId, Feedback: request.Feedback, IdempotencyKey: request.IdempotencyKey,
	})
	if err != nil {
		h.writeGeneratorError(c, err)
		return
	}
	c.JSON(http.StatusOK, toAPIGeneratorWorkflow(*workflow))
}

func (h *Handler) GetGeneratorClassification(c *gin.Context, workflowID string) {
	user := h.requireUser(c)
	if user == nil {
		return
	}
	service, ok := h.requireGenerator(c)
	if !ok {
		return
	}
	view, err := service.GetGeneration(c.Request.Context(), user.ID, workflowID)
	if err != nil {
		h.writeGeneratorError(c, err)
		return
	}
	if view.Candidate == nil || view.Candidate.Classification == nil {
		h.writeGeneratorError(c, authoringdomain.ErrInvalidState)
		return
	}
	classification, err := h.authoringClassificationProposal(c.Request.Context(), view.Candidate.Classification)
	if err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: fmt.Sprintf("read classification review: %v", err)})
		return
	}
	c.JSON(http.StatusOK, api.GeneratorClassificationReview{
		Workflow: toAPIGeneratorWorkflow(view.Workflow), Candidate: *toAPIAuthoringCandidate(view.Candidate), Classification: *classification,
	})
}

func (h *Handler) RequestGeneratorClassificationChanges(c *gin.Context, workflowID string) {
	user := h.requireUser(c)
	if user == nil {
		return
	}
	service, ok := h.requireGenerator(c)
	if !ok {
		return
	}
	var request api.GeneratorClassificationChangeRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.writeGeneratorError(c, err)
		return
	}
	workflow, err := service.RequestClassificationChanges(c.Request.Context(), user.ID, generation.ClassificationAdjustmentConfirmation{
		WorkflowID: workflowID, CandidateRevisionID: request.CandidateRevisionId, ProposalRevision: request.ProposalRevision,
		Feedback: request.Feedback, IdempotencyKey: request.IdempotencyKey,
	})
	if err != nil {
		h.writeGeneratorError(c, err)
		return
	}
	c.JSON(http.StatusOK, toAPIGeneratorWorkflow(*workflow))
}

func (h *Handler) ConfirmGeneratorClassificationAndPublish(c *gin.Context, workflowID string) {
	user := h.requireUser(c)
	if user == nil {
		return
	}
	service, ok := h.requireGenerator(c)
	if !ok {
		return
	}
	var request api.GeneratorClassificationPublicationRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.writeGeneratorError(c, err)
		return
	}
	workflow, err := service.ConfirmClassificationAndPublish(c.Request.Context(), user.ID, generation.PublicationConfirmation{
		WorkflowID: workflowID, CandidateRevisionID: request.CandidateRevisionId, ProposalRevision: request.ProposalRevision,
		IdempotencyKey: request.IdempotencyKey,
	})
	if err != nil {
		h.writeGeneratorError(c, err)
		return
	}
	c.JSON(http.StatusOK, toAPIGeneratorWorkflow(*workflow))
}

func (h *Handler) CancelGeneration(c *gin.Context, workflowID string) {
	user := h.requireUser(c)
	if user == nil {
		return
	}
	service, ok := h.requireGenerator(c)
	if !ok {
		return
	}
	var request api.GeneratorCancellationRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.writeGeneratorError(c, err)
		return
	}
	workflow, err := service.CancelGeneration(c.Request.Context(), user.ID, generation.Cancellation{WorkflowID: workflowID, IdempotencyKey: request.IdempotencyKey})
	if err != nil {
		h.writeGeneratorError(c, err)
		return
	}
	c.JSON(http.StatusOK, toAPIGeneratorWorkflow(*workflow))
}

func (h *Handler) toAPIGeneratorGeneration(ctx context.Context, workflow generation.Workflow, revision *generation.Revision) (api.GeneratorGeneration, error) {
	response := api.GeneratorGeneration{
		Workflow: toAPIGeneratorWorkflow(workflow), Assets: []api.AuthoringAsset{}, Diff: []api.AuthoringFileDiff{},
	}
	if revision == nil {
		return response, nil
	}
	response.Candidate = toAPIAuthoringCandidate(revision)
	archive, err := candidate.ReadArchive(revision.ArchivePath, revision.ArchiveSHA256)
	if err != nil {
		return api.GeneratorGeneration{}, err
	}
	assets, err := appauthoring.ReadAssets(archive)
	if err != nil {
		return api.GeneratorGeneration{}, err
	}
	response.Assets = make([]api.AuthoringAsset, 0, len(assets))
	for _, asset := range assets {
		response.Assets = append(response.Assets, api.AuthoringAsset{Path: asset.Path, Content: asset.Content})
	}
	if revision.ParentCandidateID != "" {
		previous, err := h.db.Generation.GetCandidateRevision(ctx, revision.ParentCandidateID)
		if err != nil {
			return api.GeneratorGeneration{}, err
		}
		previousArchive, err := candidate.ReadArchive(previous.ArchivePath, previous.ArchiveSHA256)
		if err != nil {
			return api.GeneratorGeneration{}, err
		}
		diff, err := appauthoring.DiffAssets(archive, previousArchive)
		if err != nil {
			return api.GeneratorGeneration{}, err
		}
		response.Diff = make([]api.AuthoringFileDiff, 0, len(diff))
		for _, entry := range diff {
			response.Diff = append(response.Diff, api.AuthoringFileDiff{Path: entry.Path, Diff: entry.Diff})
		}
	}
	verified, err := appauthoring.ReadVerifiedChallenge(archive)
	if err != nil {
		return api.GeneratorGeneration{}, err
	}
	response.Verified = toAPIVerifiedChallenge(verified)
	response.Verification = toAPIAuthoringVerificationReport(revision.Verification)
	classification, err := h.authoringClassificationProposal(ctx, revision.Classification)
	if err != nil {
		return api.GeneratorGeneration{}, err
	}
	response.Classification = classification
	return response, nil
}

func generatorWorkspaceTurn(workflowID, turnID string) generation.WorkspaceTurn {
	return generation.WorkspaceTurn{WorkflowID: workflowID, ID: turnID}
}

func fromAPIAuthoringPlan(value api.AuthoringPlan) authoringdomain.Plan {
	checkpoints := make([]authoringdomain.Checkpoint, 0, len(value.Checkpoints))
	for _, checkpoint := range value.Checkpoints {
		checkpoints = append(checkpoints, authoringdomain.Checkpoint{
			ID: checkpoint.Id, Title: checkpoint.Title, Markdown: checkpoint.Markdown, Position: checkpoint.Position,
		})
	}
	return authoringdomain.Plan{
		Metadata: authoringdomain.Metadata{
			Title: value.Metadata.Title, Difficulty: string(value.Metadata.Difficulty), Description: value.Metadata.Description, Runtime: string(value.Metadata.Runtime),
		},
		Overview: value.Overview, Checkpoints: checkpoints,
	}
}

func (h *Handler) requireGenerator(c *gin.Context) (generatorApplication, bool) {
	if h == nil || h.generator == nil {
		c.JSON(http.StatusServiceUnavailable, api.ErrorResponse{Error: "generator service is unavailable"})
		return nil, false
	}
	return h.generator, true
}

func (h *Handler) writeGeneratorError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, generation.ErrWorkspaceBusy), errors.Is(err, generation.ErrWorkspaceTurnLost),
		errors.Is(err, authoringdomain.ErrVersionConflict), errors.Is(err, authoringdomain.ErrInvalidState),
		errors.Is(err, generation.ErrCandidateInvalidState), errors.Is(err, generation.ErrClassificationConflict),
		errors.Is(err, generation.ErrChallengeSourceRefConflict):
		c.JSON(http.StatusConflict, api.ErrorResponse{Error: err.Error()})
	default:
		h.writeAuthoringError(c, err)
	}
}
