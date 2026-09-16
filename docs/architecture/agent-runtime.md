# Agent Runtime

Breakfix 在 Server 中使用 Eino、PostgreSQL `AgentRun` 记录和 Server-managed OpenSandbox workspace。没有独立 Agent
Deployment、CLI 兼容层或通用 Agent 队列。

## 执行边界

| 角色 | 执行者 | 持久化边界 | 调度方式 |
| --- | --- | --- | --- |
| Authoring | Server | AuthoringSession、消息、AgentRun、私有 Plan stage | 每个会话同时一轮；浏览器消息有稳定幂等键。 |
| Learning Assistant | Server | Assistant 会话、消息、AgentRun、只读证据 | 每个会话同时一轮。 |
| Generator client (web) | 调用 GeneratorService 的 Server Authoring Agent | GenerationWorkflow、PlanRevision、CandidateRevision、workspace | 作者确认后的对话回合。 |
| Generator client (external) | 本机 `breakfix-mcp` | 同一 GeneratorService 契约 | 每次操作一个显式 MCP workspace turn。 |
| Judge | Server | GenerationWorkflow、PlanRevision、CandidateRevision | 独立领取 `Judging` workflow。 |
| materialize、verify、provider cleanup | Runtime Worker | lease-fenced public runnable action | 仅 Runtime Worker；产品发布由 Server finalizer 写入。 |

`AgentRun` 是一次完整、可审计的逻辑执行，可以包含多个模型 HTTP 请求和工具调用；它不是常驻进程或通用队列。每个 typed result 的业务
aggregate 才是权威状态。

## Authoring 回合

一个 Authoring run 有由 `agent.authoring_run_deadline` 配置、默认 30 分钟的 `DeadlineAt`。该期限、单次模型请求超时和模型上下文
窗口是不同限制。临时模型传输失败保留在同一 Eino run 内，并且不会重放已接受的工具结果；永久 executor 错误结束该 run。

浏览器以同一幂等键重发消息时返回已有 run，而不是追加第二条消息或启动第二次模型执行。SSE 断开本身不结束 run。若 deadline 到达、Server
重启或发生永久 executor 错误，一个事务结束 run、丢弃私有 Plan stage 并写入作者可见的确定性事件。下一条作者消息从持久化对话和
workspace 状态继续，不恢复模型内存或半完成工具调用。

## Generator Workspace 恢复

每个 `GenerationWorkflow` 在 workspace 活跃时拥有一个 Server 创建的 OpenSandbox Sandbox 和专用 PVC。Generator turn 必须先取得该
workflow 的单写者 binding，才能读取、写入、执行、归档或提交；短暂的内部 snapshot holder 使用同一栅栏。

snapshotter 每 30 秒并在 turn 释放后归档 `/workspace`，校验摘要并把不可变快照写入 Server data PVC，再原子更新 workflow。它不保存
Sandbox 操作系统、命令进程或工具执行状态。`generator_workspace_idle_ttl` 默认 24 小时；过期 workspace 由异步 reaper 删除 Sandbox
和 PVC。普通 Authoring deadline、未知工具结果和浏览器/MCP 断开保留 workspace；Server 重启则退休未完成 workspace，下一次操作按
最新有效 snapshot、最近 CandidateRevision archive、空 scaffold 的顺序重建。

`submit_candidate` 是 candidate 确定性校验的唯一入口。无效提交没有 CandidateRevision 或状态副作用，workspace 可继续修复；有效提交
将不可变 CandidateRevision 作为下一次重建的后备。

## 启动与权限

HTTP readiness 前，Server 恢复 durable state：中断未完成 Authoring 和 Judge run、退休旧 Generator workspace，并恢复 Catalog
installer、内容 materialization、workspace snapshotter/reaper、学习投影、Assistant lease 和 publication finalizer。模型不会获得
Kubernetes、Incus、Registry、OpenSandbox、PostgreSQL 或 Server data PVC 凭据。网页 Authoring 与 MCP connector 只能操作调用者拥有的、
当前绑定的 workspace；MCP 只转发用户 JWT 并在本机 materialize 内容审核包。

外部副作用先写入业务状态，再由 Runtime Worker 执行。Worker 没有模型 API key、OpenSandbox 凭据、PostgreSQL DSN 或 Server data PVC
访问权限。
