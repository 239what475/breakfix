package taxonomy

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sync"

	"github.com/breakfix/breakfix/internal/agentmodel"
	"github.com/breakfix/breakfix/internal/agentruntime"
	"github.com/breakfix/breakfix/internal/agentserver"
	"github.com/breakfix/breakfix/internal/agentworker"
	"github.com/breakfix/breakfix/internal/config"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

type LeaseCredential = agentruntime.LeaseCredential

type RuntimeClient interface {
	LoadContext(context.Context, agentruntime.Claim) (ExecutionContext, error)
	FinalizeMapper(context.Context, agentruntime.Claim, ChangeSet) error
	FinalizeReviewPair(context.Context, agentruntime.Claim, Review, Review) error
}

type InternalClient struct{ server *agentserver.Client }

func NewInternalClient(serverURL, apiKey string) (*InternalClient, error) {
	client, err := agentserver.New(serverURL, apiKey)
	if err != nil {
		return nil, err
	}
	return &InternalClient{server: client}, nil
}

func (c *InternalClient) LoadContext(ctx context.Context, claim agentruntime.Claim) (ExecutionContext, error) {
	var result ExecutionContext
	err := c.post(ctx, claim.Run.ID, "/taxonomy/context", claim.Credential(), &result)
	return result, err
}

func (c *InternalClient) FinalizeMapper(ctx context.Context, claim agentruntime.Claim, changes ChangeSet) error {
	return c.post(ctx, claim.Run.ID, "/taxonomy/mapper/finalize", struct {
		LeaseCredential
		ChangeSet ChangeSet `json:"changeset"`
	}{LeaseCredential: claim.Credential(), ChangeSet: changes}, nil)
}

func (c *InternalClient) FinalizeReviewPair(ctx context.Context, claim agentruntime.Claim, curriculum, sre Review) error {
	return c.post(ctx, claim.Run.ID, "/taxonomy/review/finalize", struct {
		LeaseCredential
		Curriculum Review `json:"curriculum"`
		SRE        Review `json:"sre"`
	}{
		LeaseCredential: claim.Credential(),
		Curriculum:      curriculum,
		SRE:             sre,
	}, nil)
}

func (c *InternalClient) post(ctx context.Context, runID, suffix string, body any, result any) error {
	if c == nil || c.server == nil {
		return errors.New("taxonomy internal client is not configured")
	}
	return c.server.Post(ctx, "/api/internal/agent-runs/"+url.PathEscape(runID)+suffix, body, result)
}

type WorkerExecutor struct {
	config config.AgentConfig
	client RuntimeClient
}

func NewWorkerExecutor(cfg config.AgentConfig, client RuntimeClient) (*WorkerExecutor, error) {
	if client == nil {
		return nil, errors.New("taxonomy worker executor requires a server client")
	}
	return &WorkerExecutor{config: cfg, client: client}, nil
}

func (e *WorkerExecutor) Execute(ctx context.Context, claim agentruntime.Claim, emit agentworker.Emitter) (agentworker.ExecutionResult, error) {
	result, err := e.execute(ctx, claim, emit)
	if err != nil {
		return agentworker.ExecutionResult{}, agentworker.Terminal(err)
	}
	return result, nil
}

func (e *WorkerExecutor) execute(ctx context.Context, claim agentruntime.Claim, _ agentworker.Emitter) (agentworker.ExecutionResult, error) {
	if !claim.Valid() {
		return agentworker.ExecutionResult{}, errors.New("invalid taxonomy agent run claim")
	}
	contextSnapshot, err := e.client.LoadContext(ctx, claim)
	if err != nil {
		return agentworker.ExecutionResult{}, fmt.Errorf("load taxonomy context: %w", err)
	}
	input, err := DecodeRunInput(claim.Run.Input)
	if err != nil {
		return agentworker.ExecutionResult{}, fmt.Errorf("decode taxonomy run input: %w", err)
	}
	if input.Stage != contextSnapshot.Stage || input.WorkID != contextSnapshot.WorkID {
		return agentworker.ExecutionResult{}, errors.New("taxonomy context does not match the claimed run")
	}
	switch input.Stage {
	case WorkStageMapper:
		changes, err := runMapperWithEino(ctx, e.config, contextSnapshot)
		if err != nil {
			return agentworker.ExecutionResult{}, err
		}
		if err := e.client.FinalizeMapper(ctx, claim, changes); err != nil {
			return agentworker.ExecutionResult{}, fmt.Errorf("finalize taxonomy mapper: %w", err)
		}
	case WorkStageReview:
		curriculum, sre, err := runReviewPairWithEino(ctx, e.config, contextSnapshot)
		if err != nil {
			return agentworker.ExecutionResult{}, err
		}
		if err := e.client.FinalizeReviewPair(ctx, claim, curriculum, sre); err != nil {
			return agentworker.ExecutionResult{}, fmt.Errorf("finalize taxonomy review pair: %w", err)
		}
	default:
		return agentworker.ExecutionResult{}, fmt.Errorf("unsupported taxonomy stage %q", input.Stage)
	}
	return agentworker.ExecutionResult{Finalized: true}, nil
}

type mapperResult struct {
	Skills            []SkillChange            `json:"skills" jsonschema:"required"`
	Tags              []TagChange              `json:"tags" jsonschema:"required"`
	ChallengeMappings []ChallengeMappingChange `json:"challenge_mappings" jsonschema:"required"`
	SkillMappings     []SkillMappingChange     `json:"skill_mappings" jsonschema:"required"`
}

func (r mapperResult) ChangeSet() ChangeSet {
	return ChangeSet(r)
}

type reviewerResult struct {
	Decision ReviewDecision `json:"decision" jsonschema:"required,enum=approve,enum=reject"`
	Feedback string         `json:"feedback" jsonschema:"required"`
}

func (r reviewerResult) Review() Review {
	return Review(r)
}

func runMapperWithEino(ctx context.Context, cfg config.AgentConfig, input ExecutionContext) (ChangeSet, error) {
	result, err := invokeTypedResult[mapperResult](ctx, cfg, "taxonomy_mapper", input.SystemPrompt, input.Prompt, "submit_changeset", "提交完整的 taxonomy ChangeSet。", func(value mapperResult) error {
		if value.ChallengeMappings == nil || value.Skills == nil || value.Tags == nil || value.SkillMappings == nil {
			return errors.New("changeset must explicitly contain all four arrays")
		}
		if value.ChangeSet().Empty() {
			return errors.New("changeset must not be empty")
		}
		return nil
	})
	if err != nil {
		return ChangeSet{}, fmt.Errorf("run taxonomy mapper: %w", err)
	}
	return result.ChangeSet(), nil
}

func runReviewPairWithEino(ctx context.Context, cfg config.AgentConfig, input ExecutionContext) (Review, Review, error) {
	type outcome struct {
		review Review
		err    error
	}
	var curriculum, sre outcome
	var wait sync.WaitGroup
	wait.Add(2)
	go func() {
		defer wait.Done()
		value, err := invokeTypedResult[reviewerResult](ctx, cfg, "taxonomy_curriculum_reviewer", input.SystemPrompt+"\n\n当前只执行 Curriculum 视角。", input.Prompt, "submit_curriculum_review", "提交 Curriculum Reviewer 的审查结论。", validateReviewerResult)
		if err != nil {
			curriculum.err = err
			return
		}
		curriculum.review = value.Review()
	}()
	go func() {
		defer wait.Done()
		value, err := invokeTypedResult[reviewerResult](ctx, cfg, "taxonomy_sre_reviewer", input.SystemPrompt+"\n\n当前只执行 SRE 视角。", input.Prompt, "submit_sre_review", "提交 SRE Reviewer 的审查结论。", validateReviewerResult)
		if err != nil {
			sre.err = err
			return
		}
		sre.review = value.Review()
	}()
	wait.Wait()
	if curriculum.err != nil {
		return Review{}, Review{}, fmt.Errorf("run curriculum reviewer: %w", curriculum.err)
	}
	if sre.err != nil {
		return Review{}, Review{}, fmt.Errorf("run SRE reviewer: %w", sre.err)
	}
	return curriculum.review, sre.review, nil
}

func validateReviewerResult(value reviewerResult) error {
	return ValidateReview(value.Review())
}

func invokeTypedResult[T any](ctx context.Context, cfg config.AgentConfig, name, instruction, prompt, toolName, toolDescription string, validate func(T) error) (T, error) {
	var zero T
	chat, err := agentmodel.NewChatModel(ctx, cfg)
	if err != nil {
		return zero, err
	}
	resultTool, err := agentmodel.NewResultTool[T](toolName, toolDescription, validate)
	if err != nil {
		return zero, err
	}
	agent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:          name,
		Description:   "Breakfix taxonomy committee agent",
		Instruction:   instruction,
		Model:         chat,
		MaxIterations: 8,
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{Tools: []tool.BaseTool{resultTool}},
			ReturnDirectly:  map[string]bool{toolName: true},
		},
		ModelRetryConfig: &adk.ModelRetryConfig{
			MaxRetries: 3,
			IsRetryAble: func(_ context.Context, err error) bool {
				return agentmodel.IsTransientTransportError(err)
			},
		},
	})
	if err != nil {
		return zero, fmt.Errorf("create taxonomy Eino agent: %w", err)
	}
	runner := adk.NewRunner(ctx, adk.RunnerConfig{Agent: agent})
	events := runner.Run(ctx, []adk.Message{schema.UserMessage(prompt)})
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
		return zero, errors.New("taxonomy agent did not submit its typed result")
	}
	return value, nil
}

// Ensure ResultTool remains assignable to Eino's BaseTool when dependency
// versions change. The value is never used at runtime.
var _ tool.BaseTool = (*agentmodel.ResultTool[mapperResult])(nil)
