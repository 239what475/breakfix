package authoring

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	claudecode "github.com/239what475/eino-claude-code"
	"github.com/breakfix/breakfix/internal/challenge"
	"github.com/breakfix/breakfix/internal/config"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"log/slog"
)

type Service struct {
	repo Repository
	llm  config.LLMConfig

	locksMu sync.Mutex
	locks   map[string]*sync.Mutex
}

func NewService(repo Repository, llm config.LLMConfig) *Service {
	return &Service{repo: repo, llm: llm, locks: make(map[string]*sync.Mutex)}
}

func (s *Service) Create(ctx context.Context, userID string) (*Session, error) {
	if strings.TrimSpace(userID) == "" {
		return nil, errors.New("authoring session requires a user")
	}
	return s.repo.CreateAuthoringSession(ctx, Session{
		ID:                NewID("author"),
		UserID:            userID,
		AgentSessionID:    claudecode.NewSessionID(),
		WorkflowSessionID: claudecode.NewSessionID(),
	}, Plan{})
}

func (s *Service) Get(ctx context.Context, userID, sessionID string) (*Session, *Revision, []Message, error) {
	session, err := s.repo.GetAuthoringSession(ctx, sessionID, userID)
	if err != nil {
		return nil, nil, nil, err
	}
	visibleRevision := session.CurrentRevision
	if session.VisibleRevision > 0 {
		visibleRevision = session.VisibleRevision
	}
	revision, err := s.repo.GetAuthoringRevision(ctx, session.ID, visibleRevision)
	if err != nil {
		return nil, nil, nil, err
	}
	messages, err := s.repo.ListAuthoringMessages(ctx, session.ID)
	if err != nil {
		return nil, nil, nil, err
	}
	return session, revision, messages, nil
}

func (s *Service) SendMessage(ctx context.Context, userID, sessionID, content string) (*Session, *Revision, Message, error) {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil, nil, Message{}, errors.New("消息不能为空")
	}
	lock := s.lock(sessionID)
	lock.Lock()
	defer lock.Unlock()

	session, err := s.repo.GetAuthoringSession(ctx, sessionID, userID)
	if err != nil {
		return nil, nil, Message{}, err
	}
	if !stateAllowsAuthorMessage(session.State) {
		return nil, nil, Message{}, ErrInvalidState
	}
	if err := s.repo.AppendAuthoringMessage(ctx, session.ID, Message{
		ID: NewID("message"), Role: "user", Content: content, CreatedAt: time.Now().UTC(),
	}); err != nil {
		return nil, nil, Message{}, err
	}
	revision, err := s.repo.GetAuthoringRevision(ctx, session.ID, session.CurrentRevision)
	if err != nil {
		return nil, nil, Message{}, err
	}
	conversation := &planConversation{
		service: s,
		userID:  userID,
		session: session,
		version: session.CurrentRevision,
		state:   session.State,
	}
	response, err := s.runAgent(ctx, conversation, content, revision.Plan)
	if err != nil {
		return nil, nil, Message{}, err
	}
	if err := s.repo.SetAuthoringAgentStarted(ctx, session.ID); err != nil {
		return nil, nil, Message{}, err
	}
	response = strings.TrimSpace(response)
	if response == "" {
		response = "题意约定已经更新，请查看左侧的当前 revision。"
	}
	agentMessage := Message{
		ID: NewID("message"), Role: "agent", Content: response,
		Changes: append([]Change{}, conversation.changes...), CreatedAt: time.Now().UTC(),
	}
	if err := s.repo.AppendAuthoringMessage(ctx, session.ID, agentMessage); err != nil {
		return nil, nil, Message{}, err
	}

	updated, err := s.repo.GetAuthoringSession(ctx, session.ID, userID)
	if err != nil {
		return nil, nil, Message{}, err
	}
	current, err := s.repo.GetAuthoringRevision(ctx, session.ID, updated.CurrentRevision)
	if err != nil {
		return nil, nil, Message{}, err
	}
	return updated, current, agentMessage, nil
}

func (s *Service) lock(sessionID string) *sync.Mutex {
	s.locksMu.Lock()
	defer s.locksMu.Unlock()
	lock := s.locks[sessionID]
	if lock == nil {
		lock = &sync.Mutex{}
		s.locks[sessionID] = lock
	}
	return lock
}

func (s *Service) runAgent(ctx context.Context, conversation *planConversation, userMessage string, plan Plan) (string, error) {
	planJSON, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		return "", err
	}
	opts := []claudecode.Option{
		claudecode.WithSystemPrompt(authoringSystemPrompt()),
		claudecode.WithTools(),
		claudecode.WithCustomTools(conversation.tools()...),
		claudecode.WithPermissionMode("dontAsk"),
		claudecode.WithMaxTurns(18),
		claudecode.WithStderr(func(line string) { slog.Debug("authoring-agent", "msg", line) }),
	}
	if env := claudeEnvironment(s.llm); len(env) > 0 {
		opts = append(opts, claudecode.WithEnv(env...))
	}
	if conversation.session.AgentStarted {
		opts = append(opts, claudecode.WithResume(conversation.session.AgentSessionID))
	} else {
		opts = append(opts, claudecode.WithSessionID(conversation.session.AgentSessionID))
	}
	agent, err := claudecode.New(opts...)
	if err != nil {
		return "", fmt.Errorf("create authoring agent: %w", err)
	}
	prompt := fmt.Sprintf(`当前题意约定版本为 %d。当前题意约定如下：

%s

作者本次消息：
%s`, conversation.version, string(planJSON), userMessage)
	runner := adk.NewRunner(ctx, adk.RunnerConfig{Agent: agent})
	events := runner.Run(ctx, []adk.Message{schema.UserMessage(prompt)})
	var lastMessage string
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
				lastMessage = content
			}
		}
		if event.Action != nil && event.Action.Exit {
			break
		}
	}
	return lastMessage, nil
}

func claudeEnvironment(llm config.LLMConfig) []string {
	values := []struct{ key, value string }{
		{"ANTHROPIC_BASE_URL", llm.BaseURL},
		{"ANTHROPIC_AUTH_TOKEN", llm.APIKey},
		{"ANTHROPIC_MODEL", llm.Model},
		{"ANTHROPIC_DEFAULT_OPUS_MODEL", llm.Model},
		{"ANTHROPIC_DEFAULT_SONNET_MODEL", llm.Model},
		{"ANTHROPIC_DEFAULT_HAIKU_MODEL", llm.HaikuModel},
		{"CLAUDE_CODE_SUBAGENT_MODEL", llm.HaikuModel},
		{"CLAUDE_CODE_EFFORT_LEVEL", llm.Effort},
	}
	env := make([]string, 0, len(values))
	for _, item := range values {
		if strings.TrimSpace(item.value) != "" {
			env = append(env, item.key+"="+item.value)
		}
	}
	return env
}

type planConversation struct {
	service *Service
	userID  string
	session *Session
	version int64
	state   SessionState
	changes []Change
}

func (c *planConversation) tools() []tool.InvokableTool {
	return []tool.InvokableTool{
		&authoringTool{name: "set_metadata", desc: "更新题目标题、简介、难度、标签和运行时。只有这些字段真实反映题意约定时才调用。", params: map[string]*schema.ParameterInfo{
			"intent_version":    {Type: schema.Integer, Desc: "当前题意约定版本", Required: true},
			"title":             {Type: schema.String, Desc: "题目标题", Required: true},
			"description":       {Type: schema.String, Desc: "给学习者看的简短题目简介", Required: true},
			"difficulty":        {Type: schema.String, Enum: []string{"easy", "medium", "hard"}, Required: true},
			"tags":              {Type: schema.Array, ElemInfo: &schema.ParameterInfo{Type: schema.String}, Required: true},
			"runtime":           {Type: schema.String, Enum: []string{"container", "vcluster"}, Required: true},
			"reason":            {Type: schema.String, Desc: "本次修改的简短理由", Required: true},
			"difficulty_impact": {Type: schema.String, Desc: "本次修改对题目难度的具体影响；没有变化时明确写“难度不变”", Required: true},
		}, run: c.setMetadata},
		&authoringTool{name: "replace_overview", desc: "替换题意约定概览。概览使用中文 Markdown，清楚说明场景、目标、初始故障、范围、难度和解法方向。", params: map[string]*schema.ParameterInfo{
			"intent_version":    {Type: schema.Integer, Desc: "当前题意约定版本", Required: true},
			"markdown":          {Type: schema.String, Desc: "完整概览 Markdown", Required: true},
			"reason":            {Type: schema.String, Desc: "本次修改的简短理由", Required: true},
			"difficulty_impact": {Type: schema.String, Desc: "本次修改对题目难度的具体影响；没有变化时明确写“难度不变”", Required: true},
		}, run: c.replaceOverview},
		&authoringTool{name: "upsert_checkpoint", desc: "新增或修改一个公开检查点。正文使用中文 Markdown 描述目标、可观察结果、依赖、提示方向和完成理由。", params: map[string]*schema.ParameterInfo{
			"intent_version":    {Type: schema.Integer, Desc: "当前题意约定版本", Required: true},
			"id":                {Type: schema.String, Desc: "已有检查点使用其 id；新增时可留空", Required: false},
			"title":             {Type: schema.String, Desc: "检查点标题", Required: true},
			"markdown":          {Type: schema.String, Desc: "检查点完整说明 Markdown", Required: true},
			"position":          {Type: schema.Integer, Desc: "从 1 开始的展示顺序", Required: true},
			"reason":            {Type: schema.String, Desc: "本次修改的简短理由", Required: true},
			"difficulty_impact": {Type: schema.String, Desc: "本次修改对题目难度的具体影响；没有变化时明确写“难度不变”", Required: true},
		}, run: c.upsertCheckpoint},
		&authoringTool{name: "remove_checkpoint", desc: "移除不再需要的检查点。", params: map[string]*schema.ParameterInfo{
			"intent_version":    {Type: schema.Integer, Desc: "当前题意约定版本", Required: true},
			"id":                {Type: schema.String, Desc: "要移除的检查点 id", Required: true},
			"reason":            {Type: schema.String, Desc: "移除理由", Required: true},
			"difficulty_impact": {Type: schema.String, Desc: "本次修改对题目难度的具体影响；没有变化时明确写“难度不变”", Required: true},
		}, run: c.removeCheckpoint},
		&authoringTool{name: "reorder_checkpoints", desc: "重新排序全部检查点，ids 必须恰好覆盖当前所有检查点。", params: map[string]*schema.ParameterInfo{
			"intent_version":    {Type: schema.Integer, Desc: "当前题意约定版本", Required: true},
			"ids":               {Type: schema.Array, ElemInfo: &schema.ParameterInfo{Type: schema.String}, Required: true},
			"reason":            {Type: schema.String, Desc: "排序理由", Required: true},
			"difficulty_impact": {Type: schema.String, Desc: "本次修改对题目难度的具体影响；没有变化时明确写“难度不变”", Required: true},
		}, run: c.reorderCheckpoints},
	}
}

func (c *planConversation) apply(ctx context.Context, version int64, kind, summary, difficultyImpact string, mutate func(*Plan) error) (string, error) {
	if version != c.version {
		return "", fmt.Errorf("intent_version=%d 已过期，当前版本为 %d", version, c.version)
	}
	if !stateAllowsAgentPlanRevision(c.state) {
		return "", ErrInvalidState
	}
	if strings.TrimSpace(summary) == "" || strings.TrimSpace(difficultyImpact) == "" {
		return "", errors.New("修改理由和难度影响不能为空")
	}
	current, err := c.service.repo.GetAuthoringRevision(ctx, c.session.ID, c.version)
	if err != nil {
		return "", err
	}
	plan := current.Plan.Clone()
	if err := mutate(&plan); err != nil {
		return "", err
	}
	nextState := nextPlanRevisionState(c.state)
	revision, err := c.service.repo.ReplaceAuthoringPlan(ctx, c.session.ID, c.userID, c.version, plan, nextState)
	if err != nil {
		return "", err
	}
	c.version = revision.Number
	c.state = nextState
	change := Change{
		Kind:             kind,
		Summary:          strings.TrimSpace(summary),
		DifficultyImpact: strings.TrimSpace(difficultyImpact),
		Revision:         revision.Number,
	}
	c.changes = append(c.changes, change)
	return fmt.Sprintf(`{"revision":%d,"change":%q}`, revision.Number, change.Summary), nil
}

// stateAllowsAuthorMessage controls externally initiated turns. Once a plan
// change has started a revision, the author must wait for the hidden
// generation/verification loop rather than enqueue another concurrent change.
func stateAllowsAuthorMessage(state SessionState) bool {
	switch state {
	case StateDraftConversation, StateIntentReview, StateAwaitingVerifiedReview:
		return true
	default:
		return false
	}
}

// stateAllowsAgentPlanRevision also admits RevisingAndVerifying. A single
// author message can require several function calls (for example, changing
// metadata, the overview, and a checkpoint). The Server starts generation
// only after that turn returns, so those calls must be allowed to finish the
// same revisioning turn without admitting another user message.
func stateAllowsAgentPlanRevision(state SessionState) bool {
	return stateAllowsAuthorMessage(state) || state == StateRevisingAndVerifying
}

// A plan edit after an artifact exists starts a fresh generation and
// verification cycle. The previous verified artifact stays visible until the
// replacement is verified.
func nextPlanRevisionState(state SessionState) SessionState {
	switch state {
	case StateAwaitingVerifiedReview:
		return StateRevisingAndVerifying
	case StateRevisingAndVerifying:
		return StateRevisingAndVerifying
	default:
		return StateIntentReview
	}
}

func (c *planConversation) setMetadata(ctx context.Context, raw string) (string, error) {
	var args struct {
		PlanVersion      int64    `json:"intent_version"`
		Title            string   `json:"title"`
		Description      string   `json:"description"`
		Difficulty       string   `json:"difficulty"`
		Tags             []string `json:"tags"`
		Runtime          string   `json:"runtime"`
		Reason           string   `json:"reason"`
		DifficultyImpact string   `json:"difficulty_impact"`
	}
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		return "", err
	}
	return c.apply(ctx, args.PlanVersion, "metadata", args.Reason, args.DifficultyImpact, func(plan *Plan) error {
		if strings.TrimSpace(args.Title) == "" || strings.TrimSpace(args.Description) == "" {
			return errors.New("标题和简介不能为空")
		}
		if args.Difficulty != "easy" && args.Difficulty != "medium" && args.Difficulty != "hard" {
			return errors.New("difficulty 必须是 easy、medium 或 hard")
		}
		runtime := challenge.NormalizeRuntime(args.Runtime)
		if runtime != challenge.RuntimeContainer && runtime != challenge.RuntimeVCluster {
			return errors.New("runtime 必须是 container 或 vcluster")
		}
		tags := make([]string, 0, len(args.Tags))
		for _, tag := range args.Tags {
			if tag = strings.TrimSpace(tag); tag != "" {
				tags = append(tags, tag)
			}
		}
		if len(tags) == 0 {
			return errors.New("至少需要一个非空标签")
		}
		plan.Metadata = Metadata{Title: strings.TrimSpace(args.Title), Description: strings.TrimSpace(args.Description), Difficulty: args.Difficulty, Tags: tags, Runtime: runtime}
		return nil
	})
}

func (c *planConversation) replaceOverview(ctx context.Context, raw string) (string, error) {
	var args struct {
		PlanVersion      int64  `json:"intent_version"`
		Markdown         string `json:"markdown"`
		Reason           string `json:"reason"`
		DifficultyImpact string `json:"difficulty_impact"`
	}
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		return "", err
	}
	return c.apply(ctx, args.PlanVersion, "overview", args.Reason, args.DifficultyImpact, func(plan *Plan) error {
		if strings.TrimSpace(args.Markdown) == "" {
			return errors.New("概览不能为空")
		}
		plan.Overview = strings.TrimSpace(args.Markdown)
		return nil
	})
}

func (c *planConversation) upsertCheckpoint(ctx context.Context, raw string) (string, error) {
	var args struct {
		PlanVersion      int64  `json:"intent_version"`
		ID               string `json:"id"`
		Title            string `json:"title"`
		Markdown         string `json:"markdown"`
		Position         int    `json:"position"`
		Reason           string `json:"reason"`
		DifficultyImpact string `json:"difficulty_impact"`
	}
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		return "", err
	}
	return c.apply(ctx, args.PlanVersion, "checkpoint", args.Reason, args.DifficultyImpact, func(plan *Plan) error {
		if strings.TrimSpace(args.Title) == "" || strings.TrimSpace(args.Markdown) == "" || args.Position < 1 {
			return errors.New("检查点标题、说明不能为空，position 必须从 1 开始")
		}
		id := strings.TrimSpace(args.ID)
		if id == "" {
			id = NewID("checkpoint")
		}
		for index := range plan.Checkpoints {
			if plan.Checkpoints[index].ID == id {
				plan.Checkpoints[index].Title = strings.TrimSpace(args.Title)
				plan.Checkpoints[index].Markdown = strings.TrimSpace(args.Markdown)
				plan.Checkpoints[index].Position = args.Position
				return nil
			}
		}
		plan.Checkpoints = append(plan.Checkpoints, Checkpoint{ID: id, Title: strings.TrimSpace(args.Title), Markdown: strings.TrimSpace(args.Markdown), Position: args.Position})
		return nil
	})
}

func (c *planConversation) removeCheckpoint(ctx context.Context, raw string) (string, error) {
	var args struct {
		PlanVersion      int64  `json:"intent_version"`
		ID               string `json:"id"`
		Reason           string `json:"reason"`
		DifficultyImpact string `json:"difficulty_impact"`
	}
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		return "", err
	}
	return c.apply(ctx, args.PlanVersion, "checkpoint", args.Reason, args.DifficultyImpact, func(plan *Plan) error {
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

func (c *planConversation) reorderCheckpoints(ctx context.Context, raw string) (string, error) {
	var args struct {
		PlanVersion      int64    `json:"intent_version"`
		IDs              []string `json:"ids"`
		Reason           string   `json:"reason"`
		DifficultyImpact string   `json:"difficulty_impact"`
	}
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		return "", err
	}
	return c.apply(ctx, args.PlanVersion, "checkpoint-order", args.Reason, args.DifficultyImpact, func(plan *Plan) error {
		if len(args.IDs) != len(plan.Checkpoints) {
			return errors.New("ids 必须恰好覆盖全部检查点")
		}
		positions := make(map[string]int, len(args.IDs))
		for index, id := range args.IDs {
			id = strings.TrimSpace(id)
			if id == "" {
				return errors.New("检查点 id 不能为空")
			}
			if _, ok := positions[id]; ok {
				return fmt.Errorf("检查点 %q 重复", id)
			}
			positions[id] = index + 1
		}
		for index := range plan.Checkpoints {
			position, ok := positions[plan.Checkpoints[index].ID]
			if !ok {
				return fmt.Errorf("检查点 %q 未包含在 ids 中", plan.Checkpoints[index].ID)
			}
			plan.Checkpoints[index].Position = position
		}
		return nil
	})
}

type authoringTool struct {
	name   string
	desc   string
	params map[string]*schema.ParameterInfo
	run    func(context.Context, string) (string, error)
}

func (t *authoringTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: t.name, Desc: t.desc, ParamsOneOf: schema.NewParamsOneOfByParams(t.params)}, nil
}

func (t *authoringTool) InvokableRun(ctx context.Context, args string, _ ...tool.Option) (string, error) {
	return t.run(ctx, args)
}

func authoringSystemPrompt() string {
	return `你是 Breakfix 的题目策划与审核 agent。你和作者共同设计一题可学习、可真实验证的 SRE 挑战。

你只能通过提供的 authoring functions 修改题意约定；绝不能声称已经修改而没有成功调用函数。不要生成任何源码、Dockerfile、脚本或镜像，也不能开始候选生成或真实验证。用户不直接编辑题目资产，所有题意约定修改都必须经过函数调用与新的 revision。

题意约定规则：
1. 先理解作者意图。信息不足时可只提出具体澄清问题。
2. 题意约定足够明确时，先 set_metadata，再 replace_overview，并用 upsert_checkpoint 建立公开检查点。每次函数调用必须使用工具返回的最新 intent_version，并填写实际的修改理由和难度影响；难度没有变化时明确填写“难度不变”。
3. 概览和检查点正文均使用中文 Markdown。检查点验证最终可观察结果，不规定用户必须执行的命令或唯一的文件编辑路径。
4. 运行时只能是 container 或 vcluster。container 是普通用户容器；vcluster 表示用户容器额外操纵隔离 Kubernetes 集群。
5. 题意约定完整后直接告知作者可以点击界面上的“生成并验证题目”。你没有任何生成、验证或发布工具。
6. 已验证题目审核中，作者要求改动时继续使用函数修改题意约定。实际生成文件由后续 generator 负责；你不能假装已经查看或修改过源码。
7. 真实验证失败不会展示给作者，系统会自动把反馈交给 generator 修复。不能声称已经生成、验证或发布。
8. 回复保持简洁，说明你理解的变更、仍需澄清的地方或已经落盘的 revision。`
}
