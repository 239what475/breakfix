package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	appauthoring "github.com/breakfix/breakfix/internal/application/authoring"
	"github.com/breakfix/breakfix/internal/bootstrap/config"
	"github.com/breakfix/breakfix/internal/domain/agent"
	domain "github.com/breakfix/breakfix/internal/domain/authoring"
	generation "github.com/breakfix/breakfix/internal/domain/generation"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

// AuthoringExecutor is the Eino implementation of the interactive authoring
// application port. It owns model setup and tool execution only.
type AuthoringExecutor struct {
	config    config.AgentConfig
	generator appauthoring.GeneratorOperations
}

func NewAuthoringExecutor(cfg config.AgentConfig, generator appauthoring.GeneratorOperations) *AuthoringExecutor {
	return &AuthoringExecutor{config: cfg, generator: generator}
}

func (e *AuthoringExecutor) Run(ctx context.Context, execution appauthoring.Execution, updater appauthoring.StageUpdater, emit func(appauthoring.StreamEvent)) (string, error) {
	if strings.TrimSpace(execution.RunID) == "" || strings.TrimSpace(execution.UserID) == "" || strings.TrimSpace(execution.SessionID) == "" || updater == nil || e.generator == nil {
		return "", errors.New("authoring execution requires a trusted run, user, session, stage updater, and generator service")
	}
	if len(execution.History) == 0 || execution.History[len(execution.History)-1].Role != "user" {
		return "", errors.New("authoring execution requires a latest user message")
	}
	chat, err := NewChatModel(ctx, e.config)
	if err != nil {
		return "", err
	}
	conversation := &runtimeConversation{
		runID: execution.RunID, userID: execution.UserID, sessionID: execution.SessionID, userMessageID: execution.UserMessageID,
		updater: updater, generator: e.generator, stage: execution.Stage,
	}
	defer conversation.releaseWorkspaceTurn()
	inputs, err := authoringInputs(conversation, execution.History)
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
				return IsTransientTransportError(err)
			},
		},
	})
	if err != nil {
		return "", fmt.Errorf("create Eino authoring agent: %w", err)
	}
	runner := adk.NewRunner(ctx, adk.RunnerConfig{Agent: agent, EnableStreaming: true})
	events := runner.Run(ctx, inputs)
	var response strings.Builder
	for {
		event, ok := events.Next()
		if !ok {
			break
		}
		if event.Err != nil {
			return "", event.Err
		}
		if event.Output != nil && event.Output.MessageOutput != nil {
			output := event.Output.MessageOutput
			if output.IsStreaming && output.MessageStream != nil {
				stream := output.MessageStream
				for {
					chunk, streamErr := stream.Recv()
					if errors.Is(streamErr, io.EOF) {
						break
					}
					if streamErr != nil {
						stream.Close()
						return "", fmt.Errorf("read Eino authoring stream: %w", streamErr)
					}
					if chunk == nil || chunk.Content == "" {
						continue
					}
					response.WriteString(chunk.Content)
					if emit != nil {
						emit(appauthoring.StreamEvent{Content: chunk.Content})
					}
				}
				stream.Close()
			}
			if output.Message != nil && output.Message.Content != "" {
				response.WriteString(output.Message.Content)
				if emit != nil {
					emit(appauthoring.StreamEvent{Content: output.Message.Content})
				}
			}
		}
		if event.Action != nil && event.Action.Exit {
			break
		}
	}
	content := strings.TrimSpace(response.String())
	if content == "" {
		return "", errors.New("authoring agent returned an empty response")
	}
	return content, nil
}

func authoringInputs(conversation *runtimeConversation, history []agent.Message) ([]adk.Message, error) {
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
	runID         string
	userID        string
	sessionID     string
	userMessageID string
	updater       appauthoring.StageUpdater
	generator     appauthoring.GeneratorOperations
	stage         domain.Stage
	turn          *generation.WorkspaceTurn
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
			"runtime":    {Type: schema.String, Enum: []string{"node", "k8s"}, Required: true},
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
		&authoringTool{name: "confirm_generation", desc: "在作者已明确确认一个已持久化的 Plan revision 后创建生成任务。当前回合修改过 Plan 时不能调用，必须等该回合结束后由作者在新消息中确认。", params: map[string]*schema.ParameterInfo{
			"plan_revision": {Type: schema.Integer, Desc: "作者明确确认的当前 Plan revision", Required: true},
		}, run: c.confirmGeneration},
		&authoringTool{name: "list_active_generations", desc: "列出当前作者尚未结束的生成任务及其状态。", params: map[string]*schema.ParameterInfo{}, run: c.listActiveGenerations},
		&authoringTool{name: "get_generation", desc: "读取一个生成任务的状态、当前 candidate 和可供下一步处理的反馈。", params: map[string]*schema.ParameterInfo{
			"workflow_id": {Type: schema.String, Desc: "生成任务 ID", Required: true},
		}, run: c.getGeneration},
		&authoringTool{name: "list_workspace_files", desc: "列出一个 Generating 任务远程工作区中的文件。", params: map[string]*schema.ParameterInfo{
			"workflow_id": {Type: schema.String, Desc: "Generating 任务 ID", Required: true},
		}, run: c.listWorkspaceFiles},
		&authoringTool{name: "read_workspace_file", desc: "读取一个 Generating 任务远程工作区中的相对路径文件。", params: map[string]*schema.ParameterInfo{
			"workflow_id": {Type: schema.String, Desc: "Generating 任务 ID", Required: true},
			"path":        {Type: schema.String, Desc: "工作区相对路径", Required: true},
			"offset":      {Type: schema.Integer, Desc: "从 1 开始的行号；省略时从第一行开始", Required: false},
			"limit":       {Type: schema.Integer, Desc: "最多读取的行数；省略时读取剩余内容", Required: false},
		}, run: c.readWorkspaceFile},
		&authoringTool{name: "write_workspace_file", desc: "完整写入一个 Generating 任务远程工作区中的相对路径文件。", params: map[string]*schema.ParameterInfo{
			"workflow_id": {Type: schema.String, Desc: "Generating 任务 ID", Required: true},
			"path":        {Type: schema.String, Desc: "工作区相对路径", Required: true},
			"content":     {Type: schema.String, Desc: "文件完整内容", Required: true},
		}, run: c.writeWorkspaceFile},
		&authoringTool{name: "run_workspace_command", desc: "在一个 Generating 任务远程工作区中执行命令，并返回退出码和输出。", params: map[string]*schema.ParameterInfo{
			"workflow_id": {Type: schema.String, Desc: "Generating 任务 ID", Required: true},
			"command":     {Type: schema.String, Desc: "在工作区中执行的命令", Required: true},
		}, run: c.runWorkspaceCommand},
		&authoringTool{name: "submit_candidate", desc: "归档并提交当前 Generating 任务的工作区，进入 Server 内部审核。", params: map[string]*schema.ParameterInfo{
			"workflow_id": {Type: schema.String, Desc: "Generating 任务 ID", Required: true},
		}, run: c.submitCandidate},
		&authoringTool{name: "confirm_content", desc: "作者已审阅当前已验证 candidate 后，确认内容并启动 Server 内部分类。", params: map[string]*schema.ParameterInfo{
			"workflow_id":           {Type: schema.String, Desc: "待内容审核的任务 ID", Required: true},
			"candidate_revision_id": {Type: schema.String, Desc: "作者确认的 candidate revision ID", Required: true},
		}, run: c.confirmContent},
		&authoringTool{name: "request_content_changes", desc: "作者要求修改当前内容审核中的 candidate，返回工作区继续修复。", params: map[string]*schema.ParameterInfo{
			"workflow_id":           {Type: schema.String, Desc: "待内容审核的任务 ID", Required: true},
			"candidate_revision_id": {Type: schema.String, Desc: "作者审阅的 candidate revision ID", Required: true},
			"feedback":              {Type: schema.String, Desc: "作者的具体内容修改意见", Required: true},
		}, run: c.requestContentChanges},
		&authoringTool{name: "get_classification", desc: "读取 Server 内部 Classifier 为当前 candidate 提出的分类提案。", params: map[string]*schema.ParameterInfo{
			"workflow_id": {Type: schema.String, Desc: "生成任务 ID", Required: true},
		}, run: c.getClassification},
		&authoringTool{name: "request_classification_changes", desc: "作者要求 Server 内部 Classifier 根据反馈修订当前分类提案。", params: map[string]*schema.ParameterInfo{
			"workflow_id":           {Type: schema.String, Desc: "待分类审核的任务 ID", Required: true},
			"candidate_revision_id": {Type: schema.String, Desc: "作者审阅的 candidate revision ID", Required: true},
			"proposal_revision":     {Type: schema.Integer, Desc: "作者审阅的分类 proposal revision", Required: true},
			"feedback":              {Type: schema.String, Desc: "作者的具体分类修改意见", Required: true},
		}, run: c.requestClassificationChanges},
		&authoringTool{name: "confirm_classification_and_publish", desc: "作者已确认当前分类提案后，创建题目发布动作。", params: map[string]*schema.ParameterInfo{
			"workflow_id":           {Type: schema.String, Desc: "待分类审核的任务 ID", Required: true},
			"candidate_revision_id": {Type: schema.String, Desc: "作者确认的 candidate revision ID", Required: true},
			"proposal_revision":     {Type: schema.Integer, Desc: "作者确认的分类 proposal revision", Required: true},
		}, run: c.confirmClassificationAndPublish},
		&authoringTool{name: "cancel_generation", desc: "在作者明确要求后取消一个尚未结束的生成任务；工作区由后台回收。", params: map[string]*schema.ParameterInfo{
			"workflow_id": {Type: schema.String, Desc: "要取消的生成任务 ID", Required: true},
		}, run: c.cancelGeneration},
	}
}

func (c *runtimeConversation) apply(ctx context.Context, kind, summary, difficultyImpact string, mutate func(*domain.Plan) error) (string, error) {
	if strings.TrimSpace(summary) == "" || strings.TrimSpace(difficultyImpact) == "" {
		return "", invalidToolInput(errors.New("修改理由和难度影响不能为空"))
	}
	plan := c.stage.Plan.Clone()
	if err := mutate(&plan); err != nil {
		return "", invalidToolInput(err)
	}
	stage, err := c.updater.UpdateAuthoringStage(ctx, c.runID, c.stage.RunAttempt, c.stage.StageRevision, plan, domain.Change{Kind: kind, Summary: strings.TrimSpace(summary), DifficultyImpact: strings.TrimSpace(difficultyImpact)})
	if err != nil {
		return "", err
	}
	c.stage = *stage
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
	return c.apply(ctx, "metadata", args.Reason, args.DifficultyImpact, func(plan *domain.Plan) error {
		if strings.TrimSpace(args.Title) == "" || strings.TrimSpace(args.Description) == "" {
			return errors.New("标题和简介不能为空")
		}
		if args.Difficulty != "easy" && args.Difficulty != "medium" && args.Difficulty != "hard" {
			return errors.New("difficulty 必须是 easy、medium 或 hard")
		}
		if args.Runtime != "node" && args.Runtime != "k8s" {
			return errors.New("runtime 必须是 node 或 k8s")
		}
		plan.Metadata = domain.Metadata{Title: strings.TrimSpace(args.Title), Description: strings.TrimSpace(args.Description), Difficulty: args.Difficulty, Runtime: args.Runtime}
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
	return c.apply(ctx, "overview", args.Reason, args.DifficultyImpact, func(plan *domain.Plan) error {
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
	return c.apply(ctx, "checkpoint", args.Reason, args.DifficultyImpact, func(plan *domain.Plan) error {
		if strings.TrimSpace(args.Title) == "" || strings.TrimSpace(args.Markdown) == "" || args.Position < 1 {
			return errors.New("检查点标题、说明不能为空，position 必须从 1 开始")
		}
		id := strings.TrimSpace(args.ID)
		if id == "" {
			id = domain.NewID("checkpoint")
		}
		for index := range plan.Checkpoints {
			if plan.Checkpoints[index].ID == id {
				plan.Checkpoints[index] = domain.Checkpoint{ID: id, Title: strings.TrimSpace(args.Title), Markdown: strings.TrimSpace(args.Markdown), Position: args.Position}
				return nil
			}
		}
		plan.Checkpoints = append(plan.Checkpoints, domain.Checkpoint{ID: id, Title: strings.TrimSpace(args.Title), Markdown: strings.TrimSpace(args.Markdown), Position: args.Position})
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
	return c.apply(ctx, "checkpoint", args.Reason, args.DifficultyImpact, func(plan *domain.Plan) error {
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
	return c.apply(ctx, "checkpoint-order", args.Reason, args.DifficultyImpact, func(plan *domain.Plan) error {
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
		return invalidToolInput(fmt.Errorf("decode authoring tool arguments: %w", err))
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return invalidToolInput(errors.New("authoring tool arguments have a second JSON document"))
		}
		return invalidToolInput(fmt.Errorf("decode authoring tool arguments suffix: %w", err))
	}
	return nil
}
