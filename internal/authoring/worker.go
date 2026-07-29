package authoring

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"

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

type LeaseCredential struct {
	Attempt    int    `json:"attempt"`
	LeaseOwner string `json:"lease_owner"`
}

type ExecutionContext struct {
	Stage Stage `json:"stage"`
}

type RuntimeClient interface {
	LoadContext(context.Context, agentruntime.Claim) (ExecutionContext, error)
	UpdateStage(context.Context, agentruntime.Claim, int64, Plan, Change) (Stage, error)
	Finalize(context.Context, agentruntime.Claim, string) error
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
	err := c.post(ctx, claim.Run.ID, "/authoring/context", LeaseCredential{Attempt: claim.Attempt, LeaseOwner: claim.LeaseOwner}, &result)
	return result, err
}

func (c *InternalClient) UpdateStage(ctx context.Context, claim agentruntime.Claim, revision int64, plan Plan, change Change) (Stage, error) {
	var result Stage
	err := c.post(ctx, claim.Run.ID, "/authoring/stage", struct {
		LeaseCredential
		StageRevision int64  `json:"stage_revision"`
		Plan          Plan   `json:"plan"`
		Change        Change `json:"change"`
	}{LeaseCredential{Attempt: claim.Attempt, LeaseOwner: claim.LeaseOwner}, revision, plan, change}, &result)
	return result, err
}

func (c *InternalClient) Finalize(ctx context.Context, claim agentruntime.Claim, content string) error {
	return c.post(ctx, claim.Run.ID, "/authoring/finalize", struct {
		LeaseCredential
		Content string `json:"content"`
	}{LeaseCredential{Attempt: claim.Attempt, LeaseOwner: claim.LeaseOwner}, content}, nil)
}

func (c *InternalClient) post(ctx context.Context, runID, suffix string, body any, result any) error {
	if c == nil || c.server == nil {
		return errors.New("authoring internal client is not configured")
	}
	return c.server.Post(ctx, "/api/internal/agent-runs/"+url.PathEscape(runID)+suffix, body, result)
}

type WorkerExecutor struct {
	repo   agentruntime.Repository
	config config.AgentConfig
	client RuntimeClient
}

func NewWorkerExecutor(repo agentruntime.Repository, cfg config.AgentConfig, client RuntimeClient) (*WorkerExecutor, error) {
	if repo == nil || client == nil {
		return nil, errors.New("authoring worker executor requires runtime repository and server client")
	}
	return &WorkerExecutor{repo: repo, config: cfg, client: client}, nil
}

func (e *WorkerExecutor) Execute(ctx context.Context, claim agentruntime.Claim, _ agentworker.Emitter) (agentworker.ExecutionResult, error) {
	if !claim.Valid() || claim.Run.Purpose != "authoring" {
		return agentworker.ExecutionResult{}, errors.New("invalid authoring agent run claim")
	}
	contextSnapshot, err := e.client.LoadContext(ctx, claim)
	if err != nil {
		return agentworker.ExecutionResult{}, fmt.Errorf("load authoring context: %w", err)
	}
	history, err := e.repo.ListMessages(ctx, claim.Run.SessionID)
	if err != nil {
		return agentworker.ExecutionResult{}, fmt.Errorf("load authoring history: %w", err)
	}
	response, err := RunWithEino(ctx, e.config, claim, contextSnapshot.Stage, history, e.client)
	if err != nil {
		return agentworker.ExecutionResult{}, err
	}
	if err := e.client.Finalize(ctx, claim, response); err != nil {
		return agentworker.ExecutionResult{}, fmt.Errorf("finalize authoring run: %w", err)
	}
	return agentworker.ExecutionResult{Finalized: true}, nil
}

func RunWithEino(ctx context.Context, cfg config.AgentConfig, claim agentruntime.Claim, stage Stage, history []agentruntime.Message, client RuntimeClient) (string, error) {
	if !claim.Valid() || client == nil {
		return "", errors.New("authoring execution requires claim and server client")
	}
	if len(history) == 0 || history[len(history)-1].Role != "user" {
		return "", errors.New("authoring execution requires a latest user message")
	}
	chat, err := agentmodel.NewChatModel(ctx, cfg)
	if err != nil {
		return "", err
	}
	conversation := &runtimeConversation{claim: claim, client: client, stage: stage}
	inputs, err := authoringInputs(conversation, history)
	if err != nil {
		return "", err
	}
	agent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:          "authoring_agent",
		Description:   "Breakfix challenge authoring agent",
		Instruction:   authoringSystemPrompt(),
		Model:         chat,
		MaxIterations: 18,
		ToolsConfig: adk.ToolsConfig{ToolsNodeConfig: compose.ToolsNodeConfig{
			Tools:               toBaseAuthoringTools(conversation.tools()),
			ExecuteSequentially: true,
		}},
		ModelRetryConfig: &adk.ModelRetryConfig{
			MaxRetries: 3,
			IsRetryAble: func(_ context.Context, err error) bool {
				return agentmodel.IsTransientTransportError(err)
			},
		},
	})
	if err != nil {
		return "", fmt.Errorf("create Eino authoring agent: %w", err)
	}
	runner := adk.NewRunner(ctx, adk.RunnerConfig{Agent: agent})
	events := runner.Run(ctx, inputs)
	var response string
	for {
		event, ok := events.Next()
		if !ok {
			break
		}
		if event.Err != nil {
			return "", event.Err
		}
		if event.Output != nil && event.Output.MessageOutput != nil && event.Output.MessageOutput.Message != nil {
			if content := strings.TrimSpace(event.Output.MessageOutput.Message.Content); content != "" {
				response = content
			}
		}
		if event.Action != nil && event.Action.Exit {
			break
		}
	}
	if strings.TrimSpace(response) == "" {
		return "", errors.New("authoring agent returned an empty response")
	}
	return response, nil
}

func authoringInputs(conversation *runtimeConversation, history []agentruntime.Message) ([]adk.Message, error) {
	inputs := make([]adk.Message, 0, len(history))
	for index, message := range history {
		switch message.Role {
		case "user":
			content := message.Content
			if index == len(history)-1 {
				var err error
				content, err = conversation.prompt(content)
				if err != nil {
					return nil, err
				}
			}
			inputs = append(inputs, schema.UserMessage(content))
		case "assistant":
			inputs = append(inputs, schema.AssistantMessage(message.Content, nil))
		default:
			return nil, fmt.Errorf("unsupported authoring history role %q", message.Role)
		}
	}
	return inputs, nil
}

func toBaseAuthoringTools(values []tool.InvokableTool) []tool.BaseTool {
	result := make([]tool.BaseTool, 0, len(values))
	for _, value := range values {
		result = append(result, value)
	}
	return result
}

type runtimeConversation struct {
	claim  agentruntime.Claim
	client RuntimeClient
	stage  Stage
}

func (c *runtimeConversation) prompt(userMessage string) (string, error) {
	plan, err := json.MarshalIndent(c.stage.Plan, "", "  ")
	if err != nil {
		return "", err
	}
	return fmt.Sprintf(`当前私有题意约定如下：

%s

作者本次消息：
%s`, string(plan), userMessage), nil
}

func (c *runtimeConversation) tools() []tool.InvokableTool {
	return []tool.InvokableTool{
		&authoringTool{name: "set_metadata", desc: "更新题目标题、简介、难度和运行时。", params: map[string]*schema.ParameterInfo{
			"title": {Type: schema.String, Desc: "题目标题", Required: true}, "description": {Type: schema.String, Desc: "题目简介", Required: true},
			"difficulty": {Type: schema.String, Enum: []string{"easy", "medium", "hard"}, Required: true},
			"runtime":    {Type: schema.String, Enum: []string{"container", "vcluster"}, Required: true},
			"reason":     {Type: schema.String, Desc: "修改理由", Required: true}, "difficulty_impact": {Type: schema.String, Desc: "难度影响", Required: true},
		}, run: c.setMetadata},
		&authoringTool{name: "replace_overview", desc: "替换题意约定概览。", params: map[string]*schema.ParameterInfo{
			"markdown": {Type: schema.String, Desc: "完整概览 Markdown", Required: true},
			"reason":   {Type: schema.String, Desc: "修改理由", Required: true}, "difficulty_impact": {Type: schema.String, Desc: "难度影响", Required: true},
		}, run: c.replaceOverview},
		&authoringTool{name: "upsert_checkpoint", desc: "新增或修改一个公开检查点。", params: map[string]*schema.ParameterInfo{
			"id":    {Type: schema.String, Desc: "已有检查点 id", Required: false},
			"title": {Type: schema.String, Desc: "检查点标题", Required: true}, "markdown": {Type: schema.String, Desc: "检查点说明 Markdown", Required: true}, "position": {Type: schema.Integer, Desc: "从 1 开始的展示顺序", Required: true},
			"reason": {Type: schema.String, Desc: "修改理由", Required: true}, "difficulty_impact": {Type: schema.String, Desc: "难度影响", Required: true},
		}, run: c.upsertCheckpoint},
		&authoringTool{name: "remove_checkpoint", desc: "移除不再需要的检查点。", params: map[string]*schema.ParameterInfo{
			"id":     {Type: schema.String, Desc: "检查点 id", Required: true},
			"reason": {Type: schema.String, Desc: "移除理由", Required: true}, "difficulty_impact": {Type: schema.String, Desc: "难度影响", Required: true},
		}, run: c.removeCheckpoint},
		&authoringTool{name: "reorder_checkpoints", desc: "重新排序全部检查点。", params: map[string]*schema.ParameterInfo{
			"ids":    {Type: schema.Array, ElemInfo: &schema.ParameterInfo{Type: schema.String}, Required: true},
			"reason": {Type: schema.String, Desc: "排序理由", Required: true}, "difficulty_impact": {Type: schema.String, Desc: "难度影响", Required: true},
		}, run: c.reorderCheckpoints},
	}
}

func (c *runtimeConversation) apply(ctx context.Context, kind, summary, difficultyImpact string, mutate func(*Plan) error) (string, error) {
	if strings.TrimSpace(summary) == "" || strings.TrimSpace(difficultyImpact) == "" {
		return "", errors.New("修改理由和难度影响不能为空")
	}
	plan := c.stage.Plan.Clone()
	if err := mutate(&plan); err != nil {
		return "", err
	}
	stage, err := c.client.UpdateStage(ctx, c.claim, c.stage.StageRevision, plan, Change{Kind: kind, Summary: strings.TrimSpace(summary), DifficultyImpact: strings.TrimSpace(difficultyImpact)})
	if err != nil {
		return "", err
	}
	c.stage = stage
	return fmt.Sprintf(`{"change":%q}`, summary), nil
}

func (c *runtimeConversation) setMetadata(ctx context.Context, raw string) (string, error) {
	var args struct {
		Title            string `json:"title"`
		Description      string `json:"description"`
		Difficulty       string `json:"difficulty"`
		Runtime          string `json:"runtime"`
		Reason           string `json:"reason"`
		DifficultyImpact string `json:"difficulty_impact"`
	}
	if err := decodeAuthoringToolArguments(raw, &args); err != nil {
		return "", err
	}
	return c.apply(ctx, "metadata", args.Reason, args.DifficultyImpact, func(plan *Plan) error {
		if strings.TrimSpace(args.Title) == "" || strings.TrimSpace(args.Description) == "" {
			return errors.New("标题和简介不能为空")
		}
		if args.Difficulty != "easy" && args.Difficulty != "medium" && args.Difficulty != "hard" {
			return errors.New("difficulty 必须是 easy、medium 或 hard")
		}
		if args.Runtime != "container" && args.Runtime != "vcluster" {
			return errors.New("runtime 必须是 container 或 vcluster")
		}
		plan.Metadata = Metadata{Title: strings.TrimSpace(args.Title), Description: strings.TrimSpace(args.Description), Difficulty: args.Difficulty, Runtime: args.Runtime}
		return nil
	})
}

func (c *runtimeConversation) replaceOverview(ctx context.Context, raw string) (string, error) {
	var args struct {
		Markdown         string `json:"markdown"`
		Reason           string `json:"reason"`
		DifficultyImpact string `json:"difficulty_impact"`
	}
	if err := decodeAuthoringToolArguments(raw, &args); err != nil {
		return "", err
	}
	return c.apply(ctx, "overview", args.Reason, args.DifficultyImpact, func(plan *Plan) error {
		if strings.TrimSpace(args.Markdown) == "" {
			return errors.New("概览不能为空")
		}
		plan.Overview = strings.TrimSpace(args.Markdown)
		return nil
	})
}

func (c *runtimeConversation) upsertCheckpoint(ctx context.Context, raw string) (string, error) {
	var args struct {
		ID               string `json:"id"`
		Title            string `json:"title"`
		Markdown         string `json:"markdown"`
		Position         int    `json:"position"`
		Reason           string `json:"reason"`
		DifficultyImpact string `json:"difficulty_impact"`
	}
	if err := decodeAuthoringToolArguments(raw, &args); err != nil {
		return "", err
	}
	return c.apply(ctx, "checkpoint", args.Reason, args.DifficultyImpact, func(plan *Plan) error {
		if strings.TrimSpace(args.Title) == "" || strings.TrimSpace(args.Markdown) == "" || args.Position < 1 {
			return errors.New("检查点标题、说明不能为空，position 必须从 1 开始")
		}
		id := strings.TrimSpace(args.ID)
		if id == "" {
			id = NewID("checkpoint")
		}
		for index := range plan.Checkpoints {
			if plan.Checkpoints[index].ID == id {
				plan.Checkpoints[index] = Checkpoint{ID: id, Title: strings.TrimSpace(args.Title), Markdown: strings.TrimSpace(args.Markdown), Position: args.Position}
				return nil
			}
		}
		plan.Checkpoints = append(plan.Checkpoints, Checkpoint{ID: id, Title: strings.TrimSpace(args.Title), Markdown: strings.TrimSpace(args.Markdown), Position: args.Position})
		return nil
	})
}

func (c *runtimeConversation) removeCheckpoint(ctx context.Context, raw string) (string, error) {
	var args struct {
		ID               string `json:"id"`
		Reason           string `json:"reason"`
		DifficultyImpact string `json:"difficulty_impact"`
	}
	if err := decodeAuthoringToolArguments(raw, &args); err != nil {
		return "", err
	}
	return c.apply(ctx, "checkpoint", args.Reason, args.DifficultyImpact, func(plan *Plan) error {
		if strings.TrimSpace(args.ID) == "" {
			return errors.New("检查点 id 不能为空")
		}
		for index, checkpoint := range plan.Checkpoints {
			if checkpoint.ID == args.ID {
				plan.Checkpoints = append(plan.Checkpoints[:index], plan.Checkpoints[index+1:]...)
				return nil
			}
		}
		return fmt.Errorf("检查点 %q 不存在", args.ID)
	})
}

func (c *runtimeConversation) reorderCheckpoints(ctx context.Context, raw string) (string, error) {
	var args struct {
		IDs              []string `json:"ids"`
		Reason           string   `json:"reason"`
		DifficultyImpact string   `json:"difficulty_impact"`
	}
	if err := decodeAuthoringToolArguments(raw, &args); err != nil {
		return "", err
	}
	return c.apply(ctx, "checkpoint-order", args.Reason, args.DifficultyImpact, func(plan *Plan) error {
		if len(args.IDs) != len(plan.Checkpoints) {
			return errors.New("ids 必须恰好覆盖全部检查点")
		}
		positions := make(map[string]int, len(args.IDs))
		for index, id := range args.IDs {
			id = strings.TrimSpace(id)
			if id == "" {
				return errors.New("检查点 id 不能为空")
			}
			if _, exists := positions[id]; exists {
				return fmt.Errorf("检查点 %q 重复", id)
			}
			positions[id] = index + 1
		}
		for index := range plan.Checkpoints {
			position, exists := positions[plan.Checkpoints[index].ID]
			if !exists {
				return fmt.Errorf("检查点 %q 未包含在 ids 中", plan.Checkpoints[index].ID)
			}
			plan.Checkpoints[index].Position = position
		}
		return nil
	})
}

func decodeAuthoringToolArguments(raw string, target any) error {
	decoder := json.NewDecoder(bytes.NewBufferString(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode authoring tool arguments: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("authoring tool arguments have a second JSON document")
		}
		return fmt.Errorf("decode authoring tool arguments suffix: %w", err)
	}
	return nil
}
