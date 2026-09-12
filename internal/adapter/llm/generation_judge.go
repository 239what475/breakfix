package llm

import (
	"context"
	"errors"
	"fmt"
	"strings"

	app "github.com/breakfix/breakfix/internal/application/generation"
	"github.com/breakfix/breakfix/internal/bootstrap/config"
	"github.com/breakfix/breakfix/internal/domain/authoring"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

// Judge is the Server-internal review role for one submitted candidate. It
// has no workspace capability and cannot generate or mutate candidate files.
type Judge struct {
	config config.AgentConfig
}

func NewJudge(cfg config.AgentConfig) *Judge {
	return &Judge{config: cfg}
}

func (e *Judge) Judge(ctx context.Context, plan authoring.Plan, candidate *app.Candidate) (app.Judgement, error) {
	return judgeCandidate(ctx, e.config, plan, candidate)
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
		Name:          "generation_judge",
		Description:   "Breakfix scenario judge",
		Instruction:   generationJudgeSystemPrompt(),
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
		return app.Judgement{}, fmt.Errorf("create generation judge: %w", err)
	}
	runner := adk.NewRunner(ctx, adk.RunnerConfig{Agent: agent})
	events := runner.Run(ctx, []adk.Message{schema.UserMessage(generationJudgePrompt(plan, candidate))})
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
		return app.Judgement{}, errors.New("generation judge did not submit its typed result")
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
