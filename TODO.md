# TODO: 领域化运维与 SRE 题库

> 题库不按零散技术名词扩张。先确定少数稳定的运维领域，再逐个把每个领域做成完整、可验证、可复用的学习内容。本文记录当前平台重构和后续内容建设；长期已确认的契约以 [docs/](docs/README.md) 为准。

## P0：统一 Agent Runtime 与 Runtime Worker

Roadmap 与 Catalog 迁移已经完成；其内容模型、portable release 和关系图契约保留在 [Catalog Release](docs/architecture/catalog-release.md)、[工作流](docs/architecture/workflows.md) 与 [系统架构](docs/architecture/system-architecture.md)。当前 P0 不再维护另一份 Roadmap 设计，而是重构执行边界：**所有 Eino Agent 都在 Server 中运行；唯一的 Runtime Worker 只执行 candidate runtime 的确定性外部资源操作。** Agent workspace 的 OpenSandbox/PVC 生命周期仍属于 Server 的受限工具服务。

模型不直接持有 Kubernetes、Incus、Registry 或 OpenSandbox 凭据。Agent 只能调用 Server 提供的、按会话或 workflow 围栏的 typed tool；Server 再以相应角色的远程客户端操作外部系统。AgentRun 是一次完整、可审计的逻辑 Agent 执行，内部可以包含多轮模型 HTTP 请求和工具调用；它不是常驻内存中的 Agent 进程。已知技术错误可以重试同一 AgentRun；Server 崩溃、重启或 lease 丢失时，必须终止中断运行，并从持久化边界创建新的 AgentRun，绝不恢复其未完成上下文。

### 目标架构

~~~text
Browser
  |
  v
Server
  |- public API, SSE, sessions, PostgreSQL writes, Server data PVC
  |- Agent Runtime: Authoring, Assistant, Generator, Judge, Classifier,
  |                 Roadmap Planner and Reviewers
  |- remote tool gateway: OpenSandbox workspace, read-only learning evidence,
  |                       Roadmap Retrieval and private authoring/classification setters
  |
  +--> PostgreSQL
  +--> Controller --> NodeEnvironment / VK8sEnvironment
  +--> Runtime Worker
           |- Building
           |- ArtifactPublishing
           |- Verifying
           |- ChallengePublishing
           +- runtime resource reaping

Runtime Worker --> Incus / OCI Registry / verification Environment
~~~

固定 Deployment 为 Server、Controller、PostgreSQL 和 Runtime Worker。生产 Registry 仍由部署者提供，Environment 仍是按需创建的 CRD 与动态资源。Generate Worker 不再是一个长期概念或 Deployment。

### Agent 上下文绑定

“给 Agent 绑定工作空间”指为每个 AgentRun 持久化最小、可审计的执行边界，而不是为每种 Agent 强行创建同一种 workspace。

| Agent role | 持久化绑定 | 可用远程上下文 |
| --- | --- | --- |
| Authoring | AuthoringSession、私有 Plan stage | 无 workspace；只能修改私有 Plan。 |
| Assistant | Assistant session、用户、Environment UID、终端窗口快照 | 当前学习 Environment 的只读证据工具；不能执行命令、修改文件或触发 checkpoint。 |
| Generator | GenerationWorkflow、PlanRevision、CandidateRevision | workflow-scoped OpenSandbox workspace；Server 创建、异常替换和回收。 |
| Judge | GenerationWorkflow、PlanRevision、CandidateRevision/archive | 只读 candidate 与题意；不需要学习或验证 Environment。 |
| Classifier | verified CandidateRevision、immutable RoadmapRevision | Roadmap Retrieval；调整时仅能修改私有 ClassificationProposal。 |
| Roadmap Planner / Reviewer | RoadmapTask、固定 revision、当前 subject | Roadmap search/read 与 typed ChangeSet/Review；不直接写 Roadmap。 |

- Generator workspace 归属于 GenerationWorkflow，不是某个 Server Pod、模型进程或 AgentRun。正常情况下，它是同一 workflow 的持久修复上下文：同一 Generator AgentRun 的已知技术重试，以及 Judge、Build 或 Verify 打回后启动的下一次 Generator AgentRun，都继续使用同一套 Sandbox/PVC。Server 中断后的确定性事实是已提交的 PlanRevision、CandidateRevision archive 和反馈；workspace 文件与模型内存都不承担恢复承诺。
- Assistant 绑定的是用户正在使用的 Environment，不是可写 Generator workspace。其工具继续保持只读、按用户和 Environment 围栏。
- Classifier 与 Roadmap Agent 绑定的是 immutable revision，不需要沙箱。Authoring 同样不应为了抽象统一而创建无意义 workspace。
- 所有 Agent role 保持独立 prompt、typed result 和工具集。迁移不能把 Authoring、Classification 或 Roadmap prompt 混成一段通用指令。

#### Generator workspace PVC

Generator 的工作空间是平台管理的持久资源，不是某次模型调用的临时目录，也不是 OpenSandbox 自动创建的 PVC。正常情况下，一个 workflow 只有一套当前工作空间：

~~~text
一个 GenerationWorkflow
  -> 一条当前工作空间记录
  -> 一个稳定、由 Server 创建和标记的 RWO PVC
  -> 一个 OpenSandbox Sandbox（挂载该 PVC）
~~~

- 工作空间记录拥有独立的 `workspace_id`，以 `workflow_id` 为归属，保存 namespace、PVC 名称、Sandbox ID 和生命周期状态。一个 workflow 只指向一条当前工作空间记录；替代工作空间使用新的记录和新的 PVC 名称，旧记录进入待回收状态。workspace 不保存 `GeneratorRunID`、PVC UID 或存储代次。
- Server 通过 Kubernetes API 创建 PVC；OpenSandbox 只挂载已存在的 PVC，始终使用 `CreateIfNotExists=false`。PVC 不指定 StorageClass，由集群默认 StorageClass 供应；它的标签和所有权必须指向 GenerationWorkflow，而不是某个 Server Pod、AgentRun 或 Sandbox。
- 同一 Generator AgentRun 的五次 attempt 均复用当前工作空间；Judge、Build 或 Verify 的确定性失败返回 `Generating` 后，下一次 Generator AgentRun 也复用它。此时 Generator 已成功产生过可审计 candidate，现有文件与反馈正是修复上下文，不能为了抽象一致而重置或删除。
- Generator `execute` 命令的非零退出码是普通 typed tool result，由 Agent 在同一 Run 内决定如何处理；workspace tool 的超时或传输错误是当前 AgentRun 的已知技术错误，最多五次且仍复用当前工作空间。它们都不单独触发 workspace 替换。
- Server 崩溃、重启和 lease 丢失后的统一处理见“中断恢复”一节。控制面暂时不可访问不等于资源已丢失；在 Server 仍持有有效运行归属时，不擅自替换当前工作空间。确认 Sandbox 或 PVC 已丢失时，直接按同一中断恢复契约替代当前 Generator Run 和工作空间。
- Generator AgentRun 耗尽五次 attempt、workflow 进入 Published、Cancelled 或 Failed 等终态后，Server 后台清理当前和所有待回收工作空间的 Sandbox，再删除对应 PVC。Candidate archive、业务 revision 和报告仍由 Server data PVC/数据库保留，不依赖工作空间存活。Runtime Worker 的 reaper 只处理 build image/archive、staging/final artifact 和 verification Environment 等 runtime 资源，覆盖作者 workflow 和失败或完成的 Catalog Release。所有回收都是幂等的后台工作，不新增可见 workflow state：candidate archive、报告和已接受的业务 revision 永久保留；未发布或被替代的 build/staging/final artifact 与验证 Environment 可被回收，已 Published 的 final artifact 保留。

### Agent Runtime 内部结构

Agent Runtime 是 **Server 进程内的执行边界**，不是新的 Deployment、通用 Agent framework、通用 task queue 或第二套 workflow。它只提供三项共用能力：持久化一次完整 Agent 执行、按 role 组装不可变上下文、在写入业务状态前校验 typed result。哪个 Agent 可以何时运行，仍由 AuthoringSession、GenerationWorkflow 或 RoadmapWorkflow 自己决定。

~~~text
HTTP request / 可领取的业务 state
  -> owning application service
  -> transaction: create AgentRun or retry a known technical error + bind fixed input/revision + acquire execution fence
  -> role-specific Eino executor + role-specific typed tools
  -> typed result
  -> transaction: validate result + write owning aggregate + complete AgentRun + advance state
~~~

- application 层保留 role-specific port 和 runner：Authoring/Assistant 直接处理用户回合；GenerationAgentRunner 只处理 `Generating`、`Judging`、`Classifying`；Roadmap 保留自己的 Planner/Reviewer task 流程。它们不经由按字符串 `kind` 分发的通用 executor。
- `internal/adapter/llm/` 只实现 Eino role executor 和工具适配器。它不得拥有 workflow 调度、数据库写入、Registry、Incus 或 Kubernetes 控制面权限；transport 层只调用 application service。
- `AgentRun` 的固定事实为 closed role、owner、可选 session、固定 input revision/引用、实际使用的 model/prompt version、状态、错误、时间、独立 deadline 和单调递增的 `attempt`。model/prompt version 只记录实际使用值用于审计。状态包含终态 `Interrupted`，用于记录被 Server 中断而废弃的运行。每个 AgentRun 最多执行五次已知技术错误的 attempt；一次 attempt 可以包含多轮模型 HTTP 请求和工具调用。`attempt` 只围栏同一运行内过期的 Eino 实例，不创建 `AgentAttempt` 表，也不改变 Run 的逻辑身份。它不保存模型推理过程，也不复制业务 typed result；候选 archive、Judgement、ClassificationProposal、Roadmap ChangeSet/Review 等仍由所属 aggregate 持久化并成为结果权威。
- 只有 Authoring 和 Assistant 需要持久 AgentSession。它们的围栏是 `session_id + active_run_id + attempt + fixed input revision`；Authoring 的 staged write 额外校验 Plan stage revision。它们不使用 workflow lease 或 `server_instance_id`。Generator、Judge、Classifier 以及 Roadmap Planner/Reviewer 都从固定输入开始，不创建额外会话；Generator 的修复上下文由 workspace、PlanRevision、CandidateRevision archive 和反馈提供。
- 同一用户消息、同一 GenerationWorkflow phase 或同一 Roadmap task/round 在正常执行时只创建一个逻辑 AgentRun。模型传输错误或 typed-result 校验错误以新的 Eino 实例执行该 Run 的下一次 attempt。Server 中断后的替代 Run 规则统一见“中断恢复”一节。新的用户消息、新的生成修复轮、分类反馈或新的 Roadmap round 同样创建新的逻辑 AgentRun。
- 交互式 run 的 SSE 只观察 Server-owned 执行；浏览器断开只解除订阅，不能取消已持久化 run。最终 assistant message 或 Authoring stage 在完成时原子落库；不持久化不完整的流式文本。
- Agent tool 只有三类可见副作用：只读查询；私有、可校验的 Plan/ClassificationProposal staged write；Generator 在其 workflow-scoped workspace 内的文件/命令操作。所有正式发布、镜像提升、Incus 操作、验证 Environment 创建和回收都必须先变成业务 state，再由 Runtime Worker 执行。
- 模型调用、传输错误或 typed-result 校验错误只消耗当前 AgentRun 的五次技术重试预算；耗尽后的业务状态由 role 的固定规则处理。迟到结果由 Run ID、attempt、lease/state version 和 input revision 拒绝。

#### 重试与阶段围栏

- 删除 `GenerationWorkflow.StateAttempt`、`MaxStateAttempts` 和 workflow 级 deadline。已知技术错误的重试次数与 deadline 只属于 AgentRun，固定最多五次；deadline 只用于避免一个不再返回的模型或工具执行永久占用该 Run 的 claim，不跨替代 Run 继承。`Interrupted` 是基础设施中断记录，不伪装为第六次 attempt；同一业务阶段随后创建的替代 Run 从确定性边界开始，并使用新的五次预算和新的 deadline。`state_version` 只围栏 workflow state，不承载重试或 deadline。
- GenerationWorkflow 保留单调递增的 `state_version`，它只在 workflow 进入新 state 时递增，不表示重试次数。claim 使用 `workflow_id + state_version + randomized lease_owner`；同一 state 的重新领取更换 lease owner，但不增加 state version。
- Runtime Worker 不拥有 AgentRun，也不能直接写入 `runtime_attempt`。每个 GenerationWorkflow Runtime state 只保留一个 `runtime_attempt`，进入该 state 时由 Server 初始化为 `1`；Worker 只报告基础设施失败，或由 Server 发现其 lease 已过期时，Server 才在受 state version 和 lease owner 围栏的事务中处理。当前值小于 `5` 时递增后重新领取同一 state；当前值已经是 `5` 时 workflow 进入 `Failed`，不产生第六次执行。重新领取继续使用同一 `workflow_id + candidate_revision_id + state + state_version` 外部身份 create-or-get；`runtime_attempt` 绝不进入资源名称或外部 identity。成功进入下一 state 时自然清零。Server 重启不改变正在执行的 runtime action，也不改变 `runtime_attempt`。单次 Provider 调用、Job 或 Incus 操作仍使用自己的局部超时，但不再有跨重试的 Runtime deadline。迟到报告由 state version 和 lease owner 拒绝。
- `NeedsClassificationReview` 是唯一的持久化分类审核态。一次合法的 Classifier 结果必须携带私有 `ClassificationProposal` 进入该状态，供作者审核、调整或确认发布；其中 `unclassifiable` 是合法诊断结果，但不能确认发布，作者必须取消 workflow、修改 Plan 后重新提交。Classifier 的 RoadmapRevision 是创建 Run 时固定的 immutable input，必须随 proposal 持久化；确认发布时只能比较该 revision 与 current revision，绝不能静默改写 proposal。二者不同即保留冲突并重新 Classifying，新的 Run 绑定最新 revision。Classifier 的五次 AgentRun 技术错误耗尽时，同样进入该状态，但保留 verified candidate 和失败说明；Server 在创建 publication intent 时发现确定性的分类冲突时，也回到该状态并保留 proposal 与冲突说明。三种情况不增加新的 workflow state。Building、ArtifactPublishing 或 Verifying 发现 candidate 的确定性内容错误时直接回到 `Generating` 修复，不消耗 `runtime_attempt`；ChallengePublishing 只接收已验证、已确认分类的 publication intent，因此只产生成功或基础设施失败，五次耗尽同样进入 `Failed`。
- Catalog Release 删除 release 级 deadline。Server 在 `Pending` 拉取和 stage immutable source 时维护独立的 `source_attempt`：确定性 source/OCI 契约错误立即使 Release `Failed`，基础设施错误从 `1` 开始最多执行五次，值为 `5` 的失败同样使 Release `Failed`。Catalog Entry 的 Building、ArtifactPublishing、Verifying 与 Commit 的最终 artifact promotion 都使用和 GenerationWorkflow 相同的当前 state `runtime_attempt` 规则；Entry 或 Commit 的五次基础设施失败使整份未公开 Release `Failed`，不公开部分内容。Catalog 的 source pull、Entry 和 Commit 都只使用局部调用超时，不保留任何 aggregate deadline。
- Catalog source pull 的外部读取身份是 immutable bundle digest；Entry action 的外部 identity 是 `release_id + entry_id + state + state_version`，Commit action 的外部 identity 是 `commit_id + state + state_version`。它们在同一 state 的五次执行中不变，`runtime_attempt` 从不参与命名。Runtime Worker 只能按 identity create-or-get，并校验资源标签、固定输入 digest 和已有引用一致；不一致即报告确定性契约错误。Server 只在 Release 仍可执行、Entry/Commit state 与 lease 相符时接受结果；Release 进入 `Failed` 后不再领取新动作，遗留 runtime 资源交给 reaper 回收。

#### 中断恢复：重建上下文，不恢复 Eino 执行

P0 **不接入 Eino `CheckPointStore` 或 `Resume`** 作为应用恢复机制。Eino checkpoint 解决的是显式 `Interrupt` 后恢复同一 Agent/Graph 的内部执行状态；它要求另一份持久化存储，并隐含工具节点及其结果的恢复语义。Breakfix 已有更清晰的业务事实来源，不能再引入一份 opaque 的 Agent runtime snapshot 作为第二权威。

- `context.Context`、模型内存推理、不完整 SSE 文本和 Generator 的历史对话都不持久化。Server 只保存 AgentRun 的固定输入引用、Authoring/Assistant 的完整 session message、Plan/Candidate/Proposal/Task 等业务状态以及当前 workspace binding。
- 模型调用、workspace tool 传输/超时错误或 typed-result 校验错误时，同一 AgentRun 以新的 Eino 实例重试，最多五次；普通 Generator 重试以及 Judge、Build 或 Verify 打回后的修复继续使用当前 workspace。每个会修改私有 Plan、ClassificationProposal 或 Generator workspace 的 tool 都携带当前 AgentRun、`attempt` 和所属 stage/workflow 围栏；取消、attempt 更新或 lease 失效后，旧实例不能再写入或提交结果。只有本节定义的 Server 中断、lease 丢失或确认 Sandbox/PVC 丢失才替换 workspace。
- **Server 崩溃、重启或 lease 丢失是唯一的中断恢复分支。** 接管者先在事务中把所有受影响的 active AgentRun 标记为 `Interrupted` 并解除旧 claim，再在原业务阶段创建替代 Run。替代 Run 的 `attempt` 从一开始，拥有新的独立 deadline，只从持久化输入和已提交 revision 重建上下文；它绝不读取旧 Run 的模型内存、未完成消息或工具状态。
- 单副本 Server 在自身变为 ready 前完成启动恢复。对每个仍由 AuthoringSession 或 Assistant session 指向的 active Run，Server 在同一事务中将旧 Run 标为 `Interrupted`、创建绑定相同固定输入 revision 的替代 Run（`attempt = 1`、重新计时）并更新 `active_run_id`；后台 Agent state 同样解除旧 claim 后由其 runner 创建替代 Run。这样浏览器只会看到新的 active Run，不会与旧执行并存。
- 对 Authoring 与 Assistant，替代 Run 自动执行。浏览器重新打开页面后读取新的 active Run 并重新订阅；不回放旧 token。若旧 Run 已经原子提交最终消息或 stage，它不再是 active Run，也不会产生替代 Run。
- 对 Generator，只要被中断 Run 已绑定 Sandbox，Server 无条件立即解除 workflow 的当前 workspace binding，将该 workspace 记录标为待回收；后台 reaper 必须删除旧 Sandbox 和 PVC。替代 Run 不等待删除完成，创建新的 workspace 记录、新 PVC 和新 Sandbox，并从最新 immutable candidate archive、已提交反馈和冻结 PlanRevision 开始。没有 candidate archive 时从 Plan 初态开始。Judge、Classifier 与 Roadmap 均从各自固定 revision/输入开始替代 Run。
- Runtime Worker 不属于本节：Server 中断不重置或重启正在执行的 runtime action；它继续通过 action identity、state version、lease 和幂等 create-or-get 处理自身恢复。
- 未来若确实出现“显式暂停等待人类输入，再在同一 Agent 图中继续”的独立产品需求，可以单独评估 Eino checkpoint；它不能反向替代本节的业务恢复契约。

### 状态与执行归属

GenerationWorkflow 继续是作者题目的唯一持久流程，但 active state 按能力固定分配，不再由一个混合 Worker 跨越全部阶段领取。

#### 作者提交与失败重启

Authoring 是讨论和编辑私有 Plan 的交互式阶段，不是隐式的提交流程。Authoring Agent 只能修改当前会话的私有 Plan stage，不能创建、提交或重启 `GenerationWorkflow`。

1. 作者与 Authoring Agent 讨论并修改 Plan。
2. 作者显式确认一个 Plan revision，并提交新的确认 idempotency key。
3. Server 校验作者、会话、Plan revision 和 idempotency key；校验通过后原子冻结该 `PlanRevision` 并创建一个新的 `GenerationWorkflow`。
4. 只有这个已创建的 workflow 才可进入 `Generating`，由 Generator Agent 开始执行。

同一确认请求的重复投递只返回原有 workflow，不能生成重复题目。`Failed` 是终态，没有恢复或继续执行旧 workflow 的接口。作者可以修改 Plan 后再次确认，也可以不修改 Plan 而以新的确认 idempotency key 再次确认；两种情况都创建新的 workflow ID、全新的 AgentRuns 与新的 Generator workspace。旧的失败 workflow、candidate 和报告仅作为审计记录保留，绝不被新的 workflow 续跑或复用其 workspace。

每个 `AuthoringSession` 同时最多只能有一个非终态 workflow。提交后对应 `PlanRevision` 永远冻结；在 workflow 仍运行或等待审核时，Server 拒绝新的 Plan stage 写入和新的 Plan 确认，也不自动替换旧 workflow。`NeedsAuthorReview` 中的作者反馈只修复已冻结题意下的 candidate，回到同一 workflow 的 `Generating` 并复用其 workspace；若作者要改变题目意图，必须先显式取消当前 workflow，再回到私有 Plan stage 修改并确认新的 revision。`Cancelled` 和 `Failed` 后都可以继续编辑 Plan 并创建新的 workflow；`Published` 则关闭该 AuthoringSession，新题目必须创建新的会话。删除 `Superseded` state、`superseded_by_workflow_id` 和一切自动 supersede 行为。

~~~text
AuthoringSession
  -> Server: Generating -> Judging
  -> Runtime Worker: Building -> ArtifactPublishing -> Verifying
  -> NeedsAuthorReview
  -> Server: Classifying
  -> NeedsClassificationReview
  -> Runtime Worker: ChallengePublishing
  -> Server: materialize Challenge + publish RoadmapRevision -> Published
~~~

状态机不增加“内容错误”“发布完成待提交”之类的中间 state；它们是当前 state 中由持久化事实区分的固定分支：

| 事件 | 固定处理 |
| --- | --- |
| Building、ArtifactPublishing 或 Verifying 发现 candidate 的确定性内容错误 | 回到 `Generating`，保留 candidate、报告和当前 workspace 作为修复上下文。 |
| Classifier 的 RoadmapRevision 已过期，或确认发布时发现 Roadmap revision 冲突 | 回到 `NeedsClassificationReview`，保留 proposal 与冲突说明；重新 Classifying 必须绑定最新 immutable RoadmapRevision。 |
| Challenge source 或 title 的身份冲突 | 回到 `NeedsAuthorReview`，由作者决定修复题意或取消后重提。 |
| Runtime state 的第五次基础设施失败、或 Catalog source/Entry/Commit 的第五次基础设施失败 | 进入 `Failed`；不会转入分类审核。 |
| ChallengePublishing 的外部 promotion 已成功但 Server materialize/commit 失败 | 保留已成功的 publication result 和 durable publication intent；恢复时只重试 Server finalizer，不再重新领取或重复 Worker promotion。 |

| 所有者 | 工作 |
| --- | --- |
| Server 的交互式 Agent Runtime | Authoring 和 Assistant 的用户回合，可向浏览器流式输出。 |
| Server 的持久化后台 Agent Runtime | Generating、Judging、Classifying，以及 Server-owned RoadmapWorkflow 的 Planner/Reviewer。 |
| Runtime Worker | Building、ArtifactPublishing、Verifying、ChallengePublishing 和 Runtime resource reaping。 |
| Server workspace service | Generator workspace 的 Sandbox/PVC 创建、异常替换和终态回收；不是 Agent，也不是第二个 Worker。 |
| Server 的业务提交 | 状态迁移、AgentRun、CandidateRevision、ClassificationProposal、publication intent、challenge 目录物化和 RoadmapRevision copy-on-write。 |
| Controller | Environment CRD 的调和、status 与生命周期回收。 |

NeedsAuthorReview 与 NeedsClassificationReview 是持久化的作者等待态，不属于任何执行器。前者中，作者确认 verified candidate 后 Server 启动 `Classifying`；作者提交 candidate 修复反馈后 Server 启动同一 workflow 的下一次 `Generating`。后者总是展示正常 proposal、失败说明或 publication 冲突之一：`proposed` proposal 可提交分类反馈以启动新的 `Classifying`，或确认无冲突的 proposal 发布；`unclassifiable` proposal 只能取消后回到 Plan 修改；Classifier 耗尽且没有 proposal 时，只提供显式“重新分类”，由 Server 以同一 verified candidate 和固定 RoadmapRevision 创建新的 Classifying AgentRun，不自动循环，也不允许直接写全局 Topic/Tag。确认发布后，Server 创建 publication intent，Runtime Worker 异步完成正式 artifact 提升，Server 最后 materialize 并公开结果。

AgentRun 耗尽五次已知技术重试后不再由各调用点临时决定：`Generating` 或 `Judging` 使 workflow 进入 `Failed` 并保留 Plan、candidate 与错误；正常的 `Classifying` 以 proposal 进入 `NeedsClassificationReview`，耗尽时也进入该状态并保留 verified candidate 与失败说明；Authoring 与 Assistant 只使当前用户回合失败，等待用户显式重试；Roadmap task 保留为待处理项，等待下一次 maintenance workflow。

Runtime Worker 的结果同样有固定去向：Building、ArtifactPublishing 或 Verifying 的 candidate 内容错误回到 `Generating`；任一 GenerationWorkflow Runtime state 的五次基础设施失败进入 `Failed`；Catalog Entry/Commit 的五次基础设施失败使未公开的 Catalog Release 进入 `Failed`。`NeedsClassificationReview` 不承接 Runtime Worker 失败。

ChallengePublishing 不是交互式 Agent 工作。K8s runtime 需要在 Registry 中将验证过的 staging image 提升为 challenge-scoped immutable image；Node runtime 需要在 Incus 中提升正式 image/alias。两者都与构建、验证共享外部资源身份和幂等语义，必须归 Runtime Worker。

### 持久化、恢复与并发

Server 内执行 Agent 不等于把后台生成绑在浏览器 HTTP 请求上。

- Authoring 与 Assistant 同一 session 至多有一个 active AgentRun；不同 session 可以并发。它们使用 Server lifecycle context 执行，而不是浏览器 request context，因此 SSE 断开不会让已持久化的会话、消息和 AgentRun 消失；Server 中断后的自动替代与重新订阅遵循“中断恢复”契约。
- Generating、Judging 和初始/调整 Classifying 都由 Server 的持久化后台 runner 领取。runner 只领取明确的 Agent state，不引入通用 kind、通用 executor、持久 slot、用户配额或第二套任务模型。
- Server 在事务中创建或续租 Agent phase claim、创建 AgentRun 或启动已知技术重试、读取固定输入，并只接受同一 Run ID、attempt、lease/state version 的 typed 结果。Server 中断后的 Run、workspace 与交互式订阅处理只遵循“中断恢复”契约，不重放已完成的外部资源阶段。
- Runtime Worker 只领取 Runtime state；一个 Pod 同时处理一个已声明的 runtime action，Deployment replica 数量就是显式的外部资源并发上限。不引入 Worker slot 或按字符串 kind 分发的通用队列。
- 一次 state 成功后释放当前执行器的 lease；下一个 state 由其固定所有者重新领取。GenerationWorkflow 只保留状态机、`state_version`、无数量上限的 CandidateRevision 审计记录和显式取消语义。每个已接受的 CandidateRevision 均永久保留；技术重试不会凭空创建 revision。审核态没有正在执行的 AgentRun deadline，Server 中断也不重置 Runtime Worker 的 action。
- 当前 Server 使用 RWO data PVC，以单副本运行。P0 要求其 Agent runner 在重启后能从持久化边界重新开始，不把“单副本”当成内存状态永久可靠的理由；未来多副本仍需先解决共享 artifact 存储和流式连接，不在本次伪装支持。

### 权限与远程工具边界

- Server 持有模型 API 凭据、OpenSandbox 生命周期权限、Server data PVC、PostgreSQL 和仅完成业务所需的远程读取能力。Assistant 的 Environment 读取继续经受限工具执行，而不是把原始集群凭据交给模型。Catalog source 拉取可保留最小 Registry 只读权限。
- Runtime Worker 持有 Registry 写入、Incus build/publish 和验证 Environment 所需的 Kubernetes/Incus 角色凭据；它不持有模型 API key、OpenSandbox 管理凭据、PostgreSQL DSN 或 Server data PVC。
- Runtime Worker 的唯一输入是由 Server 领取并围栏的具体 runtime action。Server 只在 action 的 `owner + state_version + lease` 仍有效时，向 Worker 提供不可变 `RuntimeActionContext`，并通过同一内部 API 流式提供该 action 对应的 candidate archive 或 Catalog entry source。Context 至少固定 archive/source、base artifact digest、验证 Environment snapshot/spec、已有 artifact reference、目标 artifact reference、action identity、state version 和 lease；Worker 不直接挂载、读取或写入 Server data PVC，不使用预签名对象存储，也不获得任意 workflow 或 release 的查询能力。完成或失败报告继续携带同一 action identity。这个协议只服务 Building、ArtifactPublishing、Verifying 和 ChallengePublishing 等固定 Runtime state，不是通用任务队列。
- Server 不再在进程内执行 Catalog entry 的 Build、artifact publish、Verify 或最终 runtime artifact 提升。它保留 source staging、release 协调、commit intent、challenge materialization 和 RoadmapRevision 事务。
- Worker 到 Server 的内部 API 使用独立 runtime role key、owner、state version 和 lease 围栏。Agent 因为在 Server 内执行，不再经过 Generate Worker 的 workspace/classification HTTP 代理；这些旧内部接口和凭据必须删除，不保留兼容路径。

### Runtime action 交接、租约与回收

Runtime action 是挂在 GenerationWorkflow、Catalog Entry 或 Catalog Commit 的固定 state 上的持久化外部动作，不是第二套通用队列。Server 负责创建、领取、续租和状态提交；Runtime Worker 只按不可变 context 执行并报告结果。

- **Build 输出不能依赖 Worker 本地文件或 Server PVC 路径。** K8s Build 必须写入 Registry 中 build-scoped 的 immutable OCI artifact，Node Build 必须产出 Incus build image reference。Server 只持久化 provider-side reference 与 digest；`ArtifactPublishing` 再将该 build artifact 提升为 candidate staging artifact。作者 workflow 与 Catalog Entry 使用同一条交接链路。
- Worker 在执行期间必须续租 action。续租失败后立即停止本地操作，且不得提交结果。Server 重启不主动重置 action；lease 到期后由接管者沿用同一 external identity create-or-get。取消 workflow 时，Server 撤销 lease 并推进 state/version，之后的迟到报告一律拒绝，外部残留资源交给 reaper 异步回收。
- 同一 action 的外部 identity 由 aggregate、entry/candidate、state 和 `state_version` 派生，永不包含 `runtime_attempt`。同一 state 的全部重试必须 create-or-get 同一个外部资源，并校验标签、输入 digest 和已有 reference；不能因重试产生另一套 Build Job、Incus image 或 Registry artifact。
- Server 持有学习用 Environment 的 spec；Runtime Worker 只创建、读取和删除 `purpose=verification` 的 Environment，Controller 始终负责 CRD reconcile 与 status。验证报告一经持久化，验证 Environment 即由 reaper 回收；删除失败只重试删除，绝不重新验证。
- ChallengePublishing 与 Catalog Commit 都先由 Server 写入 durable publication intent。Worker promotion 成功后，Server 必须先持久化该 immutable result/reference；随后才以确定路径、hash 校验和幂等物化 source，最后在事务中公开 Challenge/RoadmapRevision 或 Catalog Release。重启恢复时只重试 Server finalizer，不重复已成功的 Worker promotion。
- Runtime Worker 的 readiness 按其可执行的 provider 类型报告；某一个 provider 不可用不能让其他类型 action 的领取与执行整体停摆。

### Catalog Release 的同一执行边界

Catalog Release 没有 Agent，但必须使用同一 Runtime Worker，否则 Server 仍会隐式承担构建和发布职责。

~~~text
Server: pull and stage immutable Catalog source; persist Release/Entry/Commit state
  -> Runtime Worker: Entry Building -> ArtifactPublishing -> Verifying
  -> Server: create durable commit intents
  -> Runtime Worker: promote each final runtime artifact
  -> Server: materialize source and atomically publish RoadmapRevision
~~~

Catalog Release 仍不创建 AuthoringSession、GenerationWorkflow 或 Generator run。它保留可恢复 identity 与原子公开契约，但删除 release aggregate deadline：Server-owned source staging 使用独立 `source_attempt`，Entry 和 Commit 的外部动作使用当前 state `runtime_attempt`，两者均固定最多五次。变化不仅是 external runtime action 的执行者从 Server 移到 Runtime Worker，也包括将其重试语义统一为 P0 的有限、按状态计数模型。

#### Catalog 启动门槛

- 正常部署必须提供 immutable `catalog_release_reference`。Server 先原子拉取、stage 并 digest 校验该 release source；不能将部分 source 暴露为可读取 catalog。
- Catalog 尚未 `Ready` 时，Server 在应用层拒绝 catalog-dependent 的读取、生成、分类和发布请求。Kubernetes readiness 不依赖 Catalog Ready，避免 Runtime Worker 或恢复中的 Catalog Release 因 Server 未就绪而形成启动死锁。
- Catalog `Failed` 是终态。修复只能提交一个新的 immutable bundle identity 并创建新的 Release；不得原地自动无限重试旧 bundle。

### Roadmap maintenance 的执行边界

Roadmap workflow 是 Server 内的异步维护流程，不创建新的 Deployment，也不使用 Runtime Worker。它的 Planner/Reviewer 继续各自持有 role-specific prompt 与 typed tool，只有 Server 合并通过审查的 ChangeSet 并写入新的 RoadmapRevision。

- 删除旧 `MaintenanceExecutionDeadline`。每个 Planner/Reviewer AgentRun 仍各自最多五次技术重试；一个 Roadmap task 的语义 round、Planner/Reviewer 的调用上限和 AgentRun 的技术 attempt 是三套不同概念，不能互相消耗或混写。
- Roadmap 启动与运行期间保持 idle barrier：开始时不得有题目处于生成、判断、构建、发布、验证或分类等执行 state；Roadmap 活跃期间也不得进入新的这些执行 state。作者等待审核不算执行 state，但新的作者确认、分类确认或发布确认必须等待 Roadmap 完成。
- 单个 task 的 AgentRun 技术失败不创建额外失败 state，也不自动循环；task 保持待处理，留给下一次 maintenance workflow。这样不会把临时模型或传输故障伪装成 Roadmap 语义结论。

### 迁移清单

- [x] 将 Eino 的 Generator、Judge 和 Classifier 从 Generate Worker 移入 Server Agent Runtime；Authoring、Assistant 与 Roadmap 保持同一 Server 运行边界，但保留独立 application port、role、prompt 和 typed tool。删除 Generator AgentSession 与所有以 Agent 为中心的 Worker 内部代理。
- [x] 明确作者提交、AgentRun 与 workspace 的恢复边界：Authoring Agent 只能修改私有 Plan stage；只有作者显式确认 Plan revision 后，Server 才能以确认 idempotency key 原子创建 GenerationWorkflow。每个 AuthoringSession 同时只允许一个非终态 workflow，期间冻结 Plan；`Failed` workflow 不提供 resume，删除 `Superseded` 及自动替换路径。Server 中断、重启或 Agent lease 丢失时，旧 Run 标记为 `Interrupted`，替代 Run 从持久化事实重新开始；被中断的 Generator workspace 无条件解除 binding、异步回收并以新 PVC/Sandbox 替换。
- [x] 将 GenerationWorkflow claim 拆为固定的 Server Agent state 集与 Runtime state 集；补全既有 state 的转移与冲突分类，不新增状态。`runtime_attempt` 仅由 Server 在基础设施失败或 lease 过期时递增，进入 state 为 `1`、最多五次、成功转移后清零；它不进入外部资源 identity。CandidateRevision 不设数量上限，已接受 revision 永久保留审计；技术重试不产生 revision。删除 `generation_repository` 在确认发布时将 proposal RoadmapRevision 静默覆盖为 current revision 的行为，revision 冲突必须回到 `NeedsClassificationReview`。
- [x] 将 Generator workspace tool 改为 Server 内的受限 workspace service，以 workflow-owned `workspace_id` 管理当前与待回收工作空间。命令非零、workspace tool 超时/传输错误和 Judge/Build/Verify 的正常修复继续复用当前 workspace；只有 Server 中断、lease 丢失或确认资源丢失时替换。Generator 不得直接取得 OpenSandbox 凭据。
- [ ] 新建唯一的 Runtime Worker，迁移 Building、ArtifactPublishing、Verifying、ChallengePublishing 与作者 workflow/Catalog 的 runtime resource reaper。Build 交接必须使用 provider-side durable reference：K8s 使用 build-scoped immutable OCI artifact，Node 使用 Incus build image reference；Server 仅持久化 reference/digest，ArtifactPublishing 再提升为 staging artifact。Worker 不读写 PostgreSQL、Server data PVC 或 OpenSandbox，也不直接修改 `runtime_attempt`。
- [ ] 实现 action-scoped Server API 和 external identity 围栏。RuntimeActionContext 必须固定 archive/source、base artifact digest、验证 snapshot/spec、artifact refs、state/version、identity 与 lease；Worker 只能取得已 claim action 的输入。Worker 必须续租，失败即停止且不提交；Server 重启不重置 action，接管者沿用同一 identity create-or-get 并校验标签、digest/reference，迟到结果拒绝。取消撤销 lease、推进 version，残留资源交给 reaper。
- [ ] 统一验证与发布的所有权和恢复：Server 管理学习 Environment spec，Worker 只管理 `purpose=verification` Environment，Controller 负责 reconcile/status；报告持久化后仅异步删除环境，不重做验证。ChallengePublishing/Catalog Commit 先持久化 publication intent，Worker promotion 成功报告由 Server 持久化 result，Server 以 hash 校验、幂等 source materialization 和事务性 Roadmap/release 公开完成 finalizer；恢复时不重复 promotion。
- [ ] 将 Catalog Release entry 与 commit 的 Build、Publish、Verify、final artifact promotion 迁移到 Runtime Worker；Server 保留 source staging、release 协调和 materialize/commit。正常部署要求 immutable `catalog_release_reference`；source staging 必须原子且 digest 校验。Catalog 未 Ready 时只在应用层拒绝 catalog-dependent 请求，不能把 Kubernetes readiness 绑定到 Catalog；失败 release 仅可由新的 immutable bundle identity 重试。
- [ ] 将 Roadmap maintenance 保持在 Server 内，删除 `MaintenanceExecutionDeadline`；区分 task 的语义 round 与 Planner/Reviewer AgentRun 的五次技术 attempt。实现 idle barrier 和失败 task 留待下一轮的语义，不创建新的 Worker、通用队列或额外失败 state。
- [ ] 更新 Deployment、镜像、ServiceAccount、Secret、配置、Telepresence、health/readiness、内部 API、测试 fixture 和正式文档；Runtime Worker readiness 必须按 provider 类型隔离故障。删除 generate-worker、其模型凭据、workspace/classification proxy 和任何旧命名，不保留兼容运行路径。
- [ ] 完成聚焦验证：状态/lease 围栏、AgentRun 中断恢复、action 幂等接管、Node/K8s runtime smoke、Catalog fixture install、Server recovery，以及 workspace/runtime resource reaper。不得恢复大型不稳定 E2E；不写 prompt 文本、渲染断言或迁移拒绝测试。

### P0 提交计划

P0 按以下顺序实施，**每完成一个部分就立即提交**。每个提交都必须可编译、通过对应的单元和集成验证；接口或部署契约变化在所属提交内同步更新生成物、前端调用和正式文档。不保留旧 Worker、旧内部接口或兼容转换；不写 prompt 文本断言、模型输出渲染断言或迁移拒绝测试。

1. **refactor(agent-runtime): centralize model execution in server**
   - **目标：** 将 Generator、Judge、Classifier 作为 Server 的独立 Agent role 运行，删除 Generator AgentSession 和 Worker 侧 Agent proxy；实现 Authoring 显式确认创建 workflow、Plan 冻结、`Failed` 不恢复和显式取消语义。实现 AgentRun 的五次技术重试与中断恢复，不接入 Eino checkpoint、工具结果 replay 或第二份 Agent 状态存储。
   - **验证：** 每种 Agent 只能取得其声明的上下文和工具；同一确认幂等、非终态 workflow 阻止 Plan 修改、旧 workflow 不会自动 supersede。Server 中断后旧 Run 成为 `Interrupted`，替代 Run 只使用已提交事实和新的预算；被中断的 Generator 换新 PVC/Sandbox，正常技术重试和内容修复仍复用当前 workspace。
2. **refactor(generation-state): fence state, revision and runtime actions**
   - **目标：** 固定 Server Agent state 与 Runtime state 的领取边界，补全状态转移、冲突处理和 `runtime_attempt` 规则。实现 action lease、state/version 围栏、取消与相同 external identity 的 create-or-get；移除确认发布时静默覆盖 ClassificationProposal RoadmapRevision 的旧行为。CandidateRevision 无上限且不由技术重试产生。
   - **验证：** 只有 Server 执行 Generating/Judging/Classifying，`runtime_attempt` 只由 Server 管理且第五次基础设施失败进入 `Failed`。内容错误、Roadmap revision 冲突、source/title 冲突和已成功 promotion 后的 Server finalizer 重试均落入规定分支；过期 lease、取消和迟到报告无法改变结果或重复创建外部资源。
3. **refactor(runtime-worker): move durable external operations**
   - **目标：** 创建唯一的 Runtime Worker，迁移 Build、ArtifactPublishing、Verifying、ChallengePublishing 和 runtime reaper。实现 action-scoped Context、provider-side Build 输出交接、验证 Environment 所有权、durable publication intent 与幂等 finalization；收紧 Server、Worker、Controller 的权限和 provider-aware health。
   - **验证：** Node/K8s candidate 都经 immutable build artifact/reference、staging、真实验证和最终 promotion 完成；Worker 只能取得已 claim action 的固定输入且不具备模型、OpenSandbox、数据库或 Server PVC 权限。验证报告持久化后只回收 Environment，不重做验证；promotion 成功后的恢复不会再执行 Worker action，reaper 可幂等清理未发布资源。
4. **refactor(catalog-roadmap): share runtime boundary and restore startup rules**
   - **目标：** 将 Catalog Entry/Commit 的外部 Build、Publish、Verify 和 final promotion 移入 Runtime Worker，保留 Server source staging、atomic commit 与 Catalog 启动 gate；删除 release 和 maintenance aggregate deadline。将 Roadmap maintenance 固定为 Server 内 Planner/Reviewer 流程，落实 idle barrier、语义 round 与技术 attempt 分离。
   - **验证：** immutable Catalog fixture 在 source/Entry/Commit 的重试、lease 接管和 Server finalizer 恢复中保持原子，失败 release 不公开且必须用新 bundle identity 重试。Catalog 未 Ready 只在应用层拒绝依赖请求，不阻塞 Kubernetes readiness；Roadmap 不能与题目执行 state 并行，暂时失败 task 保留到下一轮。
5. **refactor(deploy-test): remove the old boundary and verify recovery**
   - **目标：** 删除 Generate Worker 二进制、Deployment、镜像、模型凭据、HTTP proxy、旧 API/测试辅助和文档术语；更新 Deployment、ServiceAccount、Secret、Telepresence、生成物和正式文档，形成 Server、Controller、PostgreSQL、Runtime Worker 的唯一部署契约。
   - **验证：** 完整 Go、生成物和前端构建通过；状态/lease 围栏、AgentRun 中断恢复、action 幂等接管、Node/K8s runtime smoke、Catalog fixture install、Server recovery 和两类 reaper 均通过。保持聚焦测试，不恢复大型不稳定 E2E，也不添加 prompt/渲染/迁移拒绝断言。

P0 完成标准是：Server 是全部 Agent 的唯一执行位置；Generator 不再拥有 AgentSession 或 run-scoped workspace 身份；每个 AuthoringSession 只有一个非终态 workflow，`Superseded` 和自动替换路径均不存在；CandidateRevision 无数量上限且永久保留已接受 revision；Server 中断时 AgentRun 与 Generator workspace 按“中断恢复”契约替换；Runtime Worker 是唯一的 Build/Publish/Verify 执行位置，只能通过 action-scoped Server API 读取不可变输入，并以可接管的 identity 完成外部动作；Catalog 与作者题目共享该 runtime 边界，且所有已成功 promotion 都只由 Server finalizer 继续提交；仓库中不再存在 Generate Worker 兼容路径。

## P1：题库内容建设

以下内容只服务于题目设计、讨论、审查和 portable release 制作，不属于 P0 迁移，也不直接显示在用户 UI 中。

### 内容工作区

根目录 `catalog/` 是可读、可审查的内容工作区；`curriculum/` 是作者整理 Domain/Topic 边界和题目设计的材料，`releases/<name>/` 才是可打包、可安装的实际 Catalog Release：

```text
catalog/
  curriculum/
    domains/<domain>/README.md
    topics/<domain>/<topic>.md
  releases/
    foundation-linux-system-network/
      release.yaml
      challenges/linux-system-network/<readable-source>/
      roadmap/
        domains/
        topics/
        tags/
        challenge-bindings/
        topic-edges.yaml
        challenge-edges.yaml
```

- 每个 Domain 导读说明学习目标、范围、非范围、建议学习顺序、对运行时的要求、参考来源和完成标准。
- 每份 Topic 导读记录范围、前置背景、心智模型、典型故障、诊断证据、修复原则、常见误区、面试追问和来源。它是题库设计材料，不是用户 UI 页面，也不是 Agent 分类规则机器。

### 领域范围调研

调研日期：2026-08-02。没有一个权威框架把“运维”缩成唯一目录，但 Linux 系统管理、Kubernetes 管理和 SRE 的官方能力纲要收敛出以下稳定边界。

| 领域 | 边界与核心内容 | 调研依据 | 当前取舍 |
| --- | --- | --- | --- |
| Linux 系统与网络运维 | Shell/文件、用户与权限、软件包、进程与资源、systemd/journal、存储、网络、DNS、TLS 基础、SSH 与远程排障 | [RHCSA objectives](https://www.redhat.com/en/services/training/ex200-red-hat-certified-system-administrator-rhcsa-exam) 覆盖 essential tools、运行中系统、存储、服务、网络、用户/组和安全 | **首个完整领域**；使用 NodeEnvironment |
| 容器运行时运维 | 镜像、容器进程、日志、挂载、网络、资源限制与调试 | [roadmap.sh DevOps roadmap](https://roadmap.sh/devops)、[devops-exercises](https://github.com/bregman-arie/devops-exercises) 将 containers 作为独立能力域 | 暂不单列首批 release；先作为 Linux/Kubernetes 题的必要子能力，实际内容量足够时再独立 |
| Kubernetes 工作负载与服务运维 | Workload、配置、调度与资源、Service/DNS、存储、RBAC、NetworkPolicy、应用排障 | [CKA Domains & Competencies](https://training.linuxfoundation.org/certification/certified-kubernetes-administrator-cka/) 的 workload/scheduling、services/networking、storage、troubleshooting；[Kubernetes Concepts](https://kubernetes.io/docs/concepts/) | **第二个完整领域**；使用 VK8sEnvironment |
| Kubernetes 集群管理 | 控制平面、节点生命周期、etcd、证书、CNI、集群安装与升级 | CKA 的 cluster architecture/install/config；[Kubernetes The Hard Way](https://github.com/kelseyhightower/kubernetes-the-hard-way) 的 CA、etcd、控制平面、worker 与网络顺序 | 暂缓。现有 VK8sEnvironment 首先服务于工作负载运维，不能假装覆盖真实集群管理 |
| SRE 可靠性运维 | SLI/SLO/error budget、指标/日志/追踪、告警、容量、事故指挥、runbook、复盘与 toil 自动化 | [Google SRE Book: SLO](https://sre.google/sre-book/service-level-objectives/)、[Monitoring](https://sre.google/sre-book/monitoring-distributed-systems/)、[Toil](https://sre.google/sre-book/eliminating-toil/)、[Incidents](https://sre.google/sre-book/managing-incidents/) | **第三个完整领域**；建立在 Linux/Kubernetes 题库之上 |
| 交付与平台自动化 | Git、CI/CD、配置管理、IaC、GitOps、变更与回滚、镜像与供应链 | [roadmap.sh DevOps roadmap](https://roadmap.sh/devops)、[devops-exercises](https://github.com/bregman-arie/devops-exercises) | 暂缓，不为覆盖面强行增加题目 |
| 安全、身份与供应链 | 主机加固、密钥、访问控制、镜像/依赖安全、审计与响应 | RHCSA 的安全目标与 Kubernetes 的 security/policy 概念 | 初期作为各领域的横向约束；有足够独立内容后再判断是否单列 |

因此，基础题库只激活以下三个领域，并且**一次只生产一个领域**：

1. **Linux 系统与网络运维**：先完成，目标是建立主机、服务和多节点排障的扎实基础。
2. **Kubernetes 工作负载与服务运维**：Linux 领域达到完成标准后再开始。
3. **SRE 可靠性运维**：前两个领域已有真实服务和故障素材后再开始。

容器、Kubernetes 集群管理、交付自动化和专项安全不是遗漏，而是明确延期；在前三个领域没有做深之前，不为它们创建零散题目。

### Linux 系统与网络运维领域设计

### 边界

这个领域的目标是让学习者能够在一台或多台 Linux 主机上，以运行证据定位并恢复服务，不是训练命令记忆，也不是覆盖所有 Linux 内核、硬件或云厂商知识。

- 运行时固定为 `NodeEnvironment`。题目可创建 `client`、`gateway`、`app`、`resolver`、`storage` 等场景角色节点，学习者可以进入所有节点；这些名称不暴露 Incus。
- 首发包含主机文件与权限、账号与远程访问、软件包与配置、进程与 systemd、日志、资源与存储、IP/DNS/端口/TLS 和多节点网络诊断。主机防火墙、抓包和挂载恢复留待 capability probe 后扩展。
- 应用程序只作为可观察服务载体，例如 Nginx、OpenSSH、dnsmasq 或一个小型自定义 HTTP 服务；不把数据库、容器或 Kubernetes 专项知识混入本领域。
- 不包含 bootloader、内核模块、硬件驱动、真实宿主机内核调优、RAID/LVM、云 VPC 控制面或公网依赖。现有 Node 运行时是非 privileged Incus system container，不能假装它等同裸机。
- 检查和答案必须完全在环境私有网络中完成。不得像某些公开练习一样依赖 `google.com`、真实公网 DNS 或外部软件服务作为通过条件。

### 调研结论

| 参考 | 实际观察 | 对本领域的取舍 |
| --- | --- | --- |
| [Red Hat RHCSA objectives](https://www.redhat.com/en/services/training/ex200-red-hat-certified-system-administrator-rhcsa-exam) 与 [Linux Foundation LFCS](https://training.linuxfoundation.org/certification/linux-foundation-certified-sysadmin-lfcs/) | 两者都把主机工具、运行中服务、存储、网络、身份和安全作为系统管理的稳定内容边界。 | 用作领域范围的交叉校验，不把认证命令清单或考试时间限制直接变成题目。 |
| [Linux Upskill Challenge](https://github.com/livialima/linuxupskillchallenge) | 以 Day 1--20 从 SSH、主机认识、权限、软件包、服务、网络、计划任务、日志、磁盘逐步建立基础。 | 借鉴前置知识的推荐顺序；每个练习仍改写为症状驱动、可独立完成的故障场景，不复制讲义、任务或命令。 |
| [SadServers](https://github.com/SadServers/sadservers) | 124 个公开 scenario 中可见 `cordoba` 的 deleted-but-open 文件、`sume` 的内网 DNS、`pokhara` 的 SSH key、`valladolid` 的 systemd service 等真实故障模型。它的许多检查同时也绑定文件哈希、固定脚本或 CTF 结果。 | 借鉴“症状 + 目标状态 + 环境内验证”的场景形态；不复制 scenario、脚本或题干，尤其不采用哈希、固定编辑路径和唯一命令式检查。 |
| [bregman-arie/devops-exercises: Linux](https://github.com/bregman-arie/devops-exercises/tree/master/topics/linux) | systemd、SSH、存储、性能、进程、安全、网络、DNS、软件包、服务、用户/组覆盖很广，但主体是概念问答。 | 用作知识遗漏检查和 Topic 导读中的面试追问来源；不把问答页直接转写为 runtime 题目。 |
| [Killercoda scenario examples](https://github.com/killercoda/scenario-examples) | 一个场景将初始化、说明和每步 verify 资产分开。 | 借鉴 `generate.sh`、教学材料与验证资产分离；不使用它的有序 step、点击式 verify 或预置命令。 |
| [Educates](https://github.com/educates/educates-training-platform) 本地样例 | Workshop 将内容文件、运行时镜像和 session 配置分开，examiner 支持自动轮询。它也支持 cascade 的强制步骤链。 | 借鉴运行时与教学资产分离、自动检查；保持 Breakfix checkpoint 独立、无 Submit、无完成顺序。 |
| [The Art of Command Line](https://github.com/jlevy/the-art-of-command-line)、[Command-line Text Processing](https://github.com/learnbyexample/Command-line-text-processing) 与 [Awesome Sysadmin](https://github.com/awesome-foss/awesome-sysadmin) | 命令上下文、文本证据和系统管理工具是高频基础，但工具清单本身不是课程结构。 | 将它们嵌入日志、配置、进程和网络诊断，不出“记住 `grep`/`awk` 参数”的孤立题。 |

上述项目的许可证和内容形式并不相同，正式题库只保留来源链接和内容依据。题干、答案、初始化脚本、验证脚本和图片必须原创或另行确认可再利用的许可。

SadServers 的 `jakarta` 场景直接以 `google.com` 连通为目标；这恰好说明公网 DNS 不能成为 Breakfix 自动验证的前提。Breakfix 的 DNS、TLS、HTTP、SSH 和转发题必须自带 resolver、服务端、证书和预期路径，所有 checkpoint 仅观察环境私网状态。

### 首轮核心场景与长期目标

当前先定义 **24 道核心场景卡**：12 个 Topic 各两道，统一复用少数实验系统。它们不是已经可发布的题目，也不是每个 Topic 的固定配额；用途是先验证课程边界、运行时能力、题目资产结构和真实验证模式。只有通过 capability probe 的场景才能进入 candidate 制作，随后必须在真实 `generate -> answer -> checks` 环境中通过。

长期仍以 Linux Domain 积累到 80--100 道以上高质量题目为方向，但不预先设计一百个变体，也不以题数决定发布。每轮扩展都必须新增故障模型、证据类型、约束或合理的跨 Topic 组合；缺少独立价值的场景不进入题库。

下面是读者可直接浏览的 Topic 目录，而不是“每个 ID 必须对应若干题”的配额矩阵。一个 Topic 可以自然地包含很多题，也可以在缺少有意义场景时暂时很少；不得通过更换文件名、端口或变量来填满目标数量。

| Topic | 覆盖内容与可形成的场景 |
| --- | --- |
| Shell、文件与配置定位 | shell 执行上下文、PATH、文件层级、文本检索、配置发现和运行证据。 |
| 用户、组与权限 | 所有权、模式位、ACL、特殊目录、账户/组和 sudo 最小权限。 |
| 软件包与配置管理 | 软件包版本、仓库状态、配置语法、配置漂移和安全恢复。 |
| 进程与 systemd 服务 | 进程树、信号、后台任务、unit 生命周期、依赖、drop-in、执行用户、环境和工作目录。 |
| 日志与计划任务 | journal、应用日志、日志轮转、保留策略、cron、at 和 systemd timer。 |
| CPU、内存与资源限制 | load、CPU 争用、内存、swap、OOM、文件描述符、ulimit 和服务级限制。 |
| 磁盘与文件空间 | 磁盘空间、inode、deleted-but-open 文件、日志空间与可恢复的文件系统状态。 |
| 网络地址与路由 | 链路、地址、邻居、默认路由、路由表和多节点连通性诊断。 |
| DNS 与名称解析 | `/etc/hosts`、resolver、搜索域、记录、缓存和解析路径定位。 |
| 端口、TCP 与 HTTP 服务 | socket、监听地址、端口冲突、进程归属、TCP/HTTP 请求路径和服务暴露。 |
| TLS 与证书信任 | 证书有效期、主机名、SAN、信任链、客户端与服务端 TLS 配置。 |
| SSH 与远程运维 | sshd、密钥认证、`authorized_keys` 权限、client config、host key、ProxyJump、端口转发和安全文件传输。 |

`multi-node` 是场景拓扑 Tag，不是一个 Topic。多节点题按其根因归入 DNS、网络地址与路由、端口/TCP/HTTP、TLS 或 SSH；这样用户既能看到完整的 DNS/SSH 学习主题，也能筛选所有多节点事故。`incident`、`least-privilege` 和 `configuration-drift` 同理只表达跨 Topic 的约束或情境。

首发的推荐学习路径可以从“Shell、文件与配置定位”开始，经过“用户、组与权限”“软件包与配置管理”“进程与 systemd 服务”“日志与计划任务”，再进入资源、存储与网络 Topic；网络部分建议按“网络地址与路由 -> DNS -> 端口、TCP 与 HTTP -> TLS -> SSH”浏览。该顺序是导览，不是解锁规则；正式路线只由经 Topic 导读和题目内容证明的 `precedes` 边表达。

以下候选内容不进入当前 24 道核心场景，因为它们依赖当前非 privileged Incus system container 尚未证明可用的能力。capability probe 通过后再独立纳入目录，不能让它们拖慢或污染首轮制作。

| 后续候选 Topic | 需要先证明的能力 |
| --- | --- |
| 文件系统挂载与持久化 | 在该 profile 中安全地执行 `mount`/`fstab` 语义，不影响底层宿主机。 |
| 主机防火墙与流量过滤 | `nftables`/`iptables` 的 namespace 与 `CAP_NET_ADMIN` 行为。 |
| 抓包、MTU 与网络性能 | `CAP_NET_RAW`、可控流量和修改网络参数的隔离边界。 |

直接写 cgroup、创建 network namespace、loop device 和修改路由不属于基础 NodeEnvironment 的默认承诺。资源限制和路由类核心场景均标为 capability-gated，只有隔离行为被真实验证后才实施；不能把容器限制伪装成裸机权限。

### 题目形态与领域完成标准

题目可自然采用以下形态，具体由故障模型决定，不要求每个 Topic 都完整覆盖全部形态：

1. **状态识别**：从日志、状态、配置或网络现象形成正确假设。
2. **单故障诊断与修复**：一个根因、多个可观察证据和不限定的修复路径。
3. **受约束修复**：例如保持最小权限、不能中断另一服务、不能删除数据、必须保留 SSH 连通性。
4. **复合事故**：两个或三个相互关联但可区分的根因，要求先缩小故障域再恢复端到端状态。

首轮完成标准不是题数，而是 24 张场景卡均经过内容审查，已通过能力验证的场景都具备可运行 candidate、答案和真实验证报告；不具备能力的卡明确保留为 gated，不用伪实现替代。之后以五到十道已验证题为一个内容审查批次扩展。任一题重复、不可解释或无法在干净初态稳定验证时，必须替换为新的故障模型，不能以降低标准凑数。

题目清单应按 Topic 维护覆盖情况、场景根因、证据类型、约束、节点拓扑和验证结果，但不为 Topic 设定题数配额。一个 Topic 只有在能自然解释题目为何属于它时才收录该题；无法归类的题先回到 Topic 导读审查，而不是临时塞进 Tag。

### NodeEnvironment 能力验证前置项

当前 Node profile 是非 privileged、isolated-idmap 的 Incus system container，默认每节点为 1 CPU、512 MiB 内存、5 GiB 根盘、最多四个节点。candidate 制作前应先做一次专门的 runtime capability probe，并把结论写入 Domain 导读：

- [ ] 验证 systemd unit、timer、journal、APT 本地包/仓库、OpenSSH、低端口监听、进程信号、ACL、`/etc/hosts` 和多节点私网在学习与 verification 环境中的一致行为。题目不能依赖公网 APT 或外部镜像服务。
- [ ] 验证或明确排除 `nftables`/`iptables`、`tcpdump` 所需 capability、修改地址/路由、network namespace、mount/`fstab`、loop device、cgroup 写入和 resource limit 调整。未验证的能力只能保留为 gated 或后续候选内容，不能伪装成可发布题目。
- [ ] 所有 CPU、内存、I/O 和磁盘压力题使用有界、可自动回收的小规模负载；不得让一个 512 MiB/5 GiB 学习节点失控或影响其他环境。
- [ ] 所有 DNS、TLS、HTTP、SSH 和转发题都在题目私网内提供目标服务、证书和名称解析；禁止依赖公网、宿主机 DNS 或不可控外部网络。

### 一个领域如何做到完整

“完整”不是覆盖所有厂商产品，也不是堆到任意数字。一个领域发布前至少满足：

- 有一个经过人工审查的 Domain 导读和一组自然、边界清楚的 Topic 导读；Topic 不因技术实现而拆成难读的内部术语。
- 每个 Topic 的题目覆盖真实且不同的故障模型、证据类型或约束；不通过替换变量名、文件路径或命令参数制造重复题。
- 一个领域至少有 40 道真实、完整验证的 challenge；只有新增题能覆盖新的 Topic 内容、故障模型、证据类型或跨 Topic 组合时，才继续扩展到 80 至 100 道以上。
- 每道题有 Topic 关联、来源、渐进提示、可解释解答、参考答案和真实验证报告。面试追问写入 Topic 导读，不成为 Submit、分数或独立 runtime。
- challenge 的 checkpoint 只观察结果，不规定唯一命令、编辑器或完成顺序；`answer.sh` 必须在同一初态的真实 Environment 中通过所有 checkpoint。
- 领域内的目录、Topic 定义、Tag 用法和 Roadmap 关系能让新作者理解新增题应放在哪里；不能分类的题先回到内容审查，而不是用额外 Tag 掩盖问题。

## 后续执行清单

### P1：完成 Linux 系统与网络运维领域

- [x] 建立 Domain 导读、实验系统约定、12 个 Topic 导读和 24 张核心场景卡；它们是首轮内容审查输入，不是已发布题目。
- [ ] 将 Domain/Topic 导读、Tag、challenge、solution、hint、source citation 和内容审查清单固化为 `catalog/curriculum/` 与 release 模板。
- [ ] 完成上面的 runtime capability probe，并把每项结论回写到 Domain 导读和场景卡的能力状态。
- [ ] 先实现并真实验证 capability-ready 的核心场景；capability-gated 场景等待对应 probe 结论，不以降级题意或伪造运行时提前发布。
- [ ] 以五到十道已验证题为一个内容审查批次自然扩展；当 Linux Domain 已有至少 40 道真实、不同的题目后，再评估是否继续走向 80--100 道以上，不把未成熟的 Kubernetes、CI/CD 或云题混进该领域。
- [ ] 包含真正的多节点 SSH/网络故障题，验证 `NodeEnvironment` 的节点访问、私有网络、节点名和跨节点 checkpoint 语义；场景名称不能泄露 Incus。
- [ ] 每道候选题均在真实 `generate -> answer -> checks` 环境中通过，完成内容审查后再进入 Linux foundation release。

### P2：完成 Kubernetes 工作负载与服务运维领域

- [ ] 在 Linux 领域通过完成标准后，定义 Kubernetes Domain 导读和 Topic 目录，再持续生产至少 40 道真实 VK8s 题。
- [ ] 覆盖 workload/config、Service/DNS、调度与资源、事件/日志调试、RBAC、存储和 NetworkPolicy；每题明确证据来自哪些用户可见状态，而非唯一 YAML 写法。
- [ ] 控制平面、etcd、证书和 CNI 题不混入本领域，直到运行时能力与独立 Domain 设计都成熟。

### P3：完成 SRE 可靠性运维领域

- [ ] 以前两个领域中的真实服务和故障为素材，建立 SLI/SLO、观测信号、告警、容量、事故缓解、runbook、复盘和 toil 自动化的 Topic 目录。
- [ ] 先构造小型、受控的服务故障；只有资源与内容质量足够时，才采用 [OpenTelemetry Demo](https://github.com/open-telemetry/opentelemetry-demo) 或 [Online Boutique](https://github.com/GoogleCloudPlatform/microservices-demo) 的局部组件。
- [ ] 事故场景按“信号 -> 影响判断 -> 诊断 -> 缓解/恢复 -> 验证 -> 复盘”组织；自动 checkpoint 只判断技术状态，沟通与决策写入 Topic 导读和解答。

## 附录：研究来源与借鉴边界

| 来源 | 可借鉴内容 | 不应照搬 |
| --- | --- | --- |
| [Red Hat RHCSA objectives](https://www.redhat.com/en/services/training/ex200-red-hat-certified-system-administrator-rhcsa-exam) | Linux 主机运维的内容边界：工具、运行中系统、存储、服务、网络、用户/组和安全。 | 认证考点和厂商命令清单不能直接等同于课程或题目。 |
| [Linux Foundation CKA Domains & Competencies](https://training.linuxfoundation.org/certification/certified-kubernetes-administrator-cka/) | Kubernetes 的 cluster architecture、workloads/scheduling、services/networking、storage、troubleshooting 划分。 | 不把考试权重、限时和单一命令操作变成产品规则。 |
| [Google SRE Book](https://sre.google/sre-book/table-of-contents/) | SLO、监控、toil、事故与可靠性工程之间的关系。 | 不将大型组织流程或生产事故直接压缩为一条 shell 题。 |
| [roadmap.sh DevOps](https://roadmap.sh/devops) | 对操作系统、网络、容器、CI/CD、IaC、监控、云和安全的覆盖盘点。 | 它是广度检查清单，不是本项目的学习顺序或 Domain 契约。 |
| [bregman-arie/devops-exercises](https://github.com/bregman-arie/devops-exercises) | Linux、网络、Kubernetes、容器、可观测性等领域的面试追问与遗漏检查。 | 不复制问答，也不将其扁平主题列表变成题库结构。 |
| [Killercoda Scenario Examples](https://github.com/killercoda/scenario-examples) 与 [Educates](https://github.com/educates/educates-training-platform) | 题目资产、说明与运行时环境分离的方式。 | 不采用强制逐步验证，也不引入其完整平台复杂度。 |

所有公开资料只用于理解、覆盖盘点和结构参考。题干、答案、脚本、图片或其他具体内容的复用必须先单独核对许可；正式题库只收录可以独立解释、独立验证的原创或明确授权内容。
