# 工作流

Breakfix 有两类 durable 后台流程：作者发起的 `GenerationWorkflow` 和空平台启动时的 `CatalogRelease`。它们都由 PostgreSQL
保存状态、lease 和阶段结果，不按字符串队列或内存 worklist 恢复。学习 Environment 和 Authoring/Assistant 对话有各自的生命周期，
不属于这两类流程。

## GenerationWorkflow

作者明确确认一个 Plan revision 后创建 workflow：

```text
Generating -> Judging -> Building -> ArtifactPublishing -> Verifying
    ^                                                        |
    |                    candidate 不通过                    v
    +------------------------------------------------ NeedsAuthorReview
                                                               |
                                                 作者确认内容 -> ScenarioPublishing
                                                               |
                                      Runtime Worker promotion + Server finalizer
                                                               v
                                                           Published
```

`Generating` 只由作者请求的网页或 MCP workspace 操作推进；`Judging` 由 Server 的 Judge Agent 执行；Build、artifact publish、
Verify 和 ScenarioPublishing 由 Runtime Worker 执行。`NeedsAuthorReview` 是唯一的作者审核状态。作者确认当前 verified candidate 后，
workflow 直接进入 `ScenarioPublishing`，不存在分类、课程归属或额外审核状态。

每个 Runtime state 都有 Server 管理的 `runtime_attempt`，最多五次基础设施尝试。artifact 语义错误返回 `Generating` 并留下可见反馈；
第五次基础设施失败、确定性发布错误或显式取消进入终态。同一 state 的 lease 接管保留
`workflow_id + candidate_revision_id + state + state_version` 外部 identity，不包含 `runtime_attempt`。

`ScenarioPublishing` 的 Worker promotion 成功后，Server publication finalizer 校验 immutable artifact、materialize 场景 source，
并在同一数据库事务中写入 stable Scenario、其 active revision、candidate 发布事实、作者会话发布状态与 workflow `Published`。finalizer
的确定性错误使 workflow 失败；瞬时错误保留 `ScenarioPublishing` 并使用持久化诊断和下一次重试时间。旧 revision、artifact、
Environment 与学习记录从不被 active pointer 覆盖。

弃用不属于 Generation state。生命周期 API 只将 stable Scenario 标为 `deprecated`，阻止新 Environment；历史内容不删除。新 Environment
总是解析 active revision，已有 Environment 永远按创建时保存的 revision 读取。

## Catalog Release

Catalog Release 不是 `GenerationWorkflow` 的 source variant，也不会创建 Authoring Session、Generator turn 或 Agent 调用：

```text
Pending -> Installing -> Committing -> Ready | Failed
```

Server stage immutable source 后，Runtime Worker 依次执行每个 Entry 的 Build、artifact publish 与真实 Verify。全部 Entry 成功后，
Server 创建 commit intent，Worker 执行最终 artifact promotion，Server materialize source 并在一个事务中公开所有 stable Scenario 和
active revision。相同 digest 的重启或 lease 接管只恢复未完成阶段，不能重复分配 scenario identity 或重复已成功 promotion。

配置的首次 release 未 `Ready` 时，应用层阻塞 Catalog 读取以及作者生成和发布，但不会使 `/readyz` 失败。完成后，新增与修订内容只走
作者流程；不同 digest 不构成第二次安装。完整 portable 契约见 [Catalog Release](catalog-release.md)。

## 运行与恢复

每个 Runtime Worker 进程独立运行 Action Loop 与 Reaper Loop：前者一次领取 Build、artifact publish、Verify 或 ScenarioPublishing
action，后者只领取已有的幂等资源清理。Server 在 Generation 与 Catalog 之间轮换首选 claim，避免一个持续积压的队列饿死另一侧。

Server 启动先恢复已持久化的 finalizer、Catalog installer、Generator workspace、AgentRun、学习投影和 Assistant lease，再接受 HTTP。
停止时先停止接收 HTTP 请求，再取消并等待服务，最后关闭 Incus 与 PostgreSQL。没有第二套恢复队列，也不会把日志当作恢复权威。
