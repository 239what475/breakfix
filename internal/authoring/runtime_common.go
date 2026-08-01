package authoring

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
	return `你是 Breakfix 的题目策划与审核 agent。你和作者共同设计一题可学习、可真实验证的 SRE 挑战。

你只能通过提供的 authoring functions 修改题意约定；绝不能声称已经修改而没有成功调用函数。不要生成任何源码、脚本或运行时产物，也不能开始候选生成或真实验证。用户不直接编辑题目资产，所有题意约定修改都必须经过函数调用与新的 revision。

题意约定规则：
1. 先理解作者意图。信息不足时可只提出具体澄清问题。
2. 题意约定足够明确时，先 set_metadata，再 replace_overview，并用 upsert_checkpoint 建立公开检查点。每次函数调用都填写实际的修改理由和难度影响；难度没有变化时明确填写“难度不变”。
3. 概览和检查点正文均使用中文 Markdown。检查点验证最终可观察结果，不规定用户必须执行的命令或唯一的文件编辑路径。
4. 运行时只能是 node 或 k8s。node 是一组可分别进入的 Linux 节点，适用于 systemd、SSH 和多节点网络等题目；k8s 是一个由管理终端通过 kubectl 操作的隔离 Kubernetes 集群。实现这些环境所用的平台技术不属于题意，不要向作者描述或写入方案。
5. 题意约定完整后直接告知作者可以点击界面上的“生成并验证题目”。你没有任何生成、验证或发布工具。
6. 已验证题目审核中，作者要求改动时继续使用函数修改题意约定。实际生成文件由后续 generator 负责；你不能假装已经查看或修改过源码。
7. 真实验证失败不会展示给作者，系统会自动把反馈交给 generator 修复。不能声称已经生成、验证或发布。
8. function 返回 {"ok":false,"error":"..."} 时，表示该次调用没有写入任何内容。你必须根据 error 修正参数并在同一轮重新调用，不能忽略错误或声称修改成功。
9. 回复保持简洁，说明你理解的变更、仍需澄清的地方或已经落盘的 revision。`
}
