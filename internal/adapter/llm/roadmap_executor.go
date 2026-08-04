package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	roadmapapp "github.com/breakfix/breakfix/internal/application/roadmap"
	"github.com/breakfix/breakfix/internal/bootstrap/config"
	roadmapdomain "github.com/breakfix/breakfix/internal/domain/roadmap"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

const roadmapCommitteeMaxIterations = 18

// RoadmapExecutor is the Eino implementation of the Server-owned Roadmap
// committee. It has no persistence access: all durable calls, leases, and
// graph publication remain in application and PostgreSQL layers.
type RoadmapExecutor struct {
	config config.AgentConfig
}

func NewRoadmapExecutor(cfg config.AgentConfig) *RoadmapExecutor {
	return &RoadmapExecutor{config: cfg}
}

func (e *RoadmapExecutor) Plan(ctx context.Context, request roadmapapp.PlannerRequest) (roadmapdomain.ChangeSet, error) {
	if request.Retrieval == nil || !request.Task.Valid() || !roadmapdomain.ValidRevision(request.Revision.Revision) || request.Retrieval.Revision() != request.Revision.Revision {
		return roadmapdomain.ChangeSet{}, errors.New("roadmap planner requires a fixed valid task revision and retrieval")
	}
	conversation := roadmapConversation{retrieval: request.Retrieval}
	result, err := NewResultTool[roadmapdomain.ChangeSet]("submit_changeset", "提交当前 subject 的完整关系候选；没有合理关系时提交空 edges。", func(value roadmapdomain.ChangeSet) error {
		return value.ValidateFor(request.Task.Kind, request.Task.Subject, request.Revision)
	})
	if err != nil {
		return roadmapdomain.ChangeSet{}, err
	}
	tools := append(conversation.tools(), result)
	if err := runRoadmapCommitteeAgent(ctx, e.config, "roadmap_planner", plannerInstruction(), plannerPrompt(request.Task), tools); err != nil {
		return roadmapdomain.ChangeSet{}, err
	}
	value, called := result.Value()
	if !called {
		return roadmapdomain.ChangeSet{}, errors.New("roadmap planner did not submit a typed changeset")
	}
	return value, nil
}

func (e *RoadmapExecutor) Review(ctx context.Context, request roadmapapp.ReviewerRequest) (roadmapdomain.Review, error) {
	if request.Role != roadmapdomain.AgentCurriculumReviewer && request.Role != roadmapdomain.AgentSREReviewer || request.Retrieval == nil || !request.Task.Valid() ||
		!roadmapdomain.ValidRevision(request.Revision.Revision) || request.Retrieval.Revision() != request.Revision.Revision {
		return roadmapdomain.Review{}, errors.New("roadmap reviewer requires a valid role, fixed task revision, and retrieval")
	}
	if err := request.ChangeSet.ValidateFor(request.Task.Kind, request.Task.Subject, request.Revision); err != nil {
		return roadmapdomain.Review{}, fmt.Errorf("invalid roadmap changeset for review: %w", err)
	}
	conversation := roadmapConversation{retrieval: request.Retrieval}
	result, err := NewResultTool[roadmapdomain.Review]("submit_review", "提交批准，或提交带具体修改意见的拒绝结论。", func(value roadmapdomain.Review) error {
		return value.Validate()
	})
	if err != nil {
		return roadmapdomain.Review{}, err
	}
	tools := append(conversation.tools(), result)
	if err := runRoadmapCommitteeAgent(ctx, e.config, string(request.Role), reviewerInstruction(request.Role), reviewerPrompt(request), tools); err != nil {
		return roadmapdomain.Review{}, err
	}
	value, called := result.Value()
	if !called {
		return roadmapdomain.Review{}, errors.New("roadmap reviewer did not submit a typed review")
	}
	return value, nil
}

func runRoadmapCommitteeAgent(ctx context.Context, cfg config.AgentConfig, name, instruction, prompt string, values []tool.InvokableTool) error {
	chat, err := NewChatModel(ctx, cfg)
	if err != nil {
		return err
	}
	tools := make([]tool.BaseTool, 0, len(values))
	for _, value := range values {
		tools = append(tools, value)
	}
	agent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name: name, Description: "Breakfix roadmap maintenance committee member", Instruction: instruction, Model: chat,
		MaxIterations: roadmapCommitteeMaxIterations,
		ToolsConfig: adk.ToolsConfig{ToolsNodeConfig: compose.ToolsNodeConfig{
			Tools: tools, ExecuteSequentially: true,
		}},
		ModelRetryConfig: &adk.ModelRetryConfig{
			MaxRetries: 3,
			IsRetryAble: func(_ context.Context, err error) bool {
				return IsTransientTransportError(err)
			},
		},
	})
	if err != nil {
		return fmt.Errorf("create roadmap committee agent: %w", err)
	}
	runner := adk.NewRunner(ctx, adk.RunnerConfig{Agent: agent})
	events := runner.Run(ctx, []adk.Message{schema.UserMessage(prompt)})
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

type roadmapConversation struct {
	retrieval *roadmapapp.Retrieval
}

func (c roadmapConversation) tools() []tool.InvokableTool {
	return []tool.InvokableTool{
		&roadmapTool{name: "search_topics", desc: "按关键词搜索当前固定 Roadmap revision 中的其他 Topic。", params: map[string]*schema.ParameterInfo{
			"query":     {Type: schema.String, Desc: "用于比较课程主题的关键词", Required: true},
			"domain_id": {Type: schema.String, Desc: "可选的 Domain ID", Required: false},
			"limit":     {Type: schema.Integer, Desc: "返回数量，范围为 1 到 20", Required: true},
		}, run: c.searchTopics},
		&roadmapTool{name: "read_topic", desc: "读取一个 Topic 的完整课程定义。", params: map[string]*schema.ParameterInfo{
			"id": {Type: schema.String, Desc: "Topic ID", Required: true},
		}, run: c.readTopic},
		&roadmapTool{name: "search_challenges", desc: "按关键词搜索当前固定 Roadmap revision 中的其他 Challenge。", params: map[string]*schema.ParameterInfo{
			"query":    {Type: schema.String, Desc: "用于比较题目内容的关键词", Required: true},
			"topic_id": {Type: schema.String, Desc: "可选的 Topic ID", Required: false},
			"limit":    {Type: schema.Integer, Desc: "返回数量，范围为 1 到 20", Required: true},
		}, run: c.searchChallenges},
		&roadmapTool{name: "read_challenge", desc: "读取一个 Challenge 的题意、解答、检查点和课程绑定。", params: map[string]*schema.ParameterInfo{
			"id": {Type: schema.String, Desc: "Challenge ID", Required: true},
		}, run: c.readChallenge},
	}
}

func (c roadmapConversation) searchTopics(_ context.Context, raw string) (string, error) {
	args, err := decodeRoadmapTool[roadmapapp.TopicSearch](raw)
	if err != nil {
		return "", err
	}
	values, err := c.retrieval.SearchTopics(args)
	if err != nil {
		return "", err
	}
	return marshalRoadmapToolResult(values)
}

func (c roadmapConversation) readTopic(_ context.Context, raw string) (string, error) {
	var args struct {
		ID string `json:"id"`
	}
	if err := decodeRoadmapToolInto(raw, &args); err != nil {
		return "", err
	}
	value, err := c.retrieval.ReadTopic(strings.TrimSpace(args.ID))
	if err != nil {
		return "", err
	}
	return marshalRoadmapToolResult(value)
}

func (c roadmapConversation) searchChallenges(_ context.Context, raw string) (string, error) {
	args, err := decodeRoadmapTool[roadmapapp.ChallengeSearch](raw)
	if err != nil {
		return "", err
	}
	values, err := c.retrieval.SearchChallenges(args)
	if err != nil {
		return "", err
	}
	return marshalRoadmapToolResult(values)
}

func (c roadmapConversation) readChallenge(ctx context.Context, raw string) (string, error) {
	var args struct {
		ID string `json:"id"`
	}
	if err := decodeRoadmapToolInto(raw, &args); err != nil {
		return "", err
	}
	value, err := c.retrieval.ReadChallenge(ctx, strings.TrimSpace(args.ID))
	if err != nil {
		return "", err
	}
	return marshalRoadmapToolResult(value)
}

type roadmapTool struct {
	name   string
	desc   string
	params map[string]*schema.ParameterInfo
	run    func(context.Context, string) (string, error)
}

func (t *roadmapTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: t.name, Desc: t.desc, ParamsOneOf: schema.NewParamsOneOfByParams(t.params)}, nil
}

func (t *roadmapTool) InvokableRun(ctx context.Context, raw string, _ ...tool.Option) (string, error) {
	return t.run(ctx, raw)
}

func decodeRoadmapTool[T any](raw string) (T, error) {
	return decodeStrict[T](raw)
}

func decodeRoadmapToolInto(raw string, target any) error {
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("tool arguments contain a second JSON document")
		}
		return err
	}
	return nil
}

func marshalRoadmapToolResult(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func plannerInstruction() string {
	return `你负责为一个指定的 Topic 或 Challenge 提议局部课程关系。

只考虑当前 subject 与已发布实体之间的关系。先按需搜索和读取证据；不要假设未读取的题目内容。不得创建、修改或合并 Domain、Topic、Tag，也不得修改题目分类。

只有确实有教学意义的关系才提出边。precedes 表示明确的推荐学习先后；related 表示直接的横向关联。没有合理关系是合法结论。完成后调用 submit_changeset 一次提交完整候选。`
}

func reviewerInstruction(role roadmapdomain.AgentRole) string {
	if role == roadmapdomain.AgentCurriculumReviewer {
		return `你审查一个局部课程关系候选。检查关系是否能由题目或 Topic 内容支持、是否表达真实的学习递进或直接关联、理由是否清楚。可按需搜索和读取证据。批准时不要附带反馈；拒绝时给出 Planner 能据此修订的具体反馈。完成后调用 submit_review 一次。`
	}
	return `你从 SRE/运维实践角度审查一个局部课程关系候选。检查关系是否体现真实的技术前置、排障路径或可解释的横向关联，而非只因名称相似。可按需搜索和读取证据。批准时不要附带反馈；拒绝时给出 Planner 能据此修订的具体反馈。完成后调用 submit_review 一次。`
}

func plannerPrompt(task roadmapdomain.Task) string {
	payload, _ := json.Marshal(struct {
		Kind    roadmapdomain.TaskKind `json:"task_kind"`
		Subject roadmapdomain.Subject  `json:"subject"`
	}{Kind: task.Kind, Subject: task.Subject})
	return "当前 task：\n" + string(payload)
}

func reviewerPrompt(request roadmapapp.ReviewerRequest) string {
	payload, _ := json.Marshal(struct {
		Kind      roadmapdomain.TaskKind  `json:"task_kind"`
		Subject   roadmapdomain.Subject   `json:"subject"`
		ChangeSet roadmapdomain.ChangeSet `json:"changeset"`
	}{Kind: request.Task.Kind, Subject: request.Task.Subject, ChangeSet: request.ChangeSet})
	return "请审查当前关系候选：\n" + string(payload)
}
