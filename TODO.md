# TODO: Agent-native Challenge Authoring

Breakfix 的作者不应为了沉淀刚刚解决的真实运维问题而离开 Codex、Claude Code、Qoder 等日常 Agent，重新在网页中描述一遍上下文。网页中的 Authoring Agent 和用户日常使用的外部 Agent 都应直接成为题目的 Generator；Breakfix 继续负责隔离工作区、质量门禁、真实验证、分类和发布。

这项设计的核心不是 MCP 协议本身，而是一份共享的 Generator 工具面：网页 Authoring Agent 直接调用它，外部 Agent 通过 MCP 调用它。两者产生完全相同的 candidate 和后续生命周期。

## 统一角色与边界

```text
网页作者                                      外部作者
  Browser                              Codex / Claude Code / Qoder
     | HTTPS                                      | stdio MCP
     v                                            v
Breakfix Server                           本机 breakfix-mcp 连接器
  Authoring Agent (Eino)                         | HTTPS + user token
     | 直接 function tools                        v
     +----------------------> GeneratorService <---+
                                  - workflow / candidate
                                  - OpenSandbox workspace
                                  - 状态、版本、所有权与审计
                                             |
                                             v
                    Judge、Classifier（Server 内部 Eino Agent）
                                             |
                                             v
                    Runtime Worker
                      Build、Artifact Publish、Verify、Challenge Publish、回收
```

`breakfix-mcp` 在用户机器上作为标准 stdio MCP Server 被 Codex 等 MCP Host 启动；同时它是远程 Breakfix Server 的认证客户端。这个双重角色是有意的：本地文件副作用由我们可控的连接器实现，而不是由模型、Codex 自带 MCP Client 或远程 Server 自行决定。

远程 Breakfix Server 永远不写调用机器的 `/tmp`。第三方 Agent 也没有“写审核目录”的工具。Server 是权威状态源，连接器只是将某一不可变审核快照投影到本地。

Generator client 包括两种入口：

- **网页 Authoring Agent**：先与用户讨论并持久化 Plan；用户直接在对话中明确确认某个 Plan revision 后，Authoring Agent 通过同一组 Generator function tools 创建任务、操作 workspace 并提交 candidate。
- **外部 Agent**：利用当前真实运维上下文，通过 `breakfix-mcp` 创建任务、操作同一类 workspace、读取反馈并在用户直接要求后修复或确认审核决定。

两种 Generator client 都不能自行确认内容审核、确认分类审核或发布题目；所有决定都必须先由用户在对应 Agent 对话中对当前版本明确授权。网页 Authoring Agent 也不拥有本机审核目录的写权限。

Breakfix 负责：

- 创建并持久化 `AuthoringSession`、`PlanRevision`、`GenerationWorkflow`、candidate 和审核结果。
- 创建、隔离和回收 OpenSandbox workspace；限制文件路径、命令工作目录和可用工具。
- 执行内部 Judge、Build、Artifact Publish、真实 Verify、Classifier、Roadmap 更新和发布。
- 校验每次操作的用户所有权、workflow 状态和版本，处理断线、重启和恢复。

Generator client 不获得 Kubernetes、Incus、OpenSandbox、Registry、PostgreSQL 或模型凭据，也不能绕过 Judge、Verify 或显式发布直接写入 Catalog。

## 统一对话确认协议

网页和 MCP 不使用两套“按钮确认”与“自然语言确认”语义。Agent 展示当前版本，用户直接在对话中表达决定，随后 Agent 调用版本绑定的操作工具；不创建额外的确认请求工具，也不保存待确认记录。两端唯一的差异是审核内容在网页中展示，还是被 `breakfix-mcp` 投影到本机目录。

1. Agent 展示当前不可变 Plan、candidate 或分类 proposal，并说明将要执行的动作。
2. 用户直接在与该 Agent 的对话中确认、要求修改或取消。
3. Agent 调用相应的 `confirm_generation`、`confirm_content`、`request_content_changes`、`confirm_classification_and_publish`、`request_classification_changes` 或 `cancel_generation` 工具，并携带对象版本与请求幂等键。
4. Server 原子校验用户所有权、当前状态和版本；成功后执行状态转换。重复请求返回同一结果，版本已变化的请求被拒绝，客户端重新读取最新版本。

网页端由 Server 持久化完整对话和工具调用。外部 MCP 场景中，Server 看不到 Codex 等 Host 内的原始对话，只能将持有用户 Token 且通过版本校验的 MCP 工具调用视为外部 Agent 代表用户执行的决定。这是外部 Agent 代表用户调用有副作用工具的固有信任边界，不额外伪造网页确认码或要求用户重复操作。

## 统一生成生命周期

网页路径不再先创建 workflow、再由后台独立 Generator Agent 领取 `Generating`。确认消息对应的、仍属于同一 `AuthoringSession` 的 Authoring AgentRun 是生成回合，不是第二种 Generator 角色。流程是：

```text
用户与网页 Authoring Agent 讨论
  -> 持久化 Plan revision
  -> 用户直接在对话中明确确认该 revision
  -> 确认消息对应的 Authoring AgentRun 调用 confirm_generation(plan revision)
  -> GeneratorService 原子创建 workflow 和 workspace
  -> 同一 AgentRun 调用 workspace tools 并 submit_candidate
  -> AgentRun 正常结束
```

`confirm_generation` 是创建 workflow 的唯一对外工具；`GeneratorService` 内部才执行创建操作，客户端不存在绕过对话确认直接创建 workflow 的 `create_generation` 工具。调用使用稳定的请求幂等键：Server 若在创建过程中崩溃，重试同一请求只会得到一个 workflow；若请求尚未形成 workflow，用户可以在对话中再次确认。提交 candidate 后，Authoring AgentRun 不等待 Judge、Build 或 Verify，它们由 `GenerationWorkflow` 的异步状态机继续推进。

外部路径使用相同的 GeneratorService：外部 Agent 先通过 `set_generation_plan` 保存结构化 Plan，Server 原子创建或更新最小的 `AuthoringSession` 并冻结新的 `PlanRevision`；用户在对话中确认后，外部 Agent 调用 `confirm_generation`。后续 workspace 与 candidate 工具不再区分来源。网页与外部 Agent 都可以通过明确的 workflow ID 恢复同一用户拥有的 `Generating` workflow，便于用户在两种入口之间自然切换；服务不会按“最近一个任务”自动挑选 workspace。

```text
Generating
  -> submit_candidate -> Judging（Server 内部 Eino Agent）
  -> Building -> ArtifactPublishing -> Verifying（Runtime Worker）
  -> NeedsAuthorReview
  -> Classifying（Server 内部 Eino Agent）
  -> NeedsClassificationReview
  -> ChallengePublishing -> Published

Judging / Build / ArtifactPublishing / Verifying 的 candidate 问题
  -> Generating（保留 candidate、反馈和 workspace，等待用户要求某个 Generator client 修复）

NeedsAuthorReview -> request_content_changes -> Generating
```

`GenerationWorkflow` 是“一次用户确认的题目生成任务”从 Plan、candidate、异步质量门禁、作者审核、分类到发布的唯一持久化业务边界。它可以包含多次网页或 MCP AgentRun、多个不可变 CandidateRevision 和多轮修复，但不是一次模型调用、一次聊天会话或某个客户端进程。workflow 不保存 `generator_driver = server | mcp` 一类的执行者字段，也没有后台 Generator AgentRunner 自动领取 `Generating`；网页生成回合或外部 MCP 调用的来源只作为审计事件记录，不改变状态机、数据模型或 workspace 所有权。

`Generating` 的含义是“用户拥有的 candidate workspace 可被当前 Generator client 修改，等待 `submit_candidate`”，而不是“某个 Server Agent 正在后台生成”。Judge 和 Classifier 仍各自运行内部 `AgentRun`；外部 MCP 调用与网页 workspace 工具调用都不是伪造的 Generator `AgentRun`。`submit_candidate` 是生成阶段唯一的持久化边界，Server 在提交时生成独立的 `CandidateRevision` ID；该 ID 不从 `GeneratorRunID` 或客户端会话 ID 派生。重复请求必须按 submission idempotency key 返回同一 CandidateRevision。

## Workspace 与恢复

`GeneratorService` 是共享应用服务，直接供网页 Eino function tools 调用，并由 HTTPS API 供 `breakfix-mcp` 调用。共享的业务能力只有：创建或读取 workflow、列出/读取/写入 workspace 文件、执行受限命令、归档并提交 candidate，以及读取状态与反馈。

- 只有 `Generating` workflow 可以操作 workspace。Server 按 workflow 串行化写文件、命令、归档提交和取消，避免网页与 MCP client 同时修改同一个 workspace。
- 一个 `Generating` workflow 同时只绑定一个明确的 Generator 回合。网页回合是一个 Authoring AgentRun；MCP 回合由 `breakfix-mcp` 在首次 workspace 操作时显式开始，并在完成后显式结束。Server 拒绝其他回合对同一 workspace 的并发写入。回合因请求失败或连接中断而未正常结束时，Server 结束该回合并释放 binding；不引入独立租约表或基于“闲置”的超时规则。workflow 和 Sandbox 不因 binding 被释放而取消。
- Judge、Build 或 Verify 打回，以及作者请求内容修改时，保留当前 workspace；下一个用户请求可让网页或外部 Agent 在同一 workspace 修复并再次提交。
- 单次工具超时、连接中断或普通 Generator AgentRun 失败不是 Server 崩溃：workflow 保持 `Generating`，workspace 保留，后续 AgentRun 可以从最近的持久化边界继续。
- MCP 连接器退出、MCP Host 断开或本机审核目录被删除，不取消 workflow 或 workspace。
- Server 崩溃或重启时，所有未完成 Generator workspace 都失效；后台 reaper 异步删除旧 Sandbox/PVC。下次 Generator client 操作时直接创建新的 Sandbox 和新的 PVC，不等待旧资源删除，也不复用旧 binding，并从最近已提交 CandidateRevision 或初始 Plan scaffold 重建。未提交的文件、工具执行结果和半完成命令不恢复、不重放。

因此不需要第二套 Generator worklist、Agent pool 或 Sandbox 类型。Runtime Worker 仍负责资源密集或异步的 Build、Artifact Publish、Verify、Challenge Publish 和后台回收；网页和 MCP 只是同一 GeneratorService 的两个薄适配器。

## 本地审核投影

网页的“概览、检查点、Assets、Diff、验证、Topic、Tags”只是同一权威审核数据的多个视图。MCP 路径不复制网页 Tab，而是由 `breakfix-mcp` 将 Server 返回的不可变审核包物化为本机可浏览的目录。

Linux 默认目录为：

```text
/tmp/breakfix/reviews/<workflow-id>/
  content/<candidate-revision-id>/
    manifest.json
    overview.md
    checkpoints/
      <checkpoint-id>.md
    candidate/
      problem.md
      ... candidate assets ...
    diff/
      <path>.diff
    judge.md
    verification.md
  classification/<candidate-revision-id>-<proposal-revision>/
    manifest.json
    topic.md
    tags.md
```

实现使用操作系统临时目录根目录；在一般 Linux 环境中该目录就是 `/tmp/breakfix/reviews`。它是可丢弃缓存，删除或机器重启后可通过连接器从 Server 重新同步，绝不参与 workflow 恢复或成为业务权威。

每次 `get_generation`、`wait_generation` 或显式 `sync_review` 发现 workflow 处于 `NeedsAuthorReview` 或 `NeedsClassificationReview` 时，连接器执行以下固定动作：

1. 向 Server 请求与当前 candidate/proposal revision 绑定的审核包。
2. 在目标目录同级临时目录完整写入并校验 `manifest.json` 中的内容摘要。
3. 原子 rename 为上述 revision 目录；已有旧 revision 目录不覆盖。
4. 向 MCP Host 返回只读 `review_path`、审核种类和对应 revision。

`manifest.json` 至少包含 schema version、workflow ID、candidate revision ID、candidate archive digest、分类 proposal revision（如适用）、workflow state 和导出时间；绝不写入 Token、Sandbox ID、环境凭据或内部地址。

本地审核目录是**只读审阅投影，不是编辑输入**。用户修改其中的文件不会改变 Server 状态，连接器也不会把本地目录自动上传。需要改内容时，用户让 Agent 调用 `request_content_changes` 或根据反馈继续操作远程 workspace；需要改分类时，调用 `request_classification_changes`。这样本地文件不成为不透明的第二个 candidate 来源。

用户可以完全不打开网页，在本机编辑器、终端或文件浏览器中审阅目录。网页和本地目录都只展示 Server 的同一 revision；两者之间不存在同步冲突。

## 审核决定

内容审核与分类审核是两个明确阶段：

```text
NeedsAuthorReview
  -> breakfix-mcp materializes content/<candidate-revision-id>/
  -> 用户审阅
  -> confirm_content | request_content_changes

NeedsClassificationReview
  -> breakfix-mcp materializes classification/<candidate-revision-id>-<proposal-revision>/
  -> 用户审阅
  -> confirm_classification_and_publish | request_classification_changes
```

内容审核与分类审核同样通过 Agent 对话确认，不通过网页按钮完成。所有确认或调整工具都必须携带本地 `manifest.json` 对应的 `workflow_id`、`candidate_revision_id` 和分类时的 `proposal_revision`，以及请求幂等键。Server 只接受当前状态、当前版本和当前用户的决定；如果审核期间 candidate 或 proposal 已更新，旧快照的确认被拒绝，连接器重新物化新目录。

任何入口都不能从技术上证明用户是否真的阅读了审核内容。第一版把版本绑定的对话指示视为“用户授权 Agent 代办”的动作：网页端记录用户消息与工具调用，MCP 端记录调用身份、审核对象和决定。若未来需要强制人类确认，必须额外设计独立的人机确认通道，不能把一个 Agent 自己也能读取的确认码误当作人类证明。

## 共享工具面与 MCP 映射

`GeneratorService` 的 Generator 工具面由网页 Authoring Agent 直接作为 Eino function tools 使用，也由 `breakfix-mcp` 映射为同名 MCP tools：

- `set_generation_plan`、`confirm_generation`：保存或修订结构化 Plan；用户在对话中确认当前 Plan revision 后，`confirm_generation` 创建用户私有 workflow 和 workspace。
- `list_active_generations`、`get_generation`：列出或读取用户拥有的未完成任务、Plan、candidate、反馈和下一步动作。
- `list_workspace_files`、`read_workspace_file`、`write_workspace_file`、`run_workspace_command`：操作明确指定 workflow 的远程 Generator workspace。
- `submit_candidate`：归档当前 workspace，做确定性 candidate 校验并进入 Judge。
- `wait_generation`：有界等待状态变化。MCP 到达审核状态时额外同步本地 review bundle；网页直接读取同一份 Server 数据。

下面的用户控制动作仍由 Agent 在取得用户当前对话中的明确指示后调用；它们不接受本地文件作为编辑输入：

- `sync_review`：MCP 重新下载当前不可变审核包，返回本地路径。
- `confirm_content`、`request_content_changes`：内容审核决定。
- `get_classification`、`confirm_classification_and_publish`、`request_classification_changes`：读取 Server Classifier Agent 的分类 proposal、提交用户修改意见，或确认分类并发布。客户端不自行计算或写入 topic/tag 关系。
- `cancel_generation`：显式取消，后台回收 workspace。

工具返回结构化状态和路径，不包含内部 Prompt，不拼出“一键生成并发布”工具，也不让模型拥有审核目录的写权限。

## 认证与安全

`breakfix-mcp` 从本机安全配置读取用户 Token，通过 HTTPS 调用远程 Breakfix：

```text
Authorization: Bearer <user token>
```

MVP 可以复用现有 Bearer 身份模型；长期应提供用户级、可过期、可撤销的 Personal Access Token。Server 在每一次 workspace、workflow、审核和发布操作中校验所有权，而不是相信连接器传来的路径或状态。

Token 不进入 Agent 对话、审核目录、日志、`manifest.json` 或 candidate。`breakfix-mcp` 需要防止子进程环境、错误信息和诊断输出泄漏 Token。远程连接必须使用 HTTPS。

初期不新增管理员模型、API 配额或独立 OAuth 平台。发布始终是单独、显式、版本绑定的调用。

## 实现边界与验收

- 向 Codex 等 MCP Host 暴露的是本机 `breakfix-mcp` stdio Server；它经认证 HTTPS 调用 Breakfix application API。远程平台不需要也不得直接写客户端文件系统。
- `breakfix-mcp` 负责审核包下载、摘要校验、原子落盘和本地路径返回；所有业务状态、candidate 和审核决定仍只在 Server 中保存。
- 网页 Authoring Agent 与外部 MCP Agent 复用同一 GeneratorService、workspace/sandbox 受限能力和后续状态机；不新增第二套 worklist、Agent pool 或 Sandbox 类型，继续复用现有 Runtime Worker。
- 首个端到端验收必须覆盖两条入口：网页 Authoring Agent 在对话确认后创建任务、操作 workspace、提交 candidate；外部 Agent 完成相同的对话确认和生成路径；两者都覆盖 Judge 打回、修复并重提、真实 Build/Verify、内容审核、分类审核和显式发布。MCP 路径额外覆盖审核包本地落盘。
- 还必须覆盖：连接器退出或本地 `/tmp` 删除后重新同步；同一快照重复同步的幂等性；旧 revision 审核被拒绝；不同用户 Token 访问被拒绝；审核目录中没有 Token 或底层环境凭据。
- 对外表述为 “Agent-native challenge authoring”。MCP 是互操作协议，不单独宣称为创新；创新点是外部 Agent 的现场生成能力和平台质量门禁的组合。

## P0：统一 Generator 与 MCP Authoring

本阶段把本文前述设计落实为唯一实现，不保留旧的“网页按钮创建 workflow，后台 Generator AgentRunner 领取 `Generating`”路径。完成后，网页 Authoring Agent 与 `breakfix-mcp` 是同一 `GeneratorService` 的两个客户端；Runtime Worker 继续只消费 Build、Artifact Publish、Verify、Challenge Publish 与清理动作。

### 完成定义

- 一次用户确认的 Plan revision 只创建一个 `GenerationWorkflow`；同一请求幂等键只返回该 workflow。`GenerationWorkflow` 可包含多轮 Generator 回合和多个不可变 `CandidateRevision`，但不依赖 `GeneratorRunID` 作为 candidate identity。
- `Generating` 只表示用户可继续操作远程 workspace。没有后台 Generator AgentRunner 领取该状态；Judge 和 Classifier 仍由 Server 内部 Eino Agent 异步执行。
- 每个 workspace 操作都归属一个明确的 Generator 回合，同一 workflow 同时只能有一个回合写入。普通工具/回合失败释放 binding 并保留 workspace；Server 重启退休旧 workspace，后台删除旧 Sandbox/PVC，下一回合创建新的 Sandbox/PVC 并由最近 CandidateRevision 或 Plan scaffold 重建。
- 网页端只通过 Authoring Agent 的 function tools 推进生成、内容审核、分类调整、分类确认发布或取消；删除承担授权职责的网页操作按钮和旧直接 lifecycle API。
- 外部 Agent 使用本机 `breakfix-mcp` stdio connector，通过 JWT Bearer HTTPS 调用与网页相同的 GeneratorService；连接器将不可变审核包校验后原子投影到系统临时目录，不上传或接受该目录的编辑。
- 分类只由 Server 内部 Classifier Agent 根据固定 RoadmapRevision 提出 proposal。网页/MCP 只能读取 proposal、提交用户反馈并确认发布，不能自己写入 topic、tag 或 Roadmap 关系。
- 单元、集成和可丢弃 E2E 验收覆盖两条入口的正向主路径；不为已删除设计保留迁移兼容或反向测试。

### 迁移清单

1. 用 Server-owned `GeneratorService` 取代旧 Generator AgentRunner 的 `Generating` 执行、claim、lease 和 `GeneratorRunID` lineage；移除“一个 AuthoringSession 只能有一个未终态 workflow”的旧约束，使每次确认的 Plan revision 自然拥有独立 workflow；保留 Judge/Classifier 的 Server AgentRun 与 Runtime Worker 的 action loop。
2. 收敛 candidate、workflow、workspace 和提交幂等模型：CandidateRevision 由 Server 提交时分配，workspace binding 属于 Generator 回合，Server 重启只退休 workspace，不恢复或重放半完成工具调用。
3. 将 Authoring Eino tools 从“只编辑 Plan，提示点击按钮”改为共享 Generator tools；作者对话中的确认自然调用版本绑定操作。
4. 用新的公开 HTTP application API 供 `breakfix-mcp` 访问；网页只保留会话、对话和只读审核视图所需 HTTP/SSE，不再保留旧的 `/generate`、`/classify`、`/classification-feedback`、`/publish` 按钮 API。
5. 新增本机 `breakfix-mcp` stdio connector 和审核包导出/物化能力。远程 Server 只返回 bytes 与 manifest，不写本机文件；连接器负责校验、临时目录写入和原子 rename。
6. 将系统架构、Agent Runtime、工作流、API 合约、部署与测试文档改为新边界；删除旧 Generator Runner、旧 API、旧前端按钮、旧 prompt 和仅服务于它们的测试。

### P0 提交计划

每完成一个提交都先运行该提交涉及的格式化、生成物一致性和正向测试，再提交。后续提交以之前已提交的边界为基础；不得在工作区积累多个计划项后再集中提交。

1. **`refactor(generation): introduce the shared generator service`**
   - 建立 `GeneratorService` 的应用接口和领域输入/输出：Plan revision 确认、workflow 查询、workspace 回合开始/结束、文件/命令操作、candidate 提交、内容决定、分类反馈、发布与取消。
   - 将 CandidateRevision ID 改为 Server 在 `submit_candidate` 时分配；删除 `generator_run_id` 列、`IDForGeneratorRun`、唯一约束和所有 API 投影中的 lineage 暴露。移除每个 AuthoringSession 只有一个未终态 workflow 的索引；Plan revision 与 workflow 的绑定保持不可变。用提交幂等键保存同一请求的结果，并提升开发数据库 schema version，不提供旧 schema 兼容。
   - 将 workspace 绑定写入 workflow/workspace 权威状态，而不是依赖 Generator claim；定义回合结束、普通失败释放、Server 重启退休和异步 reaper 的状态转换。
   - 验证：领域状态机与 PostgreSQL 集成测试覆盖 Plan 确认幂等、同一 session 的独立 workflow、连续 CandidateRevision、单 workspace writer、普通失败保留、Server 重启后新 workspace 以及异步清理；全量 Go 单元测试通过。

2. **`refactor(server): remove background generator execution`**
   - 删除 `Generation AgentRunner` 对 `Generating` 的 claim/lease/恢复/执行及旧 Generator executor、workspace claim runtime 和对应 bootstrap lifecycle 服务。
   - 保留并收敛 Judge/Classifier 的 Server AgentRun 调度，使其只在 `Judging`/`Classifying` 状态执行；Runtime Worker 的 Build、Artifact Publish、Verify、Challenge Publish 和 reaper 不变。
   - Server 启动恢复只中断 Judge/Classifier 等未完成内部 AgentRun，并退休未完成 Generator workspace；不恢复模型上下文、工具结果或半完成 Generator 回合。
   - 验证：Server lifecycle、Judge/Classifier 调度和 workspace recovery 正向测试；确认 Runtime Worker 仍能领取并完成 runtime action；全量 Go 单元测试通过。

3. **`refactor(authoring): drive generation through agent tools`**
   - 将网页 Authoring Agent 的 function tools 绑定到 `GeneratorService`，支持保存 Plan、确认生成、workspace 操作、提交 candidate、读取反馈与当前 workflow。
   - 重写 Authoring prompt，使其只说明自身的工具、版本和用户对话确认语义；移除“点击生成并验证”、自动后台 Generator 修复等旧指令和渲染断言。
   - 将内容确认、分类反馈、分类确认发布和取消也改为 Authoring Agent 在用户消息后调用的版本绑定工具；分类 proposal 仍只由 Classifier 生成。
   - 验证：Authoring service/Eino tool 正向契约测试覆盖 Plan -> 用户确认 -> workflow -> workspace -> submit，及审核决定通过 Agent tool 推进状态。全量 Go 单元测试通过。

4. **`refactor(httpapi): expose generator application contracts`**
   - 重写 OpenAPI 与 HTTP handler：提供面向 GeneratorService 的 JWT 保护 application API，以及网页会话/SSE/只读审核读取接口；所有有副作用请求携带 workflow/candidate/proposal revision 与幂等键。
   - 删除旧按钮生命周期路由、旧前端 client 调用和旧 API schema 字段；重新生成 Go 和 TypeScript OpenAPI 产物。
   - 网页改为对话驱动：移除生成、内容确认、分类发布等授权按钮，保留审核 Tab 的只读展示和正常聊天输入。
   - 网页审核将 Topic 与 Tags 拆为两个独立只读 Markdown Tab，显式区分已有定义与候选新增定义；不让 Tab 中的按钮承担授权职责。
   - 验证：HTTP 正向契约测试覆盖所有权、版本绑定、重复幂等请求和审核读取；OpenAPI 生成物一致性、前端构建与全量 Go 单元测试通过。

5. **`feat(mcp): add the local breakfix-mcp connector`**
   - 新建独立 `cmd/breakfix-mcp` stdio MCP Server，读取本机配置中的 Server URL 与用户 JWT，并把共享 GeneratorService HTTP API 映射为 MCP tools。
   - 实现审核包 API 与本地只读投影：连接器下载当前不可变 content/classification bundle，校验 manifest 摘要，在系统临时目录下写入临时目录后原子 rename，并只返回 `review_path` 与 revision。
   - 不让 Token、Sandbox ID、PVC、内部地址或凭据进入 manifest、审核目录、工具结果和日志；不让连接器把本地审核目录作为候选输入上传。
   - 验证：connector 单元/集成测试覆盖 stdio tool 调用、JWT 透传、审核包完整性、重复同步、删除本地目录后的重新同步与敏感字段排除。全量 Go 单元测试通过。

6. **`test(e2e): accept web and mcp authoring paths`**
   - 扩展可丢弃 E2E 基线，分别从网页 Authoring Agent 与本机 MCP connector 走同一 GeneratorService 主路径；二者均覆盖 candidate 提交、Judge 打回后的修复、真实 Build/Verify、内容审核、分类审核和显式发布。
   - MCP 验收额外检查审核包本地投影；网页验收检查没有承担授权职责的 lifecycle 按钮，并通过对话推动相同决定。
   - 删除只断言旧 `/generate` 或后台 Generator Runner 行为的 E2E/agent-live 测试；保留与新契约直接对应的正向验收。
   - 验证：新旧可丢弃 Kind 目标从空环境 prepare、验收到 reset 均成功，且全量单元、生成物一致性、前端构建与 E2E 契约通过。

7. **`docs(authoring): document agent-native authoring`**
   - 更新系统架构、工作流、Agent Runtime、API 合约、部署、开发与测试文档，使其只描述共享 GeneratorService、Server 内部 Judge/Classifier、Runtime Worker 和本机 MCP connector。
   - 记录 HTTPS/JWT 配置、审核目录是可丢弃投影、Server 重启 workspace 语义，以及如何调试网页和 MCP 两个入口。
   - 删除旧后台 Generator、按钮确认、`GeneratorRunID` candidate lineage 和已移除端点的叙述。
   - 验证：文档链接与代码路径核对、格式检查、OpenAPI 生成物一致性、全量单元测试以及已建立的 E2E 验收通过。
