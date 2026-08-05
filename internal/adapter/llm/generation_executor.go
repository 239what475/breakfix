package llm

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"

	app "github.com/breakfix/breakfix/internal/application/generation"
	"github.com/breakfix/breakfix/internal/bootstrap/config"
	"github.com/breakfix/breakfix/internal/domain/authoring"
	"github.com/breakfix/breakfix/internal/domain/generation"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/prebuilt/deep"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

const generatorMaxIterations = 40

// GeneratorExecutor is the Server-side Eino implementation of the Generator
// role. Its only mutable capability is the workflow-fenced workspace runtime.
type GeneratorExecutor struct {
	config config.AgentConfig
	client GeneratorWorkspaceRuntime
}

// GeneratorWorkspaceRuntime is the narrow Server-owned workspace surface used
// by Generator tools. The model never receives Sandbox, PVC, or provider
// credentials; every call remains fenced by the enclosing workflow claim.
type GeneratorWorkspaceRuntime interface {
	LoadWorkspace(context.Context, generation.Claim) (app.WorkspaceContext, error)
	ReadFile(context.Context, generation.Claim, string, int, int) (app.FileReadResponse, error)
	WriteFile(context.Context, generation.Claim, string, string) error
	Execute(context.Context, generation.Claim, string, func(app.ExecuteEvent) error) error
	ArchiveWorkspace(context.Context, generation.Claim) (app.ArchiveResponse, error)
}

func NewGeneratorExecutor(cfg config.AgentConfig, client GeneratorWorkspaceRuntime) (*GeneratorExecutor, error) {
	if client == nil {
		return nil, errors.New("generator executor requires a Server runtime client")
	}
	return &GeneratorExecutor{config: cfg, client: client}, nil
}

// Generate executes exactly the Generator phase. It intentionally does not
// inspect, judge, persist, or report the archive; those boundaries belong to
// the owning Server application service.
func (e *GeneratorExecutor) Generate(ctx context.Context, execution generation.Execution) ([]byte, error) {
	if !execution.Valid() || execution.Claim.Workflow.State != generation.StateGenerating {
		return nil, errors.New("generator requires a Generating workflow")
	}
	workspace, err := e.client.LoadWorkspace(ctx, execution.Claim)
	if err != nil {
		return nil, fmt.Errorf("load generator workspace: %w", err)
	}
	if err := workspace.Plan.ValidateForGeneration(); err != nil {
		return nil, fmt.Errorf("validate generator plan: %w", err)
	}
	if !reflect.DeepEqual(workspace.Plan, execution.Context.Plan) {
		return nil, errors.New("generator workspace plan differs from workflow context")
	}
	if !sameFeedback(workspace.Feedback, execution.Context.Feedback) {
		return nil, errors.New("generator workspace feedback differs from workflow context")
	}
	backend, err := NewOpenSandboxBackend(execution.Claim, e.client)
	if err != nil {
		return nil, err
	}
	if err := runDeepAgent(ctx, e.config, backend, workspace.Plan, workspace.Feedback); err != nil {
		return nil, err
	}
	archive, err := e.client.ArchiveWorkspace(ctx, execution.Claim)
	if err != nil {
		return nil, fmt.Errorf("archive generator workspace: %w", err)
	}
	if _, err := app.InspectCandidateArchive(archive.Archive); err != nil {
		return nil, generation.NewArtifactError("CANDIDATE_INVALID", err.Error())
	}
	return archive.Archive, nil
}

func (e *GeneratorExecutor) Judge(ctx context.Context, plan authoring.Plan, candidate *app.Candidate) (app.Judgement, error) {
	return judgeCandidate(ctx, e.config, plan, candidate)
}

func sameFeedback(left, right generation.Feedback) bool {
	if left.Summary != right.Summary || len(left.Issues) != len(right.Issues) {
		return false
	}
	for index := range left.Issues {
		if left.Issues[index] != right.Issues[index] {
			return false
		}
	}
	return true
}

func runDeepAgent(ctx context.Context, cfg config.AgentConfig, backend *OpenSandboxBackend, plan authoring.Plan, feedback generation.Feedback) error {
	chat, err := NewChatModel(ctx, cfg)
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
				return IsTransientTransportError(err)
			},
		},
	})
	if err != nil {
		return fmt.Errorf("create generator DeepAgent: %w", err)
	}
	runner := adk.NewRunner(ctx, adk.RunnerConfig{Agent: agent})
	events := runner.Run(ctx, []adk.Message{schema.UserMessage(generatorTurnPrompt(plan, feedback))})
	for {
		event, ok := events.Next()
		if !ok {
			break
		}
		if event.Err != nil {
			return event.Err
		}
		if event.Action != nil && event.Action.Exit {
			break
		}
	}
	return nil
}

type judgementDecision string

const (
	judgementPass   judgementDecision = "pass"
	judgementReject judgementDecision = "reject"
)

type judgementResult struct {
	Decision judgementDecision `json:"decision" jsonschema:"required,enum=pass,enum=reject"`
	Feedback string            `json:"feedback" jsonschema:"required"`
}

func judgeCandidate(ctx context.Context, cfg config.AgentConfig, plan authoring.Plan, candidate *app.Candidate) (app.Judgement, error) {
	if candidate == nil {
		return app.Judgement{}, errors.New("judge candidate is required")
	}
	chat, err := NewChatModel(ctx, cfg)
	if err != nil {
		return app.Judgement{}, err
	}
	resultTool, err := NewResultTool[judgementResult]("submit_judgement", "提交题目审核结论。", validateJudgement)
	if err != nil {
		return app.Judgement{}, err
	}
	agent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:          "generator_judge",
		Description:   "Breakfix challenge judge",
		Instruction:   generatorJudgeSystemPrompt(),
		Model:         chat,
		MaxIterations: 8,
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{Tools: []tool.BaseTool{resultTool}},
		},
		ModelRetryConfig: &adk.ModelRetryConfig{
			MaxRetries: 3,
			IsRetryAble: func(_ context.Context, err error) bool {
				return IsTransientTransportError(err)
			},
		},
	})
	if err != nil {
		return app.Judgement{}, fmt.Errorf("create generator judge: %w", err)
	}
	runner := adk.NewRunner(ctx, adk.RunnerConfig{Agent: agent})
	events := runner.Run(ctx, []adk.Message{schema.UserMessage(generatorJudgePrompt(plan, candidate))})
	for {
		event, ok := events.Next()
		if !ok {
			break
		}
		if event.Err != nil {
			return app.Judgement{}, event.Err
		}
		if event.Action != nil && event.Action.Exit {
			break
		}
	}
	value, called := resultTool.Value()
	if !called {
		return app.Judgement{}, errors.New("generator judge did not submit its typed result")
	}
	result := app.Judgement{Approved: value.Decision == judgementPass, Feedback: strings.TrimSpace(value.Feedback)}
	if err := result.Validate(); err != nil {
		return app.Judgement{}, err
	}
	return result, nil
}

func validateJudgement(value judgementResult) error {
	result := app.Judgement{Approved: value.Decision == judgementPass, Feedback: strings.TrimSpace(value.Feedback)}
	if value.Decision != judgementPass && value.Decision != judgementReject {
		return errors.New("judge decision must be pass or reject")
	}
	return result.Validate()
}
