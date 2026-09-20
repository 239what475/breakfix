package mcpconnector

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/breakfix/breakfix/internal/domain/toolresult"
	api "github.com/breakfix/breakfix/internal/transport/httpapi/generated"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	defaultPollInterval = time.Second
	defaultWaitTimeout  = 30 * time.Second
	maxWaitTimeout      = time.Minute
)

// GeneratorAPI is the public JWT-authenticated Generator application surface.
// It deliberately mirrors no Server storage or provider APIs.
type GeneratorAPI interface {
	SetGenerationPlan(context.Context, api.GeneratorPlanRequest) (api.GeneratorPlanResponse, error)
	ConfirmGeneration(context.Context, api.GeneratorGenerationConfirmationRequest) (api.GeneratorWorkflow, error)
	ListActiveGenerations(context.Context) (api.GeneratorWorkflowList, error)
	GetGeneration(context.Context, string) (api.GeneratorGeneration, error)
	StartWorkspaceTurn(context.Context, string, api.GeneratorWorkspaceTurnRequest) (api.GeneratorWorkspaceTurn, error)
	EndWorkspaceTurn(context.Context, string, api.GeneratorWorkspaceTurnRequest) error
	ListWorkspaceFiles(context.Context, string, string) (api.GeneratorWorkspaceFileList, error)
	ReadWorkspaceFile(context.Context, string, string, string, *int, *int) (api.GeneratorWorkspaceFileRead, error)
	WriteWorkspaceFile(context.Context, string, api.GeneratorWorkspaceFileWriteRequest) error
	RunWorkspaceCommand(context.Context, string, api.GeneratorWorkspaceCommandRequest) (api.GeneratorWorkspaceCommandResult, error)
	SubmitCandidate(context.Context, string, api.GeneratorCandidateSubmissionRequest) (api.GeneratorGeneration, error)
	ConfirmContent(context.Context, string, api.GeneratorContentConfirmationRequest) (api.GeneratorWorkflow, error)
	RequestContentChanges(context.Context, string, api.GeneratorContentChangeRequest) (api.GeneratorWorkflow, error)
	CancelGeneration(context.Context, string, api.GeneratorCancellationRequest) (api.GeneratorWorkflow, error)
	GetReviewBundle(context.Context, string, api.GetGeneratorReviewBundleParamsKind) (api.GeneratorReviewBundle, error)
}

type ConnectorConfig struct {
	PollInterval time.Duration
}

// Connector is a local MCP adapter over one remote Generator API and one
// disposable review projector. It owns no workflow state of its own.
type Connector struct {
	api          GeneratorAPI
	projector    *ReviewProjector
	pollInterval time.Duration
}

func NewConnector(client GeneratorAPI, projector *ReviewProjector, config ConnectorConfig) (*Connector, error) {
	if client == nil || projector == nil {
		return nil, errors.New("MCP connector requires Generator API client and review projector")
	}
	if config.PollInterval <= 0 {
		config.PollInterval = defaultPollInterval
	}
	return &Connector{api: client, projector: projector, pollInterval: config.PollInterval}, nil
}

// NewMCPServer registers the public Generator tools on a standard local stdio
// MCP Server. Tool descriptions document the user-confirmation boundary but
// never expose connector credentials or Server implementation details.
func (c *Connector) NewMCPServer() (*mcp.Server, error) {
	if c == nil || c.api == nil || c.projector == nil {
		return nil, errors.New("MCP connector is not configured")
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "breakfix-mcp", Version: "0.1.0"}, &mcp.ServerOptions{
		Instructions: "Use these tools to create and review Breakfix scenarios. Before tools that create, modify, submit, confirm, request changes, publish, or cancel, obtain the user's explicit current instruction in this conversation.",
	})
	c.registerTools(server)
	return server, nil
}

func (c *Connector) registerTools(server *mcp.Server) {
	registerTool(server, "set_generation_plan", "保存或修订结构化运维场景 Plan。首次保存不提供 session_id，后续修订提供上次返回的 session_id 和当前 revision。", false, func(ctx context.Context, input setGenerationPlanInput) (any, error) {
		return c.api.SetGenerationPlan(ctx, api.GeneratorPlanRequest{SessionId: input.SessionID, ExpectedRevision: input.ExpectedRevision, IdempotencyKey: input.IdempotencyKey, Plan: input.Plan})
	})
	registerTool(server, "confirm_generation", "仅在用户明确确认当前 Plan revision 后创建运维场景生成任务。", false, func(ctx context.Context, input confirmGenerationInput) (any, error) {
		return c.api.ConfirmGeneration(ctx, api.GeneratorGenerationConfirmationRequest{SessionId: input.SessionID, PlanRevision: input.PlanRevision, IdempotencyKey: input.IdempotencyKey})
	})
	registerTool(server, "list_active_generations", "列出当前用户尚未结束的生成任务。", true, func(ctx context.Context, _ emptyInput) (any, error) {
		return c.api.ListActiveGenerations(ctx)
	})
	registerTool(server, "get_generation", "读取一个生成任务的当前状态、candidate 和反馈；到达审核状态时自动同步本地只读审核目录。", true, func(ctx context.Context, input workflowInput) (any, error) {
		return c.generationResult(ctx, input.WorkflowID)
	})
	registerTool(server, "list_workspace_files", "列出指定 Generating 任务远程工作区中的文件。", true, func(ctx context.Context, input workspaceInput) (any, error) {
		return c.withWorkspaceTurn(ctx, input.WorkflowID, func(ctx context.Context, turnID string) (any, error) {
			result, err := c.api.ListWorkspaceFiles(ctx, input.WorkflowID, turnID)
			if err != nil {
				return nil, err
			}
			return workspaceFilesResult{WorkflowID: input.WorkflowID, Files: result.Files}, nil
		})
	})
	registerTool(server, "read_workspace_file", "读取指定 Generating 任务远程工作区中的一个相对路径文件。", true, func(ctx context.Context, input readWorkspaceFileInput) (any, error) {
		return c.withWorkspaceTurn(ctx, input.WorkflowID, func(ctx context.Context, turnID string) (any, error) {
			result, err := c.api.ReadWorkspaceFile(ctx, input.WorkflowID, turnID, input.Path, input.Offset, input.Limit)
			if err != nil {
				return nil, err
			}
			return workspaceFileResult{WorkflowID: input.WorkflowID, Path: result.Path, Content: result.Content}, nil
		})
	})
	registerTool(server, "write_workspace_file", "在指定 Generating 任务远程工作区写入一个相对路径文件。", false, func(ctx context.Context, input writeWorkspaceFileInput) (any, error) {
		return c.withWorkspaceTurn(ctx, input.WorkflowID, func(ctx context.Context, turnID string) (any, error) {
			err := c.api.WriteWorkspaceFile(ctx, input.WorkflowID, api.GeneratorWorkspaceFileWriteRequest{TurnId: turnID, Path: input.Path, Content: input.Content})
			if err != nil {
				return nil, err
			}
			return workspaceFileResult{WorkflowID: input.WorkflowID, Path: input.Path}, nil
		})
	})
	registerTool(server, "run_workspace_command", "在指定 Generating 任务远程工作区执行受平台限制的命令。", false, func(ctx context.Context, input runWorkspaceCommandInput) (any, error) {
		return c.withWorkspaceTurn(ctx, input.WorkflowID, func(ctx context.Context, turnID string) (any, error) {
			result, err := c.api.RunWorkspaceCommand(ctx, input.WorkflowID, api.GeneratorWorkspaceCommandRequest{TurnId: turnID, Command: input.Command})
			if err != nil {
				return nil, err
			}
			exitCode := 0
			if result.ExitCode != nil {
				exitCode = *result.ExitCode
			}
			output := ""
			if result.Output != nil {
				output = *result.Output
			}
			message := ""
			if result.Error != nil {
				message = *result.Error
			}
			return toolresult.WithData(toolresult.Status(result.Status), workspaceCommandResult{
				WorkflowID: input.WorkflowID, ExitCode: exitCode, Output: output,
			}, message)
		})
	})
	registerTool(server, "submit_candidate", "归档并提交指定 Generating 任务当前远程工作区。仅在用户要求提交当前 candidate 后调用。", false, func(ctx context.Context, input submitCandidateInput) (any, error) {
		return c.submitCandidate(ctx, input)
	})
	registerTool(server, "wait_generation", "有界等待一个生成任务状态变化；进入审核状态时自动同步本地只读审核目录。", true, func(ctx context.Context, input waitGenerationInput) (any, error) {
		return c.waitGeneration(ctx, input)
	})
	registerTool(server, "sync_review", "重新下载当前不可变审核快照到本机只读目录。", true, func(ctx context.Context, input workflowInput) (any, error) {
		return c.syncReview(ctx, input.WorkflowID)
	})
	registerTool(server, "confirm_content", "仅在用户明确确认当前内容审核快照后开始发布。", false, func(ctx context.Context, input contentConfirmationInput) (any, error) {
		return c.api.ConfirmContent(ctx, input.WorkflowID, api.GeneratorContentConfirmationRequest{CandidateRevisionId: input.CandidateRevisionID, IdempotencyKey: input.IdempotencyKey})
	})
	registerTool(server, "request_content_changes", "仅在用户明确要求修改当前内容审核 candidate 后返回远程工作区继续修复。", false, func(ctx context.Context, input contentChangeInput) (any, error) {
		return c.api.RequestContentChanges(ctx, input.WorkflowID, api.GeneratorContentChangeRequest{CandidateRevisionId: input.CandidateRevisionID, Feedback: input.Feedback, IdempotencyKey: input.IdempotencyKey})
	})
	registerTool(server, "cancel_generation", "仅在用户明确要求取消一个未结束的生成任务后调用。", false, func(ctx context.Context, input cancellationInput) (any, error) {
		return c.api.CancelGeneration(ctx, input.WorkflowID, api.GeneratorCancellationRequest{IdempotencyKey: input.IdempotencyKey})
	})
}

func registerTool[Input any](server *mcp.Server, name, description string, readOnly bool, handler func(context.Context, Input) (any, error)) {
	mcp.AddTool[Input, any](server, &mcp.Tool{
		Name: name, Description: description,
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: readOnly},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input Input) (*mcp.CallToolResult, any, error) {
		result, err := handler(ctx, input)
		if err != nil {
			return nil, toolResultFromError(err), nil
		}
		if envelope, ok := result.(toolresult.Envelope); ok {
			return nil, envelope, nil
		}
		envelope, err := toolresult.Success(result)
		if err != nil {
			return nil, nil, err
		}
		return nil, envelope, nil
	})
}

func toolResultFromError(err error) toolresult.Envelope {
	status := toolresult.StatusForError(err)
	return toolresult.WithRawData(status, nil, publicToolError(err).Error())
}

func (c *Connector) generationResult(ctx context.Context, workflowID string) (generationResult, error) {
	generation, err := c.api.GetGeneration(ctx, requireWorkflowID(workflowID))
	if err != nil {
		return generationResult{}, err
	}
	projection, err := c.syncReviewForWorkflow(ctx, generation.Workflow, generation.Candidate)
	if err != nil {
		return generationResult{}, err
	}
	return generationResult{Generation: generation, Review: projection}, nil
}

func (c *Connector) syncReview(ctx context.Context, workflowID string) (ReviewProjection, error) {
	result, err := c.generationResult(ctx, workflowID)
	if err != nil {
		return ReviewProjection{}, err
	}
	if result.Review == nil {
		return ReviewProjection{}, newPublicMCPError("generation is not waiting for review")
	}
	return *result.Review, nil
}

func (c *Connector) syncReviewForWorkflow(ctx context.Context, workflow api.GeneratorWorkflow, candidate *api.AuthoringCandidate) (*ReviewProjection, error) {
	var kind api.GetGeneratorReviewBundleParamsKind
	switch string(workflow.State) {
	case "NeedsAuthorReview":
		kind = api.GetGeneratorReviewBundleParamsKindContent
	default:
		return nil, nil
	}
	if candidate == nil || strings.TrimSpace(candidate.Id) == "" {
		return nil, errors.New("reviewing generation has no candidate revision")
	}
	bundle, err := c.api.GetReviewBundle(ctx, workflow.Id, kind)
	if err != nil {
		return nil, err
	}
	if bundle.Manifest.WorkflowId != workflow.Id || bundle.Manifest.CandidateRevisionId != candidate.Id {
		return nil, errors.New("review bundle does not match the current generation")
	}
	projection, err := c.projector.Project(bundle)
	if err != nil {
		return nil, err
	}
	return &projection, nil
}

func (c *Connector) withWorkspaceTurn(ctx context.Context, workflowID string, action func(context.Context, string) (any, error)) (result any, err error) {
	workflowID = requireWorkflowID(workflowID)
	turnID, err := newWorkspaceTurnID()
	if err != nil {
		return nil, err
	}
	if _, err := c.api.StartWorkspaceTurn(ctx, workflowID, api.GeneratorWorkspaceTurnRequest{TurnId: turnID}); err != nil {
		return nil, err
	}
	defer func() {
		releaseErr := c.endWorkspaceTurn(workflowID, turnID)
		if err == nil && releaseErr != nil {
			err = releaseErr
		}
	}()
	return action(ctx, turnID)
}

func (c *Connector) submitCandidate(ctx context.Context, input submitCandidateInput) (generationResult, error) {
	workflowID := requireWorkflowID(input.WorkflowID)
	if workflowID == "" || strings.TrimSpace(input.IdempotencyKey) == "" {
		return generationResult{}, newPublicMCPError("workflow_id and idempotency_key are required")
	}
	// The direct attempt first resolves an already committed idempotency receipt
	// after a lost HTTP response. A new submission is then bound to this short,
	// explicit workspace turn only when the Server reports a conflict.
	turnID := submissionTurnID(workflowID, input.IdempotencyKey)
	request := api.GeneratorCandidateSubmissionRequest{TurnId: turnID, IdempotencyKey: input.IdempotencyKey}
	generation, err := c.api.SubmitCandidate(ctx, workflowID, request)
	if err == nil {
		return generationResult{Generation: generation}, nil
	}
	if !isConflict(err) {
		return generationResult{}, err
	}
	if _, startErr := c.api.StartWorkspaceTurn(ctx, workflowID, api.GeneratorWorkspaceTurnRequest{TurnId: turnID}); startErr != nil {
		// A completed first attempt may have transitioned the workflow before its
		// response was lost. Resolve that durable receipt once more before giving
		// the caller the current conflict.
		if isConflict(startErr) {
			if generation, receiptErr := c.api.SubmitCandidate(ctx, workflowID, request); receiptErr == nil {
				return generationResult{Generation: generation}, nil
			}
		}
		return generationResult{}, startErr
	}
	defer func() { _ = c.endWorkspaceTurn(workflowID, turnID) }()
	generation, err = c.api.SubmitCandidate(ctx, workflowID, request)
	if err != nil {
		return generationResult{}, err
	}
	return generationResult{Generation: generation}, nil
}

func publicToolError(err error) error {
	var publicErr *publicMCPError
	if errors.As(err, &publicErr) {
		return errors.New(publicErr.message)
	}
	var httpErr *HTTPError
	if errors.As(err, &httpErr) {
		switch httpErr.StatusCode {
		case 400:
			return errors.New("the Breakfix server rejected the requested generation operation")
		case 401, 403:
			return errors.New("the Breakfix server rejected the authorization")
		case 404:
			return errors.New("the requested Breakfix resource was not found")
		case 409:
			return errors.New("the generation changed; read its current state before trying again")
		default:
			return errors.New("the Breakfix server could not complete the generation operation")
		}
	}
	return errors.New("the Breakfix server could not complete the generation operation")
}

type publicMCPError struct {
	message string
}

func (e *publicMCPError) Error() string {
	if e == nil {
		return ""
	}
	return e.message
}

func newPublicMCPError(message string) error {
	return &publicMCPError{message: message}
}

func (c *Connector) endWorkspaceTurn(workflowID, turnID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := c.api.EndWorkspaceTurn(ctx, workflowID, api.GeneratorWorkspaceTurnRequest{TurnId: turnID})
	if isConflict(err) {
		return nil
	}
	return err
}

func (c *Connector) waitGeneration(ctx context.Context, input waitGenerationInput) (generationWaitResult, error) {
	workflowID := requireWorkflowID(input.WorkflowID)
	timeout, err := waitTimeout(input.TimeoutSeconds)
	if err != nil {
		return generationWaitResult{}, err
	}
	initial, err := c.generationResult(ctx, workflowID)
	if err != nil {
		return generationWaitResult{}, err
	}
	if initial.Review != nil || terminalGenerationState(initial.Generation.Workflow.State) {
		return generationWaitResult{generationResult: initial}, nil
	}

	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(c.pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return generationWaitResult{}, ctx.Err()
		case <-deadline.C:
			return generationWaitResult{generationResult: initial, TimedOut: true}, nil
		case <-ticker.C:
			current, err := c.generationResult(ctx, workflowID)
			if err != nil {
				return generationWaitResult{}, err
			}
			if current.Review != nil || terminalGenerationState(current.Generation.Workflow.State) || current.Generation.Workflow.State != initial.Generation.Workflow.State {
				return generationWaitResult{generationResult: current}, nil
			}
			initial = current
		}
	}
}

func requireWorkflowID(value string) string {
	return strings.TrimSpace(value)
}

func terminalGenerationState(value api.GeneratorWorkflowState) bool {
	return value == "Published" || value == "Failed" || value == "Cancelled"
}

func waitTimeout(seconds int) (time.Duration, error) {
	if seconds == 0 {
		return defaultWaitTimeout, nil
	}
	if seconds < 1 || time.Duration(seconds)*time.Second > maxWaitTimeout {
		return 0, newPublicMCPError(fmt.Sprintf("timeout_seconds must be between 1 and %d", int(maxWaitTimeout/time.Second)))
	}
	return time.Duration(seconds) * time.Second, nil
}

func newWorkspaceTurnID() (string, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", fmt.Errorf("create workspace turn ID: %w", err)
	}
	return "mcp-turn-" + hex.EncodeToString(random[:]), nil
}

func submissionTurnID(workflowID, idempotencyKey string) string {
	// The key is opaque but persisted by the Server. Its deterministic digest is
	// only an internal turn fence and is never sent back to an MCP Host.
	return "mcp-submit-" + reviewPayloadDigest([]byte(workflowID + "\x1f" + idempotencyKey))[len("sha256:"):][:32]
}

func isConflict(err error) bool {
	var httpErr *HTTPError
	return errors.As(err, &httpErr) && httpErr.StatusCode == 409
}

type emptyInput struct{}

type workflowInput struct {
	WorkflowID string `json:"workflow_id"`
}

type workspaceInput struct {
	WorkflowID string `json:"workflow_id"`
}

type setGenerationPlanInput struct {
	SessionID        *string           `json:"session_id,omitempty"`
	ExpectedRevision int64             `json:"expected_revision"`
	IdempotencyKey   string            `json:"idempotency_key"`
	Plan             api.AuthoringPlan `json:"plan"`
}

type confirmGenerationInput struct {
	SessionID      string `json:"session_id"`
	PlanRevision   int64  `json:"plan_revision"`
	IdempotencyKey string `json:"idempotency_key"`
}

type readWorkspaceFileInput struct {
	WorkflowID string `json:"workflow_id"`
	Path       string `json:"path"`
	Offset     *int   `json:"offset,omitempty"`
	Limit      *int   `json:"limit,omitempty"`
}

type writeWorkspaceFileInput struct {
	WorkflowID string `json:"workflow_id"`
	Path       string `json:"path"`
	Content    string `json:"content"`
}

type runWorkspaceCommandInput struct {
	WorkflowID string `json:"workflow_id"`
	Command    string `json:"command"`
}

type submitCandidateInput struct {
	WorkflowID     string `json:"workflow_id"`
	IdempotencyKey string `json:"idempotency_key"`
}

type waitGenerationInput struct {
	WorkflowID     string `json:"workflow_id"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
}

type contentConfirmationInput struct {
	WorkflowID          string `json:"workflow_id"`
	CandidateRevisionID string `json:"candidate_revision_id"`
	IdempotencyKey      string `json:"idempotency_key"`
}

type contentChangeInput struct {
	WorkflowID          string `json:"workflow_id"`
	CandidateRevisionID string `json:"candidate_revision_id"`
	Feedback            string `json:"feedback"`
	IdempotencyKey      string `json:"idempotency_key"`
}

type cancellationInput struct {
	WorkflowID     string `json:"workflow_id"`
	IdempotencyKey string `json:"idempotency_key"`
}

type generationResult struct {
	Generation api.GeneratorGeneration `json:"generation"`
	Review     *ReviewProjection       `json:"review,omitempty"`
}

type generationWaitResult struct {
	generationResult
	TimedOut bool `json:"timed_out,omitempty"`
}

type workspaceFilesResult struct {
	WorkflowID string                       `json:"workflow_id"`
	Files      []api.GeneratorWorkspaceFile `json:"files"`
}

type workspaceFileResult struct {
	WorkflowID string `json:"workflow_id"`
	Path       string `json:"path"`
	Content    string `json:"content,omitempty"`
}

type workspaceCommandResult struct {
	WorkflowID string `json:"workflow_id"`
	ExitCode   int    `json:"exit_code"`
	Output     string `json:"output"`
}
