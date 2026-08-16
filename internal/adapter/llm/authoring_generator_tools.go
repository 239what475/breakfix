package llm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/breakfix/breakfix/internal/domain/authoring"
	"github.com/breakfix/breakfix/internal/domain/generation"
	"github.com/breakfix/breakfix/internal/domain/toolresult"
)

const authoringWorkspaceReleaseTimeout = 10 * time.Second

func (c *runtimeConversation) confirmGeneration(ctx context.Context, raw string) (string, error) {
	var args struct {
		PlanRevision int64 `json:"plan_revision"`
	}
	if err := decodeAuthoringToolArguments(raw, &args); err != nil {
		return "", err
	}
	if args.PlanRevision != c.stage.BaseRevision {
		return "", invalidToolInput(fmt.Errorf("只能确认当前已持久化的 Plan revision %d", c.stage.BaseRevision))
	}
	if len(c.stage.Changes) != 0 {
		return "", invalidToolInput(errors.New("本轮已修改私有 Plan；回合结束并持久化新 revision 后，等待作者在下一条消息明确确认"))
	}
	workflow, err := c.generator.ConfirmGeneration(ctx, c.userID, c.sessionID, generation.StartConfirmation{
		PlanRevision:   args.PlanRevision,
		IdempotencyKey: c.idempotencyKey("confirm-generation", fmt.Sprintf("%d", args.PlanRevision)),
	})
	if err != nil {
		return "", generatorToolError(err)
	}
	return marshalAuthoringToolResult(struct {
		Workflow generation.Workflow `json:"workflow"`
	}{Workflow: *workflow})
}

func (c *runtimeConversation) listActiveGenerations(ctx context.Context, raw string) (string, error) {
	var args struct{}
	if err := decodeAuthoringToolArguments(raw, &args); err != nil {
		return "", err
	}
	workflows, err := c.generator.ListActiveGenerations(ctx, c.userID)
	if err != nil {
		return "", generatorToolError(err)
	}
	return marshalAuthoringToolResult(struct {
		Workflows []generation.Workflow `json:"workflows"`
	}{Workflows: workflows})
}

func (c *runtimeConversation) getGeneration(ctx context.Context, raw string) (string, error) {
	var args struct {
		WorkflowID string `json:"workflow_id"`
	}
	if err := decodeAuthoringToolArguments(raw, &args); err != nil {
		return "", err
	}
	if strings.TrimSpace(args.WorkflowID) == "" {
		return "", invalidToolInput(errors.New("workflow_id 不能为空"))
	}
	workflow, err := c.generator.GetGenerationWorkflow(ctx, c.userID, strings.TrimSpace(args.WorkflowID))
	if err != nil {
		return "", generatorToolError(err)
	}
	candidate, err := c.generator.GetGenerationCandidate(ctx, c.userID, workflow.ID)
	if err != nil {
		return "", generatorToolError(err)
	}
	return marshalAuthoringToolResult(struct {
		Workflow  generation.Workflow  `json:"workflow"`
		Candidate *generation.Revision `json:"candidate,omitempty"`
	}{Workflow: *workflow, Candidate: candidate})
}

func (c *runtimeConversation) listWorkspaceFiles(ctx context.Context, raw string) (string, error) {
	var args struct {
		WorkflowID string `json:"workflow_id"`
	}
	if err := decodeAuthoringToolArguments(raw, &args); err != nil {
		return "", err
	}
	turn, err := c.workspaceTurn(ctx, args.WorkflowID)
	if err != nil {
		return "", err
	}
	files, err := c.generator.ListWorkspaceFiles(ctx, c.userID, turn)
	if err != nil {
		return "", generatorToolError(err)
	}
	return marshalAuthoringToolResult(struct {
		WorkflowID string                     `json:"workflow_id"`
		Files      []generation.WorkspaceFile `json:"files"`
	}{WorkflowID: turn.WorkflowID, Files: files})
}

func (c *runtimeConversation) readWorkspaceFile(ctx context.Context, raw string) (string, error) {
	var args struct {
		WorkflowID string `json:"workflow_id"`
		Path       string `json:"path"`
		Offset     int    `json:"offset"`
		Limit      int    `json:"limit"`
	}
	if err := decodeAuthoringToolArguments(raw, &args); err != nil {
		return "", err
	}
	if strings.TrimSpace(args.Path) == "" {
		return "", invalidToolInput(errors.New("path 不能为空"))
	}
	turn, err := c.workspaceTurn(ctx, args.WorkflowID)
	if err != nil {
		return "", err
	}
	content, err := c.generator.ReadWorkspaceContent(ctx, c.userID, turn, args.Path, args.Offset, args.Limit)
	if err != nil {
		return "", generatorToolError(err)
	}
	return marshalAuthoringToolResult(struct {
		WorkflowID string `json:"workflow_id"`
		Path       string `json:"path"`
		Content    string `json:"content"`
	}{WorkflowID: turn.WorkflowID, Path: strings.TrimSpace(args.Path), Content: content})
}

func (c *runtimeConversation) writeWorkspaceFile(ctx context.Context, raw string) (string, error) {
	var args struct {
		WorkflowID string `json:"workflow_id"`
		Path       string `json:"path"`
		Content    string `json:"content"`
	}
	if err := decodeAuthoringToolArguments(raw, &args); err != nil {
		return "", err
	}
	if strings.TrimSpace(args.Path) == "" {
		return "", invalidToolInput(errors.New("path 不能为空"))
	}
	turn, err := c.workspaceTurn(ctx, args.WorkflowID)
	if err != nil {
		return "", err
	}
	if err := c.generator.WriteWorkspaceFile(ctx, c.userID, turn, args.Path, args.Content); err != nil {
		return "", generatorToolError(err)
	}
	return marshalAuthoringToolResult(struct {
		WorkflowID string `json:"workflow_id"`
		Path       string `json:"path"`
	}{WorkflowID: turn.WorkflowID, Path: strings.TrimSpace(args.Path)})
}

func (c *runtimeConversation) runWorkspaceCommand(ctx context.Context, raw string) (string, error) {
	var args struct {
		WorkflowID string `json:"workflow_id"`
		Command    string `json:"command"`
	}
	if err := decodeAuthoringToolArguments(raw, &args); err != nil {
		return "", err
	}
	if strings.TrimSpace(args.Command) == "" {
		return "", invalidToolInput(errors.New("command 不能为空"))
	}
	turn, err := c.workspaceTurn(ctx, args.WorkflowID)
	if err != nil {
		return "", err
	}
	result, err := c.generator.ExecuteWorkspaceCommand(ctx, c.userID, turn, args.Command)
	if err != nil {
		return "", generatorToolError(err)
	}
	return toolresult.Marshal(result)
}

func (c *runtimeConversation) submitCandidate(ctx context.Context, raw string) (string, error) {
	var args struct {
		WorkflowID string `json:"workflow_id"`
	}
	if err := decodeAuthoringToolArguments(raw, &args); err != nil {
		return "", err
	}
	turn, err := c.workspaceTurn(ctx, args.WorkflowID)
	if err != nil {
		return "", err
	}
	revision, err := c.generator.SubmitCandidate(ctx, c.userID, generation.CandidateSubmission{
		WorkflowID:     turn.WorkflowID,
		TurnID:         turn.ID,
		IdempotencyKey: c.idempotencyKey("submit-candidate", turn.WorkflowID),
	})
	if err != nil {
		return "", generatorToolError(err)
	}
	// A successful submission ends its workspace turn atomically with the
	// workflow transition. An unknown or rejected submission keeps this turn so
	// the Agent can inspect and correct the workspace in the same run.
	c.turn = nil
	return marshalAuthoringToolResult(struct {
		Candidate generation.Revision `json:"candidate"`
	}{Candidate: *revision})
}

func (c *runtimeConversation) confirmContent(ctx context.Context, raw string) (string, error) {
	var args struct {
		WorkflowID          string `json:"workflow_id"`
		CandidateRevisionID string `json:"candidate_revision_id"`
	}
	if err := decodeAuthoringToolArguments(raw, &args); err != nil {
		return "", err
	}
	if strings.TrimSpace(args.WorkflowID) == "" || strings.TrimSpace(args.CandidateRevisionID) == "" {
		return "", invalidToolInput(errors.New("workflow_id 和 candidate_revision_id 不能为空"))
	}
	workflow, err := c.generator.ConfirmContent(ctx, c.userID, generation.ContentConfirmation{
		WorkflowID:          strings.TrimSpace(args.WorkflowID),
		CandidateRevisionID: strings.TrimSpace(args.CandidateRevisionID),
		IdempotencyKey:      c.idempotencyKey("confirm-content", args.WorkflowID, args.CandidateRevisionID),
	})
	if err != nil {
		return "", generatorToolError(err)
	}
	return marshalWorkflowToolResult(workflow)
}

func (c *runtimeConversation) requestContentChanges(ctx context.Context, raw string) (string, error) {
	var args struct {
		WorkflowID          string `json:"workflow_id"`
		CandidateRevisionID string `json:"candidate_revision_id"`
		Feedback            string `json:"feedback"`
	}
	if err := decodeAuthoringToolArguments(raw, &args); err != nil {
		return "", err
	}
	if strings.TrimSpace(args.WorkflowID) == "" || strings.TrimSpace(args.CandidateRevisionID) == "" || strings.TrimSpace(args.Feedback) == "" {
		return "", invalidToolInput(errors.New("workflow_id、candidate_revision_id 和 feedback 不能为空"))
	}
	workflow, err := c.generator.RequestContentChanges(ctx, c.userID, generation.ContentChangeRequest{
		WorkflowID:          strings.TrimSpace(args.WorkflowID),
		CandidateRevisionID: strings.TrimSpace(args.CandidateRevisionID),
		Feedback:            strings.TrimSpace(args.Feedback),
		IdempotencyKey:      c.idempotencyKey("request-content-changes", args.WorkflowID, args.CandidateRevisionID),
	})
	if err != nil {
		return "", generatorToolError(err)
	}
	return marshalWorkflowToolResult(workflow)
}

func (c *runtimeConversation) getClassification(ctx context.Context, raw string) (string, error) {
	var args struct {
		WorkflowID string `json:"workflow_id"`
	}
	if err := decodeAuthoringToolArguments(raw, &args); err != nil {
		return "", err
	}
	if strings.TrimSpace(args.WorkflowID) == "" {
		return "", invalidToolInput(errors.New("workflow_id 不能为空"))
	}
	workflow, err := c.generator.GetGenerationWorkflow(ctx, c.userID, strings.TrimSpace(args.WorkflowID))
	if err != nil {
		return "", generatorToolError(err)
	}
	candidate, err := c.generator.GetGenerationCandidate(ctx, c.userID, workflow.ID)
	if err != nil {
		return "", generatorToolError(err)
	}
	if candidate == nil {
		return "", invalidToolInput(errors.New("当前任务还没有 candidate，无法读取分类提案"))
	}
	return marshalAuthoringToolResult(struct {
		WorkflowID          string                             `json:"workflow_id"`
		CandidateRevisionID string                             `json:"candidate_revision_id"`
		Classification      *generation.ClassificationProposal `json:"classification,omitempty"`
	}{WorkflowID: workflow.ID, CandidateRevisionID: candidate.ID, Classification: candidate.Classification})
}

func (c *runtimeConversation) requestClassificationChanges(ctx context.Context, raw string) (string, error) {
	var args struct {
		WorkflowID          string `json:"workflow_id"`
		CandidateRevisionID string `json:"candidate_revision_id"`
		ProposalRevision    int    `json:"proposal_revision"`
		Feedback            string `json:"feedback"`
	}
	if err := decodeAuthoringToolArguments(raw, &args); err != nil {
		return "", err
	}
	if strings.TrimSpace(args.WorkflowID) == "" || strings.TrimSpace(args.CandidateRevisionID) == "" || args.ProposalRevision < 1 || strings.TrimSpace(args.Feedback) == "" {
		return "", invalidToolInput(errors.New("workflow_id、candidate_revision_id、正数 proposal_revision 和 feedback 都不能为空"))
	}
	workflow, err := c.generator.RequestClassificationChanges(ctx, c.userID, generation.ClassificationAdjustmentConfirmation{
		WorkflowID:          strings.TrimSpace(args.WorkflowID),
		CandidateRevisionID: strings.TrimSpace(args.CandidateRevisionID),
		ProposalRevision:    args.ProposalRevision,
		Feedback:            strings.TrimSpace(args.Feedback),
		IdempotencyKey:      c.idempotencyKey("request-classification-changes", args.WorkflowID, args.CandidateRevisionID, fmt.Sprintf("%d", args.ProposalRevision)),
	})
	if err != nil {
		return "", generatorToolError(err)
	}
	return marshalWorkflowToolResult(workflow)
}

func (c *runtimeConversation) confirmClassificationAndPublish(ctx context.Context, raw string) (string, error) {
	var args struct {
		WorkflowID          string `json:"workflow_id"`
		CandidateRevisionID string `json:"candidate_revision_id"`
		ProposalRevision    int    `json:"proposal_revision"`
	}
	if err := decodeAuthoringToolArguments(raw, &args); err != nil {
		return "", err
	}
	if strings.TrimSpace(args.WorkflowID) == "" || strings.TrimSpace(args.CandidateRevisionID) == "" || args.ProposalRevision < 1 {
		return "", invalidToolInput(errors.New("workflow_id、candidate_revision_id 和正数 proposal_revision 不能为空"))
	}
	workflow, err := c.generator.ConfirmClassificationAndPublish(ctx, c.userID, generation.PublicationConfirmation{
		WorkflowID:          strings.TrimSpace(args.WorkflowID),
		CandidateRevisionID: strings.TrimSpace(args.CandidateRevisionID),
		ProposalRevision:    args.ProposalRevision,
		IdempotencyKey:      c.idempotencyKey("confirm-classification-and-publish", args.WorkflowID, args.CandidateRevisionID, fmt.Sprintf("%d", args.ProposalRevision)),
	})
	if err != nil {
		return "", generatorToolError(err)
	}
	return marshalWorkflowToolResult(workflow)
}

func (c *runtimeConversation) cancelGeneration(ctx context.Context, raw string) (string, error) {
	var args struct {
		WorkflowID string `json:"workflow_id"`
	}
	if err := decodeAuthoringToolArguments(raw, &args); err != nil {
		return "", err
	}
	if strings.TrimSpace(args.WorkflowID) == "" {
		return "", invalidToolInput(errors.New("workflow_id 不能为空"))
	}
	workflow, err := c.generator.CancelGeneration(ctx, c.userID, generation.Cancellation{
		WorkflowID:     strings.TrimSpace(args.WorkflowID),
		IdempotencyKey: c.idempotencyKey("cancel-generation", args.WorkflowID),
	})
	if err != nil {
		return "", generatorToolError(err)
	}
	if c.turn != nil && c.turn.WorkflowID == workflow.ID {
		c.turn = nil
	}
	return marshalWorkflowToolResult(workflow)
}

func (c *runtimeConversation) workspaceTurn(ctx context.Context, workflowID string) (generation.WorkspaceTurn, error) {
	workflowID = strings.TrimSpace(workflowID)
	if workflowID == "" {
		return generation.WorkspaceTurn{}, invalidToolInput(errors.New("workflow_id 不能为空"))
	}
	if c.turn != nil {
		if c.turn.WorkflowID != workflowID {
			return generation.WorkspaceTurn{}, invalidToolInput(fmt.Errorf("当前 Authoring 回合已绑定任务 %q，不能同时操作 %q", c.turn.WorkflowID, workflowID))
		}
		return *c.turn, nil
	}
	turn := generation.WorkspaceTurn{WorkflowID: workflowID, ID: c.runID}
	if err := c.generator.StartWorkspaceTurn(ctx, c.userID, turn); err != nil {
		return generation.WorkspaceTurn{}, generatorToolError(err)
	}
	c.turn = &turn
	return turn, nil
}

func (c *runtimeConversation) releaseWorkspaceTurn() {
	if c == nil || c.generator == nil || c.turn == nil {
		return
	}
	turn := *c.turn
	c.turn = nil
	c.releaseTurn(turn)
}

func (c *runtimeConversation) releaseTurn(turn generation.WorkspaceTurn) {
	if c == nil || c.generator == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), authoringWorkspaceReleaseTimeout)
	defer cancel()
	_ = c.generator.EndWorkspaceTurn(ctx, c.userID, turn)
}

func (c *runtimeConversation) idempotencyKey(action string, values ...string) string {
	parts := make([]string, 0, len(values)+3)
	parts = append(parts, c.runID, c.userMessageID, action)
	parts = append(parts, values...)
	digest := sha256.Sum256([]byte(strings.Join(parts, "\x1f")))
	return "authoring-" + action + "-" + hex.EncodeToString(digest[:12])
}

func marshalWorkflowToolResult(workflow *generation.Workflow) (string, error) {
	if workflow == nil {
		return "", errors.New("generator service returned an empty workflow")
	}
	return marshalAuthoringToolResult(struct {
		Workflow generation.Workflow `json:"workflow"`
	}{Workflow: *workflow})
}

func marshalAuthoringToolResult(value any) (string, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(payload), nil
}

func generatorToolError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, authoring.ErrNotFound) || errors.Is(err, authoring.ErrVersionConflict) || errors.Is(err, authoring.ErrInvalidState) ||
		errors.Is(err, generation.ErrWorkspaceNotFound) || errors.Is(err, generation.ErrWorkspaceBusy) || errors.Is(err, generation.ErrWorkspaceTurnLost) ||
		errors.Is(err, generation.ErrCandidateNotFound) || errors.Is(err, generation.ErrCandidateInvalidState) {
		return invalidToolInput(err)
	}
	return err
}
