package llm

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

type authoringTool struct {
	name   string
	desc   string
	params map[string]*schema.ParameterInfo
	run    func(context.Context, string) (string, error)
}

// invalidToolInputError marks a rejected model request. The rejected request
// must be returned to the Agent as a tool result so it can correct its own
// arguments in the same conversation; it must never change the staged plan.
type invalidToolInputError struct {
	err error
}

func (e *invalidToolInputError) Error() string {
	return e.err.Error()
}

func (e *invalidToolInputError) Unwrap() error {
	return e.err
}

func invalidToolInput(err error) error {
	if err == nil {
		return nil
	}
	return &invalidToolInputError{err: err}
}

func (t *authoringTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: t.name, Desc: t.desc, ParamsOneOf: schema.NewParamsOneOfByParams(t.params)}, nil
}

func (t *authoringTool) InvokableRun(ctx context.Context, args string, _ ...tool.Option) (string, error) {
	result, err := t.run(ctx, args)
	if err == nil {
		return result, nil
	}
	var inputErr *invalidToolInputError
	if !errors.As(err, &inputErr) {
		return "", err
	}
	payload, marshalErr := json.Marshal(struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}{OK: false, Error: inputErr.Error()})
	if marshalErr != nil {
		return "", marshalErr
	}
	return string(payload), nil
}

func authoringSystemPrompt() string {
	return `你是 Breakfix 的题目策划与生成 agent。你与作者共同把真实运维场景沉淀为可学习、可真实验证的 SRE 挑战。

你只能依据工具成功返回的结果声称已修改、已提交或已推进任务。信息不足时先提出具体澄清问题。题意约定的概览与检查点使用中文 Markdown；检查点描述可观察的最终状态，不规定唯一命令或编辑路径。运行时只能是 node 或 k8s，底层平台实现不属于题意。

Plan：使用 Plan 工具修改当前回合的私有 Plan。每次修改都填写真实的理由和难度影响。回合内的 Plan 修改会在回合成功结束后持久化为新的 revision；同一回合不能确认生成该修改后的 Plan。作者在下一条消息明确确认时，才调用 confirm_generation；plan_revision 必须使用该消息中给出的当前已持久化 revision 编号。

生成：confirm_generation 创建用户拥有的生成任务。之后只对明确提供 workflow_id 的 Generating 任务使用 workspace 工具读写文件、执行命令和提交 candidate。一个回合只能写入一个任务的 workspace。submit_candidate 后，Judge、Build、Artifact Publish 和 Verify 都由 Server 内部流程异步完成；不要把它们说成已完成，也不要假定系统会自动修复反馈。

审核：读取任务和 candidate 以了解当前状态与反馈。只有作者在当前对话中明确授权时，才调用内容确认、内容修改请求、分类修改请求、分类确认发布或取消工具；这些调用必须针对工具返回的当前版本。分类 proposal 只由 Server 内部 Classifier 生成。你只能读取 proposal、传递作者反馈或确认发布，不能自行写入 topic、tag 或路线图关系。

工具返回 {"ok":false,"error":"..."} 表示本次调用未成功；根据错误修正后再继续。回复保持简洁，说明已确认的状态、下一步需要作者决定的事项或尚缺的信息。`
}
