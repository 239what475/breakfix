# TODO: 时间预算的生成回合

P0 的 agent-native authoring 已经落地：网页 Authoring Agent 与本机 `breakfix-mcp` 共用同一个 `GeneratorService`，Judge、Classifier、Build、Artifact Publish、Verify 与发布的边界以 `docs/architecture/` 为准。live 验收同时暴露了这套链路里一个结构性弱点：生成回合把「确认生成 → 写全部文件 → 执行命令 → 提交 candidate」押在一次模型会话上，任何一环失败都让整个 `AgentRun` 从头重建；系统用固定重试次数和固定迭代次数约束这个不稳定过程，结果是大任务被误杀、死循环烧满预算。

本阶段的核心不是给模型加更多重试，而是改变预算的记账方式与失败后的落点：生成回合只有一个时间预算，回合内的模型循环不数次数；时间用尽或技术故障时，回合以 durable 状态结束，作者或 MCP host 的下一个回合从 workspace 及其快照继续，而不是从模型记忆恢复。candidate 的确定性校验仍然只有 `submit_candidate` 一处，作为唯一权威。同时把 E2E 验收的分类靶场改成测试自有的、覆盖各验收场景的内容，不再让测试场景去迁就一份窄而随意的 fixture Roadmap。

## 时间预算的生成回合

一个逻辑 AgentRun 只以 `DeadlineAt` 为上限。Authoring 不再通过 `RetryAuthoringRun` 重建整个 Eino Agent；模型传输重试发生在当前 Eino 回合的模型包装层，已经接受的工具结果仍留在该回合的内存状态中。`attempt` 保留为公共 AgentRun 的审计字段，但模型传输重试不递增它，也不再由数据库的 `<= 5` 约束决定 authoring 是否可继续；`agent_runs.attempt` 与 `authoring_stages.run_attempt` 的上限约束必须一并删除。Judge、Classifier、Roadmap 等短任务自己的有限重试仍由各自应用层决定，不能依赖公共列的上限。

- 单次模型请求超时（当前 5m）保留，它保护的是传输层，与回合时间预算相互独立；一条卡死的流式连接不应占着整段预算。
- 模型请求的 `event.Err`、流式响应 `Recv` 失败、连接超时、HTTP 5xx 和限流属于暂时性模型传输故障。Eino 的 `ModelRetryConfig` 使用 `math.MaxInt` 作为实际无上限的重试计数，并使用其 deadline-aware 指数退避；实际边界只有 `DeadlineAt` 和 Server 进程生命周期。重试判定必须先检查 callback 收到的 context：`ctx.Err() != nil` 时一律不再重试；只有回合 context 仍有效时的单次请求超时才是可重试传输故障。重试停留在当前模型调用，不能重建 Agent、重放已成功返回的工具调用或写一条新的 AgentRun。
- 模型配置、认证、协议解析、不可恢复的请求参数和其他明确的永久性执行器错误不进入无限重试；本轮结束并保留可恢复的 workflow 状态。deadline 与 Server 停止/重启标为 `RunInterrupted`；明确永久的执行器错误标为 `RunFailed`。两种情况都不自动 replacement，下一条用户消息才创建新的 AgentRun。数据库持久化失败只重试持久化，不重新运行 Agent。
- 普通工具错误（命令非零退出、参数校验失败、工具返回 `{"ok":false,"error":"..."}`）是模型可处理的工具结果，不重建 Eino Agent。模型可以据此修正参数、检查状态或结束本轮。
- 对可能产生副作用的工具，错误按工具调用层而不是 HTTP 状态码判断：服务端明确拒绝且能证明请求未执行时，返回普通工具失败；若 HTTP 超时、连接中断或 5xx 使“请求是否已执行”无法确定，工具必须向模型返回结构化的“不确定结果”（至少说明错误和执行状态未知），而不是自动重放或让执行器重建 Agent。此规则适用于命令、文件写入、Plan 更新和所有 Generator 状态变更，不只限于 `run_workspace_command`。未知结果不退休 Sandbox、不丢弃 workspace，也不阻止 Agent 继续执行任意检查或后续命令；模型自行检查文件、进程、服务或其他可观察产出后决定下一步。workspace turn 只隔离不同客户端回合，不是命令进程锁，平台不假定重试安全。模型供应商接口的 5xx 仍属于上一条的模型传输故障，按执行器重试。
- 网页 Authoring 在未知 workspace 工具结果后保留当前 `WorkspaceTurn`，让同一 AgentRun 直接执行检查命令；MCP 的一次工具调用仍可自然结束其短 turn，host 再发起下一次工具调用检查。两条入口都不承诺 Sandbox 内命令进程串行或自动终止。
- 网页 Eino function tool 与 MCP tool 必须共用同一份结果契约：成功、已知失败和执行状态未知是显式、可机器读取的三种结果；连接器不得把未知状态压缩成普通 HTTP 错误文本。外部 Agent 与网页 Agent 因而获得相同的下一步选择，原始 provider/HTTP 细节仍只留在服务端日志。
- Eino 的迭代上限去掉。`eino@v0.9.13` 的 react 循环在每次模型调用前检查剩余迭代数，`MaxIterations <= 0` 会回退到 SDK 默认值 20，不是无上限；因此 authoring agent 传一个足够大的值（如 `math.MaxInt`），使迭代在事实上无上限，只由回合 deadline 的 context 取消和模型给出最终回答来终止，不改 SDK。
- authoring 的最大时间从常量改为可配置（默认 30m）。live 验收用短值走超时路径，不同场景也能有自己的预算。
- 范围先只改 authoring run；Judge/Classifier/Roadmap 保持现有次数上限——它们是短促的单次 typed-result 任务，次数上限便宜且能快速暴露策略死循环。等 authoring 的 live 表现稳定后再决定是否推广。

## 跨回合继续与 workspace 快照

生成回合不必一轮做完。workspace 与反馈是生成回合之间的权威进展：Authoring Agent 可以在一个回合只完成部分工作，回合结束时用持久消息说明已完成与待办；作者下一条消息（或 MCP host 的下一个工具调用）在新回合读取 workspace 并继续，最后提交。这里明确不恢复模型上下文、不重放半完成工具调用——继续的载体是已经落盘的文件，与 Judge 打回后的修复是同一机制，只是从特例变成常态。

回合正常结束时可以用持久消息交代已完成与待办；被 deadline 掐断的回合来不及写总结。Server 托管的回合因 deadline 或不可恢复的执行器故障结束时，Server 向 AuthoringSession 追加一条 event 消息，workflow 保持 `Generating`，不写 `workflow.last_error`——那是 Judge/Verify 反馈的语义。Event 只说明本轮结束及可用的恢复方式，不把尚未提交的 Plan stage 当作已保存进展。客户端托管的 MCP 回合没有对话可写，其 durable 信号就是 workflow 状态与快照，host 靠 `get_generation` 继续。

Event 使用现有 `role = event`，content 是带 `schema_version` 的稳定 JSON，至少包含 `kind`、`run_id`、`reason`、`resumable` 和 `recovery`（`workspace`、`snapshot`、`candidate` 或 `empty`）。`kind` 与 Run 状态一一对应：`RunInterrupted` 写 `authoring_run_interrupted`，`RunFailed` 写 `authoring_run_failed`。`reason` 是闭合枚举：`deadline_exceeded`、`server_stopping`、`server_restarted`、`permanent_executor_error`；不把 provider 名称、HTTP 原文或其他原始错误暴露到 Event 或公开的 `authoring_sessions.last_error`，详细技术诊断留在 Run 的内部错误字段和服务日志。同一个 Run 的 Event ID 由 `run_id` 和 kind 确定性派生，重复恢复不会产生第二条 Event。UI 将 Event 渲染为流程状态消息；构造下一轮 Eino 输入时过滤 Event，只传递 user 和 assistant 消息。MCP 不依赖 Event。

网页发起 Authoring 消息也必须是幂等的。浏览器为一条待发送消息生成稳定的 `idempotency_key`，Server 将它与 session、用户消息和 AgentRun 一同持久化；相同 key 的重复 POST 只能返回同一个已创建 Run，不能写入第二条 user message、创建第二个 stage 或再次执行模型。原请求已经结束时从该 Run 的持久结果恢复响应；仍在运行时，重复请求只得到既有 `run_id` 和进行中状态，前端继续轮询 AuthoringSession。实时 SSE 只服务最初执行该 Run 的连接，不为断连重试引入跨请求的流 fan-out；handler 必须区分「新建 Run」和「已有 receipt」，只有前者能调用 `RunTurn`。MCP 已有每个写操作的 idempotency key，语义也必须对齐。

Authoring 私有 Plan 的 set/add/remove/reorder 工具同样是状态变更，不能例外。每次变更以 `run_id + expected_stage_revision + canonical arguments` 派生确定的 operation identity；数据库为该 operation 保存输入摘要与结果 stage。同一 operation 的重放返回已持久化 stage，不同输入复用同一 identity 返回冲突。若数据库调用的执行状态未知，工具返回结构化未知结果，模型可以选择以完全相同的参数重新发起该工具调用；平台不在背后重放。任何自动生成的 checkpoint ID 必须由该 operation identity 确定，不能让未知后的重试产生另一份 Plan 变更。

同一规则适用于 Generator action receipt：receipt 除 workflow、candidate、proposal 等版本围栏外，还必须保存 canonical request digest。相同 idempotency key 携带不同 payload（尤其是 content/classification feedback）必须冲突，不能静默返回第一次操作的成功结果。

继续已有任务时，Agent 必须先 `list_active_generations` / `get_generation` 找到当前 `Generating` 的 workflow 再操作其 workspace；不得再次 `confirm_generation`、不得再次 `set_generation_plan`。`confirm_generation` 的业务唯一性是 `(source_kind, source_ref, source_revision)`：即使新回合带来新的 transport idempotency key，重复确认同一 Plan revision 也必须返回既有 workflow，不能创建第二个 workflow；工具提示仍应禁止这种无意义的重复调用。

`submit_candidate` 的确定性校验是 candidate 的唯一权威，并且无效提交是无副作用的：不生成 CandidateRevision、不推进状态、workspace 原样保留，`CANDIDATE_INVALID` 作为完整校验报告原样返回给 Agent。因此不新增写入时校验或独立校验工具；系统提示只补充说明这一语义，模型在同一回合内据此修改后重提即可。

时间预算只能覆盖进程存活内的回合边界。Server 崩溃或重启会退休未完成 workspace，未提交文件按现有设计全部丢失。因此把快照作为跨崩溃的恢复载体，按时间而不是按回合触发：

- 新增 Server-owned 的 Generator workspace snapshotter 周期服务（与 workspace reaper 同构），每 30 秒检查每个处于 `Generating` 且拥有 active workspace 的 workflow。它用不暴露给客户端的内部 snapshot holder 尝试取得同一单写者租约；用户回合占用 workspace 或另一个 snapshot 正在运行时，本轮跳过，下一轮补偿。每次用户回合释放单写者后再异步触发一次补拍，因此 30 秒是调度间隔而不是严格的新鲜度保证。回合释放只清空 `idle_since`，不直接开始闲置计时。
- 当前 Server 因 RWO data PVC 明确单副本运行；snapshotter、submit、Run 终止和 workspace 清理仍要通过按 workflow 的数据库锁协调同一进程内的并发操作。snapshot holder 必须一直持有到归档校验和文件 rename 完成；最终事务在 holder 仍占用 workspace 时，同时确认 workflow 仍为 `Generating`、workspace 仍为同一记录，把 digest 写入数据库、设置 `idle_since` 并释放内部 holder。不能在归档完成后先释放再发布；归档或最终事务失败时只释放 holder、不设置新的 `idle_since`，未引用的临时或已 rename 文件交给宽限清理；条件不满足时丢弃本次归档。
- 每次归档在 Sandbox 内使用操作级唯一临时路径，在 Server data PVC 内先写唯一临时文件；不能再使用固定的 `/tmp/breakfix-generator-candidate.tar.gz`。归档完整写入、校验 digest 后，原子 rename 为最终路径。归档必须走与 candidate 相同的 canonical contract：稳定的路径排序、固定的归档元数据、无时间戳的 gzip 流和可保留的文件权限，使未变化的 `/workspace` 得到相同的 digest。
- 快照文件落在 Server data PVC 的确定性路径，`generation_workflows` 记录其 digest（沿用 candidate archive 的「文件在 PVC、digest 在 DB」模式）。
- 如果新归档与当前 digest 相同，则删除临时文件，不创建新的快照版本；digest 基于上述 canonical archive，而不是带随机时间元数据的压缩字节。
- `submit_candidate` 的事务里清空快照 digest，CandidateRevision 成为新权威；旧快照不在提交事务中删除。后台清理器只删除超过宽限期的无引用快照和遗留临时文件，不能删除刚 rename 但尚未完成数据库指针更新的文件。
- 快照只包含 `/workspace` 树（PVC 挂载点），不包含 sandbox 操作系统文件；复用同一 canonical `ArchiveWorkspace` 语义，不单独发明快照格式。不做 candidate 路径白名单过滤——杂项文件是否进入题目由提交校验和 Judge 把关，快照只忠实保存编辑现场。快照归档期间没有其他平台合法的 workspace 工具写者，但它不是进程级 checkpoint：工具超时后已启动的命令可能继续写入 workspace，快照只代表归档时观察到的文件树；恢复后由 Agent 自行检查，平台不自动终止或重放命令。长时间占用单写者的回合仍可能在崩溃时丢失最近修改。
- canonical archive 的创建、校验和恢复由同一个 Go helper 完成，不能让 `ResetWorkspace` 对不受信任的字节直接执行 `tar -xzf`。归档只允许相对路径的普通文件和目录及其权限；拒绝绝对路径、`..` 穿越、符号/硬链接、设备和 FIFO。恢复前先完整校验 archive，再写入空 workspace。
- 恢复时既校验文件 digest，也验证归档可以安全解包。快照无效时在数据库中清空无效 digest，然后回退到最近 CandidateRevision；两者都不存在时创建空 workspace。

正常 deadline 结束的回合保留当前 workspace；Server 崩溃恢复时则退休旧 workspace，下一轮创建新的 Sandbox/PVC，并按 `snapshot > CandidateRevision > empty` 取得 seed。Server 启动恢复不能自动创建或执行新的 Authoring AgentRun，必须等待作者下一条消息。

## workspace 闲置回收

workspace 归属于 `GenerationWorkflow`，不是某个 AgentRun 的子资源。`Authoring AgentRun` 只是一次作者消息驱动的执行回合；它需要操作文件时临时取得 `WorkspaceTurn`，结束时释放该 turn。因而 Authoring AgentRun 的 deadline 不能直接删除 workspace：正常 deadline 后必须保留已写入文件，使下一回合可以继续。

现有 WorkspaceReaper 继续作为唯一的 Sandbox/PVC 删除执行者。本阶段只补齐它的一个前置状态转换，不增加第二套清理服务：

- Server 使用全局 `GENERATOR_WORKSPACE_IDLE_TTL`，默认 `24h`；不提供作者级配置、UI 或关闭开关。
- `generator_workspaces` 增加可空的 `idle_since`。取得 `WorkspaceTurn` 时在同一行锁内清空它；释放 WorkspaceTurn 时也清空它，不写当前时间。只有成功发布一份与当前 workspace 对应的快照后，snapshotter 才在同一协调事务中设置 `idle_since`。网页 Authoring AgentRun 在结束时释放其 turn，MCP 在 `end_workspace_turn` 时释放；没有 workspace 的 Authoring AgentRun 不参与该计时。
- snapshotter 在 turn 释放后优先补拍。它只为 `idle_since` 为空或没有可校验 `workflow.snapshot_digest` 的 workspace 启动归档；已有有效快照且 `idle_since` 非空时只检查 TTL，不重复拍快照。只有 workflow 仍为 `Generating`、workspace 为 `Active`、没有 `active_turn_id`、`idle_since` 已过 TTL，且 `workflow.snapshot_digest` 指向已校验的快照文件时，闲置回收转换才有资格执行。刚释放 turn 后 `idle_since` 为空时，快照成功才会启动计时；快照失败时不启动新的闲置计时，宁可继续保留资源，不能丢失未提交文件。
- 闲置回收与取得 WorkspaceTurn 使用同一 workspace 行锁。取得操作先成功则清空 `idle_since` 并继续复用；闲置回收先成功则仅把 workspace 标为 `Deleting`，不在请求路径同步删除。下一次需要 workspace 的操作创建新的 Sandbox/PVC，并从 `snapshot > CandidateRevision > empty` 恢复。旧资源仍由现有 reaper 异步删除。
- workflow 进入终态或 Server 重启时沿用既有的立即退休逻辑，不等待 idle TTL。闲置回收不会改变 workflow、CandidateRevision 或 AuthoringSession 的状态。

Graceful deadline/永久性执行器故障的「结束 Run + 删除 private stage + 写 Event」使用一个数据库事务，并使用独立于已过期 Agent deadline 的短时持久化 context。进程崩溃无法完成原事务；启动恢复使用同一个幂等终止操作在新事务中补写 `RunInterrupted` 和 Event。浏览器或 MCP 连接断开不等同于 Server 中断，不能因此自动结束 Run。删除 private stage 意味着本轮尚未提交的 Plan 修改丢失，只保留已经落盘的 workspace、快照和持久消息。

## 自洽的 E2E 分类靶场

分类类验收必须有一份 Roadmap 内容作靶子：domain 只能经 Catalog Release bootstrap 进入，测试在运行时没有 API 自己造 Roadmap。真正的问题是这份靶子不对——`test/fixtures/catalog-release/roadmap/` 只有一个很窄的 `platform-runtime` domain 和 node 验证 topic，导致测试场景被迫迁就它：网页 node 场景被改成「运行时初始化标记」才走通分类，k8s 场景没有可挂靠的 domain、`acceptance-k8s` 必然 `unclassifiable`。验收应当测机制，而不是测这份 fixture 的内容形态。

- 把 fixture Roadmap 扩成覆盖三条验收场景的 domain/topic 集合：Linux/Node 方向 domain（如「Linux 系统与网络运维」）与日志归档题目可归属的 topic；k8s 方向 domain（如「Kubernetes 工作负载与服务运维」）与 Deployment/Service 题目可归属的 topic；MCP 的运行时初始化场景继续挂现有 node 方向 topic。
- 网页 node 场景恢复为自然的 Linux 运维题（如日志归档），不再用运行时标记题迁就；`acceptance-k8s` 借此恢复可运行。
- fixture 是测试自有内容，与 `catalog/` 半成品完全解耦；改动只落在 `test/fixtures/catalog-release/roadmap/` 与相关测试场景，分类机制不变。重算 `release.yaml` 的 roadmap `contentRevision`。
- 附带清理：`test/fixtures/roadmap/` 是无人引用的重复 YAML（与 catalog-release 内的 roadmap、`internal/testkit/roadmap` 的 Go fixture 并列为三份不一致内容），确认无引用后删除或标注。

作者侧「申请新 Domain」的通道本阶段不做：domain 由平台按需补充，目前就是改 roadmap source 再 release 的纯内容操作。等第三方作者规模出现后，再升级为显式的 curator 审核门。

## 实现边界与验收

- 生成回合只有一个时间预算：authoring 逻辑 AgentRun 去掉 attempt 上限，Eino 生成回合去掉迭代上限；模型传输故障在 deadline 内持续退避重试，工具不确定结果回传模型自行判断；终止后 workflow 保持 `Generating`，下一回合可继续。
- Server 托管的生成回合终止时以幂等事务写 durable event 消息；Event 使用闭合 reason 枚举且不泄露原始 provider 错误，不进入 Eino 输入；网页消息、私有 Plan 变更和继续已有任务都使用持久化幂等身份，禁止重复创建 Run、重复 Plan 变更、`confirm_generation` 或 `set_generation_plan`，同时 API 以 Plan revision 业务唯一性与 request digest 兜底；`workflow.last_error` 只承载 Judge/Verify 反馈。
- 不恢复模型上下文、不重放半完成工具调用；跨回合继续与崩溃恢复的载体是 workspace 文件和周期快照。Server 重启不自动重跑 Authoring AgentRun。
- `submit_candidate` 仍是 candidate 确定性校验的唯一权威，不新增第二校验面；坏 candidate 的完整错误在同一回合内可被修改后重提。
- 快照只保存 `/workspace` 树，不含 Token、Sandbox ID、PVC、内部地址或系统文件；archive 的创建和解包都经过同一安全的 canonical helper；submit 成功后清空 digest，后台按宽限期清理无引用文件，恢复 seed 优先级为快照 > 最近候选 > 空。
- E2E 分类靶场由 `test/fixtures` 自包含，不与 `catalog/` 课程内容耦合；三条 authoring 验收各自写自然题目，不再迁就单一 topic，k8s 场景不再 `unclassifiable`。
- 单元、集成和可丢弃 E2E 验收覆盖：模型传输故障在 deadline 内持续退避重试、工具超时以“执行状态未知”结果交给模型且不自动重放、快照并发与清理、损坏快照回退、超时后从快照继续、重启后不自动重跑以及三条 authoring live 正向主路径。

## 本阶段：生成回合健壮性与自洽验收靶场

本阶段把前述设计落实为唯一实现，不保留「固定次数约束生成回合」与「验收迁就 fixture 内容形态」的旧路径。完成后：生成回合只由时间预算约束；未完成回合可跨轮继续并在重启后从快照恢复；三条 authoring 验收各自拥有可分类的真实场景。

### 完成定义

- authoring 逻辑 AgentRun 只以 `DeadlineAt` 为上限，attempt 只作审计；authoring 最大时间可配置，默认 30m。
- Eino 生成回合的迭代在事实上无上限，由 deadline 的 context 取消或模型最终回答终止；live 生成回合不再出现 `exceeds max iterations`。
- 时间用尽或技术故障后 workflow 保持 `Generating`；正常 deadline 时 workspace 保留，Server 重启时旧 workspace 退休；作者或 MCP host 的下一回合可以按恢复 seed 继续并 submit，提示与验收都把这当成合法路径。
- Server 托管的回合终止时，AuthoringSession 出现一条幂等 event 消息；Event 不进入下一轮模型输入；继续已有任务时不得再次 `confirm_generation` / `set_generation_plan`，同时重复确认同一 Plan revision 必须由 API 返回既有 workflow，不能产生第二个 workflow。
- 30 秒周期的 Generator workspace snapshotter 对每个 `Generating` workflow 持久化最新快照，并在回合释放后补拍；turn 释放先清空 `idle_since`，只有快照成功发布后才启动闲置计时，未变化的 digest 不刷新已有计时；publish 使用唯一临时路径、digest 条件更新和不可变文件；`submit_candidate` 在同一事务清空 digest，后台按宽限期清理；损坏快照回退到 CandidateRevision。
- Server 启动恢复将旧 Authoring AgentRun 标记为 `RunInterrupted`、删除 private stage 并幂等写入 Event，不自动创建 replacement Run；下一条用户消息才创建新的 AgentRun。
- `Generating` workflow 的空闲 workspace 只有在成功快照后才开始按 Server 固定 `24h` TTL 计时；取得新的 WorkspaceTurn 会清空计时，周期快照不会因 digest 未变化而刷新已有计时。闲置退休与取得 turn 互斥，旧 Sandbox/PVC 仍由现有 reaper 异步删除，下一回合从既定 seed 创建新 workspace。
- `test/fixtures/catalog-release/roadmap/` 覆盖三条验收场景的 domain/topic；从空环境 prepare、验收到 reset，`acceptance-node`、`acceptance-mcp`、`acceptance-k8s` 全部成功。

### 工作项

1. **时间预算与错误分类**：删除 Authoring 的 `RetryAuthoringRun` 重建路径；移除 `agent_runs.attempt` 与 `authoring_stages.run_attempt` 的次数上限，使 `attempt` 仅为审计；authoring 最大时间改为可配置；将模型传输故障、永久性执行器错误、普通工具结果和工具执行状态未知明确分流，并让网页与 MCP 共用结构化工具结果契约；更新 domain/agent 注释，明确各短任务在自身应用层维护有限重试。
2. **去掉迭代上限**：authoring `MaxIterations` 改为无上限大整数，删除/替换相关常量与注释；系统提示补充 `CANDIDATE_INVALID` 是完整校验报告、可同一回合修改后重提，并要求按需读取、避免重复读取大文件（上下文窗口是无上限后的隐性上限）。
3. **跨回合继续与终止 Event**：authoring 提示允许「部分完成 + 交代剩余工作」的合法回合结尾；实现 deadline/永久故障的幂等终止事务、Event 投影与 Eino 输入过滤，以及状态与闭合 reason 的映射；为网页 Authoring message 和私有 Plan 变更增加持久化幂等身份；Server 启动只标记旧 Run 中断，不自动 replacement；提示写死继续时禁止再次 confirm/set_plan，API 以 Plan revision 唯一性和 request digest 兜底；验收 helper 支持跨回合继续。
4. **workspace 快照**：新增 30 秒周期的 snapshotter，对每个 `Generating` workflow 在空闲时归档 `/workspace`，回合释放后补拍；turn 释放只清空 `idle_since`，快照 holder 覆盖归档到 digest 发布的全过程，只有成功快照才启动闲置计时且相同 digest 不刷新已有计时；使用唯一临时路径、canonical archive、digest 条件发布、不可变文件、宽限期清理和损坏回退；archive 创建和恢复共用安全 Go helper；submit 事务里清空 digest；恢复 seed 实现快照 > 最近候选 > 空的优先级。
5. **验收靶场**：扩展 `test/fixtures/catalog-release/roadmap/`（Linux/Node 与 k8s 两个方向）；给 `cmd/catalog-release` 增加 `-print-content-revisions` 并用它重算 `release.yaml` 的 contentRevision；网页 node 场景恢复为日志归档等真实 Linux 题；删除或标注无引用的 `test/fixtures/roadmap/`。
6. **文档同步**：更新 `docs/architecture/agent-runtime.md`、`docs/operations/testing.md` 与 `docs/architecture/catalog-release.md` 中关于重试、迭代、生成回合与 E2E 靶场的叙述。

### 任务清单

- [x] 1.1 删除 Authoring `RetryAuthoringRun` 重建路径；移除公共 AgentRun 和 authoring stage 的 `attempt <= 5` 约束，attempt 退化为审计
- [x] 1.2 authoring 最大时间可配置（默认 30m）
- [x] 1.3 单元测试：模型传输故障在 deadline 内持续退避重试、单次请求超时可重试、整体 run deadline 不再进入重试、配置生效
- [x] 1.4 工具错误语义：命令/参数错误作为工具结果留在同一 AgentRun；副作用工具 HTTP 超时返回“执行状态未知”，不自动重放、不重建 Agent、不退休 workspace；Agent 可继续执行检查命令判断产出
- [x] 1.5 网页 Eino 与 MCP 的工具结果统一为成功、已知失败、执行状态未知三种可读结构；MCP 不再把未知状态压缩为公共错误文本
- [x] 1.6 私有 Plan 变更使用由 run、stage revision 和 canonical arguments 派生的 operation identity；未知结果可由模型显式重放同一 operation，自动生成的 checkpoint ID 仍确定
- [x] 2.1 authoring `MaxIterations` 改无上限大整数
- [x] 2.2 系统提示补充 `CANDIDATE_INVALID` 语义
- [x] 2.3 llm 行为测试：无上限迭代配置与模型重试行为；不对 prompt 文案作单元测试断言
- [x] 2.4 系统提示要求按需读取、避免重复读大文件；记录上下文窗口风险
- [x] 3.1 提示允许「部分完成 + 交代剩余」的回合结尾
- [x] 3.2 终止事务：Run 状态、private stage、Event 在同一事务完成；使用独立持久化 context
- [x] 3.3 Event 确定性 ID、状态与闭合 reason 枚举映射、API/UI 投影与 Eino 输入过滤；终止不能向公开的 `authoring_sessions.last_error` 写原始执行错误；浏览器/MCP 断开不结束 Run
- [x] 3.4 Server 启动将旧 Authoring AgentRun 标记为 `RunInterrupted`，不自动创建 replacement Run
- [x] 3.5 验收 helper 支持跨回合继续
- [x] 3.6 短 deadline 的 live 验收：预算耗尽 → 下一轮继续 → 提交并发布
- [x] 3.7 删除 private stage 后确认未提交 Plan 修改不会伪装成已恢复
- [x] 3.8 网页 Authoring message 的持久化幂等键：重复 POST 返回同一 Run，不重复写用户消息、stage 或执行模型
- [x] 3.9 重复网页 POST 的 SSE 语义：已有运行 Run 只返回 run/status 并由前端轮询；只有新建 Run 的连接执行并接收实时流
- [x] 3.10 generation action receipt 保存 canonical request digest；相同 idempotency key 的不同反馈或其他 payload 返回冲突
- [x] 4.1 新增 30 秒周期的 Generator workspace snapshotter，并在回合释放后补拍；turn 释放只清空 `idle_since`，成功快照才启动闲置计时
- [x] 4.2 快照单写者 holder 覆盖归档、校验和 rename；最终事务在 holder 仍占用 workspace 时发布 digest、设置 `idle_since` 并释放 holder；使用唯一临时路径、canonical archive 和 digest 去重
- [x] 4.3 快照落 PVC + `generation_workflows` 记录 digest；submit 事务清空 digest
- [x] 4.4 后台宽限期清理无引用快照/临时文件；覆盖 snapshot、submit、Run 终止和 reaper 的并发协调
- [x] 4.5 单元/集成测试：周期触发、并发跳过、digest 校验、损坏回退、清理、seed 优先级、重启恢复
- [x] 4.6 canonical archive 安全性：拒绝路径穿越、链接和特殊文件；恢复不再直接调用 shell `tar -xzf`
- [x] 4.7 workspace 闲置回收：增加 `idle_since` 与 Server 固定 `24h` TTL；turn 获取清空计时、释放不直接开始计时，成功快照后才设置且已有有效快照时不重复刷新；turn 获取/释放、快照发布和闲置退休使用同一行锁；覆盖 TTL 到期、活跃 turn 抢占、快照缺失保留、异步删除及从快照重建
- [x] 5.1 扩展 fixture Roadmap：Linux/Node 与 k8s 两个方向的 domain/topic
- [x] 5.2 `cmd/catalog-release` 增加 `-print-content-revisions`，并用它重算写回 `release.yaml`
- [x] 5.3 网页 node 场景恢复为日志归档等真实 Linux 题
- [x] 5.4 删除或标注无引用的 `test/fixtures/roadmap/`
- [x] 5.5 空环境 prepare 后三条 live 验收全部通过
- [x] 6.1 文档同步：agent-runtime、testing、catalog-release

### 提交计划

每完成一个提交都先运行该提交涉及的格式化、生成物一致性和正向测试，再提交。后续提交以之前已提交的边界为基础；不得在工作区积累多个计划项后再集中提交。

本阶段的 PostgreSQL schema 是开发期破坏性基线，不写兼容 migration。每个修改 schema 的提交都必须提升 `currentSchemaVersion`，并在验证前重新创建开发/E2E 数据库；不能把多个不兼容 schema 改动藏在同一个旧版本号下。

1. **`refactor(authoring): bound generation turns by time instead of attempts`**
   - 删除 Authoring 的 `RetryAuthoringRun` 重建路径，并移除 `agent_runs.attempt`、`authoring_stages.run_attempt` 的次数上限；attempt 计数只作审计。仅模型传输层暂时性故障进入无限退避重试；工具结果、工具执行状态未知和持久化失败走各自语义，不重建或重放 Agent。网页 Eino 与 MCP 统一返回成功、已知失败或执行状态未知的结构化工具结果；私有 Plan 变更使用确定 operation identity，未知结果只能由模型显式重放同一 operation。
   - authoring 最大时间从常量改为可配置，默认 30m。
   - 提升开发 schema version，重新创建开发/E2E 数据库。
   - 验证：authoring 单元测试覆盖模型传输故障的 deadline 内持续退避、单次请求超时可重试、整体 run deadline 不进入下一次重试、配置生效，以及普通工具错误和副作用工具超时不触发 Agent 重建、自动重放或 workspace 退休，并允许同一 AgentRun 继续执行检查命令；网页和 MCP 对未知执行状态返回同一语义；私有 Plan 变更的未知结果可确定地由相同 operation 重放；全量 Go 单元测试通过。

2. **`refactor(authoring): remove the Eino iteration cap`**
   - authoring `MaxIterations` 改为无上限大整数，删除/替换相关常量与注释。
   - 系统提示说明 `CANDIDATE_INVALID` 是完整校验报告、可同一回合修改后重提，并要求按需读取、避免重复读取已写入的大文件（无上限后的隐性上限是上下文窗口）。
   - 验证：llm 行为测试覆盖无上限迭代配置和模型重试行为，不对 prompt 文案作断言；全量 Go 单元测试通过。

3. **`refactor(authoring): let generation continue across turns`**
   - 系统提示把「部分完成 + 交代剩余工作」作为合法回合结尾，并写死继续时先找现有 `Generating` workflow、禁止再次 confirm/set_plan。
   - Server 托管回合终止时，在独立持久化 context 中以同一事务完成 Run 终止、private stage 删除和幂等 Event 写入；Event 的 kind/reason 使用闭合映射且不暴露原始 provider 错误，也不进入 Eino 输入。Server 启动只标记旧 Run `RunInterrupted`，不自动创建 replacement Run；浏览器/MCP 断开不结束 Run。网页消息使用持久化幂等键，重复 POST 只返回既有 Run 的状态并轮询，不创建第二个 SSE 执行者；generation action receipt 校验完整 request digest；重复确认同一 Plan revision 必须返回既有 workflow。
   - 验收 helper 支持跨回合继续。
   - 提升开发 schema version，重新创建开发/E2E 数据库。
   - 验证：单元/集成测试覆盖 Event 确定性幂等、UI/API 投影、Eino 输入过滤、stage 丢失语义、网页重复 POST 不重复执行且不启动第二个 SSE executor、action receipt payload 冲突，以及 Server 重启不自动重跑；全量 Go 单元测试通过；live 验收在提交 4 后一起执行。

4. **`feat(generation): snapshot and retire idle workspaces`**
   - 新增 30 秒周期的 Generator workspace snapshotter，空闲时取得内部单写者 holder，回合释放后补拍；turn 释放只清空 `idle_since`，最终事务在 holder 仍占用 workspace 时发布 digest、设置 `idle_since` 并释放 holder；已有有效快照且计时中的 workspace 不重复拍快照。使用唯一临时路径、与 candidate 共用的 canonical archive、不可变快照文件、digest 条件发布、去重、宽限期清理和损坏回退。archive 创建和解包由同一安全 Go helper 完成；submit 事务里清空 digest；恢复 seed 实现快照 > 最近候选 > 空的优先级。
   - 增加 `idle_since` 与全局 `GENERATOR_WORKSPACE_IDLE_TTL`（默认 `24h`）：只有持有有效快照、无 active turn 的 `Generating` workspace 才可在 TTL 到期后转为 `Deleting`；取得 turn 与退休使用同一行锁，实际 Sandbox/PVC 删除仍完全由现有 reaper 异步执行。
   - 提升开发 schema version，重新创建开发/E2E 数据库；不提供旧 schema 兼容。
   - 验证：单元/集成测试覆盖周期触发、并发跳过、canonical digest 去重、条件发布、宽限期清理、损坏回退、seed 优先级、安全 archive 解包，以及 snapshot、submit、Run 终止、闲置 TTL、turn 抢占和 reaper 的并发协调；全量 Go 单元测试通过；随后用短 deadline 配置执行「超时 → 继续 → 发布」live 验收。

5. **`test(fixtures): give acceptance a self-contained classification roadmap`**
   - 扩展 `test/fixtures/catalog-release/roadmap/`：Linux/Node 与 k8s 两个方向的 domain/topic。
   - 给 `cmd/catalog-release` 增加 `-print-content-revisions`，用它重算并写回 `release.yaml` 的 contentRevision。
   - 网页 node 场景恢复为真实 Linux 题；删除或标注无引用的 `test/fixtures/roadmap/`。
   - 验证：生成物一致性与全量 Go 单元测试通过；空环境 prepare 后 `acceptance-node`、`acceptance-mcp`、`acceptance-k8s` 全部成功。

6. **`docs(authoring): document time-bounded generation and recovery`**
   - 更新 agent-runtime、testing、catalog-release 文档，只描述时间预算的生成回合、workspace 快照恢复与自洽的 E2E 靶场。
   - 验证：文档链接与格式检查、OpenAPI 生成物一致性、全量 Go 单元测试以及已建立的 live 验收通过。
