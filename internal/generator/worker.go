package generator

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/breakfix/breakfix/internal/agentmodel"
	"github.com/breakfix/breakfix/internal/agentruntime"
	"github.com/breakfix/breakfix/internal/agentworker"
	"github.com/breakfix/breakfix/internal/authoring"
	"github.com/breakfix/breakfix/internal/config"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/prebuilt/deep"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

const generatorMaxIterations = 40

// WorkerExecutor owns the model-side half of challenge generation. It only
// accesses generic Agent Runtime data directly; all workspace, artifact, and
// VerifyTask mutations use the Server's fenced internal API.
type WorkerExecutor struct {
	config config.AgentConfig
	client RuntimeClient
}

func NewWorkerExecutor(cfg config.AgentConfig, client RuntimeClient) (*WorkerExecutor, error) {
	if client == nil {
		return nil, errors.New("generator worker executor requires a server client")
	}
	return &WorkerExecutor{config: cfg, client: client}, nil
}

func (e *WorkerExecutor) Execute(parent context.Context, claim agentruntime.Claim, emit agentworker.Emitter) (agentworker.ExecutionResult, error) {
	if !claim.Valid() || claim.Run.Purpose != RuntimePurpose {
		return agentworker.ExecutionResult{}, errors.New("invalid generator agent run claim")
	}
	if !claim.Run.DeadlineAt.After(time.Now().UTC()) {
		return agentworker.ExecutionResult{}, context.DeadlineExceeded
	}
	ctx, cancel := context.WithDeadline(parent, claim.Run.DeadlineAt)
	defer cancel()

	input, err := DecodeRunInput(claim.Run.Input)
	if err != nil {
		return agentworker.ExecutionResult{}, fmt.Errorf("decode generator run input: %w", err)
	}
	workspace, err := e.client.LoadWorkspace(ctx, claim)
	if err != nil {
		return agentworker.ExecutionResult{}, fmt.Errorf("load generator workspace context: %w", err)
	}
	if err := workspace.Plan.ValidateForGeneration(); err != nil {
		return agentworker.ExecutionResult{}, fmt.Errorf("validate generator plan: %w", err)
	}
	if strings.TrimSpace(workspace.BaseImage) == "" {
		return agentworker.ExecutionResult{}, errors.New("generator workspace base image is required")
	}
	feedback := workspace.Feedback
	if !sameFeedback(input.Feedback, workspace.Feedback) {
		return agentworker.ExecutionResult{}, errors.New("generator workspace feedback does not match immutable run input")
	}
	backend, err := NewOpenSandboxBackend(claim, e.client)
	if err != nil {
		return agentworker.ExecutionResult{}, err
	}

	for {
		if err := ctx.Err(); err != nil {
			return agentworker.ExecutionResult{}, err
		}
		if err := runDeepAgent(ctx, e.config, backend, workspace.Plan, workspace.BaseImage, feedback, emit); err != nil {
			return agentworker.ExecutionResult{}, err
		}
		archive, err := e.client.ArchiveWorkspace(ctx, claim)
		if err != nil {
			return agentworker.ExecutionResult{}, fmt.Errorf("archive generator workspace: %w", err)
		}
		candidate, err := InspectCandidateArchive(archive.Archive)
		if err != nil {
			feedback = validationFeedback(err)
			continue
		}
		judgement, err := judgeCandidate(ctx, e.config, workspace.Plan, candidate)
		if err != nil {
			// Typed-result and model transport failures are technical failures.
			// They end this attempt so the durable retry policy owns re-execution.
			return agentworker.ExecutionResult{}, err
		}
		if judgement.Decision == judgementReject {
			feedback = Feedback{
				Summary: "题目审核未通过，请根据具体意见修复。",
				Issues:  []Issue{{Code: "JUDGE_REJECT", Message: judgement.Feedback}},
			}
			continue
		}
		if _, err := e.client.SubmitCandidate(ctx, claim, candidate.Archive); err != nil {
			return agentworker.ExecutionResult{}, fmt.Errorf("submit generator candidate: %w", err)
		}
		return agentworker.ExecutionResult{Finalized: true}, nil
	}
}

func sameFeedback(left, right Feedback) bool {
	if left.BuildPassed != right.BuildPassed || left.AnswerPassed != right.AnswerPassed ||
		left.CheckpointsPassed != right.CheckpointsPassed || left.Summary != right.Summary || len(left.Issues) != len(right.Issues) {
		return false
	}
	for index := range left.Issues {
		if left.Issues[index] != right.Issues[index] {
			return false
		}
	}
	return true
}

func runDeepAgent(ctx context.Context, cfg config.AgentConfig, backend *OpenSandboxBackend, plan authoring.Plan, baseImage string, feedback Feedback, emit agentworker.Emitter) error {
	chat, err := agentmodel.NewChatModel(ctx, cfg)
	if err != nil {
		return err
	}
	agent, err := deep.New(ctx, &deep.Config{
		Name:                   "generator",
		Description:            "Breakfix challenge implementation agent",
		ChatModel:              chat,
		Instruction:            generatorSystemPrompt(),
		MaxIteration:           generatorMaxIterations,
		Backend:                backend,
		StreamingShell:         backend,
		WithoutWriteTodos:      true,
		WithoutGeneralSubAgent: true,
		ModelRetryConfig: &adk.ModelRetryConfig{
			MaxRetries: 3,
			IsRetryAble: func(_ context.Context, err error) bool {
				return agentmodel.IsTransientTransportError(err)
			},
		},
	})
	if err != nil {
		return fmt.Errorf("create generator DeepAgent: %w", err)
	}
	runner := adk.NewRunner(ctx, adk.RunnerConfig{Agent: agent})
	events := runner.Run(ctx, []adk.Message{schema.UserMessage(generatorTurnPrompt(plan, baseImage, feedback))})
	exited := false
	for {
		event, ok := events.Next()
		if !ok {
			break
		}
		if event.Err != nil {
			return event.Err
		}
		if event.Output != nil && event.Output.MessageOutput != nil && event.Output.MessageOutput.Message != nil {
			for _, call := range event.Output.MessageOutput.Message.ToolCalls {
				emit.EmitTool(ctx, call.Function.Name)
			}
		}
		if event.Action != nil && event.Action.Exit {
			exited = true
			break
		}
	}
	if !exited {
		return errors.New("generator DeepAgent ended without a normal exit")
	}
	return nil
}

type judgementDecision string

const (
	judgementPass   judgementDecision = "pass"
	judgementReject judgementDecision = "reject"
)

type judgement struct {
	Decision judgementDecision `json:"decision" jsonschema:"required,enum=pass,enum=reject"`
	Feedback string            `json:"feedback" jsonschema:"required"`
}

func judgeCandidate(ctx context.Context, cfg config.AgentConfig, plan authoring.Plan, candidate *Candidate) (judgement, error) {
	var zero judgement
	if candidate == nil {
		return zero, errors.New("judge candidate is required")
	}
	chat, err := agentmodel.NewChatModel(ctx, cfg)
	if err != nil {
		return zero, err
	}
	resultTool, err := agentmodel.NewResultTool[judgement]("submit_judgement", "提交题目审核结论。", validateJudgement)
	if err != nil {
		return zero, err
	}
	agent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:          "generator_judge",
		Description:   "Breakfix challenge judge",
		Instruction:   generatorJudgeSystemPrompt(),
		Model:         chat,
		MaxIterations: 8,
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{Tools: []tool.BaseTool{resultTool}},
			ReturnDirectly:  map[string]bool{"submit_judgement": true},
		},
		ModelRetryConfig: &adk.ModelRetryConfig{
			MaxRetries: 3,
			IsRetryAble: func(_ context.Context, err error) bool {
				return agentmodel.IsTransientTransportError(err)
			},
		},
	})
	if err != nil {
		return zero, fmt.Errorf("create generator judge: %w", err)
	}
	runner := adk.NewRunner(ctx, adk.RunnerConfig{Agent: agent})
	events := runner.Run(ctx, []adk.Message{schema.UserMessage(generatorJudgePrompt(plan, candidate))})
	for {
		event, ok := events.Next()
		if !ok {
			break
		}
		if event.Err != nil {
			return zero, event.Err
		}
		if event.Action != nil && event.Action.Exit {
			break
		}
	}
	value, called := resultTool.Value()
	if !called {
		return zero, errors.New("generator judge did not submit its typed result")
	}
	return value, nil
}

func validateJudgement(value judgement) error {
	switch value.Decision {
	case judgementPass:
		if value.Feedback != "" {
			return errors.New("judge pass feedback must be empty")
		}
	case judgementReject:
		if strings.TrimSpace(value.Feedback) == "" {
			return errors.New("judge reject feedback must be non-empty")
		}
	default:
		return errors.New("judge decision must be pass or reject")
	}
	return nil
}

func validationFeedback(err error) Feedback {
	return Feedback{
		Summary: "候选未通过确定性结构或语义校验。",
		Issues:  []Issue{{Code: "CANDIDATE_INVALID", Message: err.Error()}},
	}
}
