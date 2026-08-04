# Agent Runtime

Breakfix 使用 Eino、PostgreSQL AgentRun 和远程 OpenSandbox workspace 运行模型能力。系统不保留
Claude Code CLI、`eino-claude-code` 或另一套 Agent SDK 的兼容路径。

## 两类执行边界

| 场景 | 执行者 | 持久化 | 调度方式 |
| --- | --- | --- | --- |
| Authoring 对话 | Server | AuthoringSession、消息、AgentRun、stage | 同一会话串行，直接执行。 |
| 学习 Assistant | Server | Assistant session、消息、AgentRun、工具证据 | 同一会话串行，直接执行。 |
| Generator / Judge | Generate Worker | GenerationWorkflow、AgentRun、CandidateRevision | 持有一个 Workflow lease。 |

`AgentRun` 只记录一次模型调用的 session、输入、结构化结果和执行状态；它不拥有 lease，也不是
调度任务。当前后台 Agent 调度与恢复的权威是 `GenerationWorkflow`。

## Generate Worker

Generate Worker 只通过 Server 内部 API 领取 `GenerationWorkflow`。一次 lease 可以跨越所有活动
state；每次阶段成功后 Server 返回刷新后的 lease credential。进入作者审核、终态或基础设施重试时
lease 被释放。

Generator 与 Judge 的输出使用 typed result。候选 archive、构建产物、artifact reference 与验证报告
均由 Server 再做确定性校验；模型输出不满足协议时不会用默认值、模糊文本或兼容分支放行。

Generator workspace 由 Server 通过 OpenSandbox 生命周期 API 创建和回收。Generate Worker 可以在
Server 许可的 workspace 中执行生成工具，但不拥有 OpenSandbox 管理凭据，也不直接读取 Server data
目录。

每个 Worker 只知道自己的 Server API key，不知道 PostgreSQL DSN 或 Server data PVC。内部 API 以角色密钥、
Workflow ID、lease credential 与 expected state 共同围栏结果。
