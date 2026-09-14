# 下一阶段：公共运行底座与文档实践自动流水线

目标：先将当前带有运维场景语义的运行设施重构为真正公共的可运行底座，再从固定版本的 Kubernetes Hugo 文档中识别适合实践的学习单元，由纯 Agent 流水线生成、审核、真实验证并发布可运行示例。首个闭环只处理一个固定文档版本，不考虑文档更新后的增量维护。

## 真正公共的可运行基础设施

文档实践和可复现运维现场共享环境、artifact、终端、验证和回收能力，但不共享内容模型、审核流程、发布规则或索引。两个内容模块分别把自己的不可变 revision 编译为公共运行契约；Runtime Worker 和 Controller 不解释内容语义。

```text
OperationsScenarioRevision ── Operations Adapter ───────┐
                                                        ├── RunnableSpec ── Build ── ArtifactReference
PracticeRevision ─────────── Documentation Adapter ─────┘                              │
                                                                                       v
                                                                                RunnableRevision
                                                                                       │
                                                                  Runtime Worker ──────┤
                                                                                       v
                                                                                  Environment
                                                                                  ├── terminal
                                                                                  ├── logs/events/state
                                                                                  └── reset/stop/reap
```

### 分层职责

#### 内容层

内容层表达产品语义和用户体验，不直接操作 Provider，也不把另一产品的字段写入公共模型。

- `OperationsScenarioRevision`：现场、初始状态、故障证据、可选修复、学习辅助和运维 Catalog 元数据。
- `PracticeRevision`：文档来源、语言、版本、页面路径、标题锚点、学习目标、用户步骤、观察点和文档证据。
- 两者都产生不可变 revision，使用各自的审核、发布和索引流程，并通过各自适配器编译公共运行输入。

#### Runnable 层的两个不可变时点

公共模型必须区分构建前已经冻结的运行输入和构建后得到的 Provider artifact，不能在一个所谓“不可变 revision”中逐阶段回填字段。

`RunnableSpec` 在开始任何外部构建前冻结，至少包含：

```text
RunnableSpec
  ├── identity
  │     content_kind、content_id、content_revision
  ├── runtime_profile
  │     runtime、基础镜像、软件版本、资源、网络、拓扑和 profile_revision
  ├── source
  │     不可变 source archive、archive digest 和格式版本
  ├── initialization
  │     从干净环境建立初始状态的入口、目标位置、超时和平台执行边界
  ├── validation_plan
  │     有序阶段、动作、断言和结果协议
  └── lifecycle_policy
        创建、重置、停止、超时和回收策略
```

- `RunnableSpec` 使用规范化序列化计算 `spec_digest`；内容 revision、运行 profile、source archive 或计划中的任一字段变化，都必须产生新的 spec。
- `ArtifactReference` 是构建和运行时 artifact 发布完成后的结果类型，包含 runtime、不可变 Provider reference、artifact digest、`built_from_spec_digest` 和构建器版本；它不能反向修改 `RunnableSpec`，也不单独作为业务 aggregate 持久化。
- `RunnableRevision` 内嵌 `ArtifactReference`，是 `RunnableSpec/spec_digest` 与 artifact digest 的不可变绑定。验证环境和用户环境只接受完整的 `RunnableRevision`，并核验两个 digest 的关联。
- 构建和运行时 artifact 发布消费 `RunnableSpec`，可以产生内部阶段结果，但只有最终 `ArtifactReference` 形成后才能冻结 `RunnableRevision`；验证和启动环境消费 `RunnableRevision`。内容发布只保存对 `RunnableRevision` 的引用，不由 Runtime Worker 执行产品发布。
- `RunnableSpec` 是 runtime profile 和 lifecycle policy 的唯一规范来源；创建 Environment 时只复制并解析出实际 profile digest、deadline、idle TTL 和有效释放时间，Environment/CRD 与 Reaper 不重新解释或修改内容层策略。
- 公共层只通过 `content_kind/content_id/content_revision` 保留归属和审计关联，不能根据 `content_kind` 分支运行逻辑。

公共层只允许环境、动作、观察、断言、结果、artifact、租约、尝试次数、超时和生命周期等通用概念。它不能出现 `reproduction`、`reference repair`、`checkpoint`、`answer`、`solution`、`ScenarioPublishing` 等特定产品词汇，也不保存文档标题、运维标签、用户提示或 Catalog 字段。

#### Provider 层

Runtime Worker 和 Controller 只负责公共运行能力：

- 从 `RunnableSpec` 构建并发布 Node 或 Kind-Kubernetes artifact；
- 按 `RunnableRevision.runtime_profile` 创建隔离环境；
- 在声明的目标位置、权限、网络和资源限制内执行初始化及验证动作；
- 收集 stdout、stderr、日志、事件、资源状态和断言结果；
- 提供终端和只读观测接口；
- 处理 lease、超时、基础设施重试、reset、stop 和 reap。

Provider 层不能知道某个断言是在证明故障、验证修复，还是确认文档中的 API 行为。

### 通用执行与验证模型

公共验证模型由有序阶段、动作和断言组成，不再围绕 `generate.sh -> reproduce.sh -> answer.sh -> checks.sh` 固定流程设计。

```text
ValidationPlan
  └── phases[]
        ├── id / timeout
        ├── actions[]      允许改变环境的确定性动作
        └── assertions[]   只能观察的检查及其 assertion ID
```

- `ActionSpec` 包含稳定 ID、执行入口、目标位置、超时、平台执行边界和预期退出结果。
- `AssertionSpec` 包含稳定 ID、只读执行入口、目标位置、超时和平台执行边界。
- 执行边界由 runtime profile、Provider 身份、NetworkPolicy、沙箱和资源限制共同决定；artifact 不能通过自由文本扩大权限，Worker 在运行时再次强制校验。
- 阶段严格按顺序执行；阶段内部的并行性必须显式声明，默认顺序执行。
- 动作可以改变环境，断言只能观察；断言脚本不得执行或 source 动作脚本，也不能使用具有写权限的身份。

断言统一输出：

```json
{
  "assertions": [
    {
      "id": "pod-reaches-running",
      "satisfied": true,
      "summary": "Pod reached Running phase",
      "details": "optional diagnostic data"
    }
  ]
}
```

协议要求：

- 每个声明的 assertion ID 必须恰好报告一次；
- `satisfied: false` 是有效业务结果，不是协议错误，但会使机器计算的 `passed` 为 false；
- 未声明、重复或缺失的 ID，非 JSON 输出、越权修改以及动作结果不符合契约，属于 artifact failure；
- 公共验证器根据所有阶段的动作和断言结果计算 `passed`，Agent 和内容适配器都不能覆盖机器结果；
- `VerificationReport` 绑定 `runnable_revision_digest`、环境身份、执行 attempt、各阶段动作/断言结果以及日志和原始输出的不可变引用；不再复制一份独立的顶层机器断言结果。

公共层的最小类型为：

```text
RunnableSpec / ArtifactReference / RunnableRevision
ActionSpec / ActionResult
AssertionSpec / AssertionResult
ValidationPhase / ValidationPlan
VerificationReport
```

内容模块可以增加自己的解释和展示投影，但不能修改公共结果的含义。

### 两个内容模块的映射

运维现场编译为公共计划：

```text
初始化动作
  ↓
初始状态断言       ← 运维模块中的 reproduction evidence
  ↓
可选修复动作       ← 运维模块中的 reference repair / answer
  ↓
最终状态断言       ← 运维模块中的 checkpoint
```

文档实践编译为公共计划：

```text
初始化动作
  ↓
文档状态观察断言
  ↓
可选演示动作       ← 自动回放用户关键步骤
  ↓
结论断言
```

运维模块仍可在自己的领域和展示模型中使用“复现证据”“参考修复”和“检查点”，文档模块可以使用“观察点”和“学习断言”；这些名称不能下沉到公共 Runtime。

文档实践允许没有修复动作，也允许没有用户操作，但必须至少有一条从干净环境到可验证结论的自动路径。只能人工操作、无法被验证器回放的内容不能进入自动发布流程。

### 生命周期与无感回收

- 验证结束后，编排器只需持久化“环境已可释放”的事实；独立 Reaper 根据 lifecycle policy 异步执行 stop 和 reap。
- 清理状态、清理日志和重试诊断属于运行基础设施，不写入内容的 `VerificationReport` 或 `PublicationManifest`。
- 清理失败不能撤销机器验证结果、回滚内容发布或阻塞上层流程；Reaper 持续幂等重试，并通过内部指标和告警暴露积压。
- reset、stop 和 reap 仍必须使用稳定资源身份、lease fencing、超时和最大资源存活期，避免无感回收演变为资源泄漏。

### Environment CRD 设计（公共 API）

只保留一个 `breakfix.dev/v2` 的 `RuntimeEnvironment` CRD。Provider 由被引用的 `RunnableRevision.runtime_profile` 决定（`node` 或 `k8s`），不再维护两套几乎相同的 Environment schema，也不把 Provider SDK 类型暴露给 API。

#### Spec：Server 是唯一写入者

```yaml
spec:
  runnableRevisionRef: {id: <id>, digest: sha256:<digest>}
  purpose: learning | verification
  lease:
    renewedAt: <timestamp>       # 仅允许单调递增
    releaseAt: <timestamp>       # 可选；只允许单调递增
  resetNonce: <integer>          # 可选；Server 递增以请求一次 reset
```

`runnableRevisionRef` 和 `purpose` 创建后不可变；运行 profile、artifact、ValidationPlan 和生命周期策略都从该 revision 解析，CRD 不复制。`lease` 与 `resetNonce` 是 Server 唯一可变的控制面：Server 续租、请求释放或递增 reset nonce，Controller 不改写它们。用户归属放在 owner reference/受限 label 中，不增加内容字段。

#### Status：Controller 是唯一写入者

```yaml
status:
  phase: Pending | Provisioning | Ready | Draining | Released | Failed
  operation: None | Resetting
  observedGeneration: <integer>
  conditions: []
  runtime:
    provider: node | k8s
    profileDigest: sha256:<digest>
    resourceRefs: [{provider: <p>, kind: <k>, id: <id>}]
    endpointRefs: [{name: <terminal-or-kubeconfig>, ref: <controlled-ref>}]
  progress: {phaseId: <id>, attempt: <n>, reportRef: {id: <id>, digest: sha256:<digest>}}
  lifecycle: {expiresAt: <timestamp>, releasedAt: <timestamp>}
  failure: {class: artifact | infrastructure, component: <c>, reason: <r>, message: <bounded>, at: <timestamp>}
```

- `phase` 只描述基础设施生命周期；`operation=Resetting` 是可回到 `Ready` 的短操作，Controller 为每个新 `resetNonce` 最多执行一次，因此不破坏生命周期单向性。释放条件为 `releaseAt` 到期或 `min(deadlineAt, renewedAt+idleTTL)` 到期，进入 `Draining` 后不再接受新操作。
- Runtime Worker 将不可变 `VerificationReport` 写入持久化层；Controller 只投影当前阶段、attempt 和报告 ID/digest，不复制动作/断言结果。
- Controller 独占 finalizer 和 status；Reaper 只通过带 fencing 的内部队列执行 stop/reap，完成后回报 Controller，由 Controller 确认资源消失、写 `Released` 并移除 finalizer。清理失败只留在 `Draining` 和内部指标中，不改变报告或上层发布状态。
- Admission/CEL 只负责 strict schema、未知字段、不可变字段和单调时间；Server/Controller 负责 revision-digest 绑定、profile 解析和平台上限校验。

#### 替换策略

先排空并回收旧 `NodeEnvironment`/`VK8sEnvironment` v1 对象，再安装 v2 CRD、Controller、RBAC 和客户端；不做 conversion webhook、双写或旧字段兼容读取。短生命周期环境不迁移，仍被引用的 artifact、已发布内容和学习记录按保留策略保留。

### 需要移除的现有耦合

这次重构不增加兼容别名，而是直接切换公共契约并更新所有调用方：

- 从公共 `execution.Snapshot` 移除 `Reproduction`、`Checkpoints`、`ReferenceRepair`；
- 从公共 `VerificationReport` 移除 `Reproduction`、`Answers`、`Checkpoints` 专属字段；
- 将 `ScenarioID`、`ScenarioRevisionID` 和 `ScenarioPublishing` 从 Runtime Worker 输入及状态中移出；
- 将 `reproduce.sh`、`answer.sh`、`checks.sh` 的路径和协议编译移到 Operations 适配器；
- 将 Controller 中检查点的产品投影与公共环境状态分离；
- 将 `internal/domain/reproduction` 和 `internal/domain/checkpoint` 限制在 Operations 模块；
- Runtime Worker 只接受 `RunnableSpec`、`RunnableRevision` 和通用 `ValidationPlan`，不再导入 `content/scenario` 的业务校验；
- Runtime Worker 只发布运行时 artifact，Operations 与 Documentation 的内容发布分别由各自应用服务完成。

### 一次性破坏性重构策略

本项目不维护旧公共模型的双读、双写或兼容别名。重构采用一次性切换：

1. 定义 `RunnableSpec`、`ArtifactReference`、`RunnableRevision`、通用验证和报告契约，并固定规范化 digest 算法；
2. 将现有 Operations Scenario 编译器改造成第一个内容适配器，用现有场景覆盖观察型、修复型和多阶段验证；
3. 迁移 Runtime Worker、Controller、artifact 发布、环境状态、内部 API、持久化 schema 和测试到新契约；
4. 将 Operations 内容发布从 Runtime Worker 移回 Operations 应用服务，并删除 `ScenarioPublishing` runtime action；
5. 删除旧的运维语义字段、协议解析分支和公共别名；
6. 仅清理或重建明确可丢弃的本地数据库、候选 archive、临时运行记录和未发布 artifact，不迁移旧格式；
7. 公共底座通过验收后，再实现 `PracticeRevision` 适配器和文档实践自动流水线。

这次重构允许持久化 schema、内部 API 和 artifact 格式发生不兼容变化。旧的运维内容需要重新编译和重新验证；已发布内容及学习记录是否清理必须由部署范围显式决定，不能被迁移脚本默认删除。

### 公共底座验收标准

- 同一个 Runtime Worker 不通过 `if operations / if documentation` 分支即可构建和运行两种内容；
- 公共验证器可以执行观察型、修复型和多阶段动作型计划；
- 环境、artifact、终端、日志、断言、回收和重试逻辑不包含产品词汇；
- Operations 和 Documentation 只在各自适配器中编译计划，并在各自应用层解释验证结果；
- 任意验证结果都能通过 `RunnableRevision` digest、artifact digest 和 environment profile revision 重现；
- 新增第三种内容类型时只需增加内容适配器和上层产品流程，不修改 Runtime Worker 核心；
- Reaper 故障不会阻塞内容工作流，但资源积压可观测且能在 lease 接管后继续收敛。

## 文档实践的纯 Agent 自动流水线

公共底座稳定后，从固定版本的 Kubernetes 文档建立第一个无人审核闭环：

```text
固定 revision 的 website 源码
        ↓
Hugo 构建的只读文档镜像
        ↓
规划 Agent：浏览页面，提出学习单元和文档证据
        ↓
计划审核 Agent 集群：独立检查价值、边界和证据
        ↓
Server 确定性计划门禁：批准或拒绝 LearningUnitPlan
        ↓
场景 Agent：生成 PracticeCandidate、source archive 和 RunnableSpec
        ↓
产物审核 Agent 集群：审核实际文件、执行边界、动作、断言和计划一致性
        ↓
Server 确定性产物门禁：批准或拒绝不可变 candidate digest
        ↓
Runtime Worker：构建并发布不可变 runtime artifact
        ↓
编排器：冻结 RunnableRevision；Runtime Worker 在干净隔离环境中真实执行
        ↓
验证审核 Agent：核对机器结果、文档结论和可观察性
        ↓
Server 发布 finalizer：原子写入 PracticeRevision 与文档实践索引
```

### 自动维护的信任边界

- 本阶段明确不设置维护者审核；规划、审核、生成和验证解释由 Agent 完成，审核门禁与发布由 Server 确定性执行。
- Agent 只提出结构化 artifact、审核意见和状态转换请求。Server 的确定性代码负责 schema、digest、状态机、执行边界、租约、机器结果、审核门禁和发布前置条件，Agent 不能直接写数据库、发布索引或覆盖机器判定。
- 上游文档、页面正文、代码块、注释和 include 都是不可信证据数据，不是 Agent 指令。文档读取工具必须将内容与系统指令分离，忽略其中要求调用工具、泄露信息或改变目标的文本。
- 同一个 AgentRun 不能审核自己生成的 artifact；每个审核角色使用独立运行记录。模型、提示、工具和审核策略版本都进入审计元数据。
- 自动审核策略必须版本化，并明确硬性否决项、通过条件和意见冲突的处理方式。机器失败、证据缺失、执行边界越权和安全审核拒绝不能被任何 Agent 覆盖；approve/reject 由 Server 按固定规则确定性计算。

### 文档访问和计划约束

- Agent 不接收整个上游仓库作为一次性上下文；规划 Agent 通过受限只读工具按需浏览由固定 revision 构建的页面，并可读取同一 revision 的相关源码、示例或 include 文件。
- 文档工具只能访问配置中固定的镜像 origin 和源码快照，不能访问任意公网地址、修改或执行上游内容，也不能接触 Breakfix 用户数据、生产 API、凭据或用户终端。
- `DocumentContext` 固定来源、语言、版本、上游 commit、镜像 digest、渲染页面路径和标题锚点；源码证据另存 source path、内容 digest 和引用片段，不能混淆源码路径与渲染 URL。
- `LearningUnitPlan` 必须包含学习目标、场景边界和所需的运行环境约束（运行时、资源、网络与拓扑），以及每个动作和预期结果的 evidence references。规划 Agent 可以明确返回 `no_practice`，不能默认每个段落或代码块都生成实践。
- 场景 Agent 只能接收已批准的计划、其证据和由 Server 解析出的固定 runtime profile/执行边界，不能自行扩展目标、权限、网络访问或没有文档依据的行为。

### 生成后的独立产物审核

计划批准不能替代对实际生成物的审核。场景 Agent 完成后，编排器先冻结 candidate source archive 和 `RunnableSpec`，再以 digest 为单位启动新的审核集群。

产物审核至少独立覆盖：

- `PracticeCandidate`、source archive 和 `RunnableSpec` 是否都绑定同一个已批准计划版本和文档证据；
- 每个初始化动作、演示动作、断言和用户步骤能否逐项追溯到计划，是否扩展了学习目标；
- 实际入口、目标位置、镜像、网络、资源、超时和执行边界是否在平台允许范围内；
- 断言是否只读、结果协议是否完整，是否存在执行其他脚本、隐藏副作用、凭据或不受控下载；
- 用户可见步骤与自动回放路径是否语义一致，机器断言是否真的证明计划中的文档结论；
- archive 和 spec 的规范化 digest、格式版本及所有引用文件是否完整。

审核意见绑定确切的 `candidate_digest + spec_digest`。任何文件或 spec 变化都产生新版本并使旧批准失效；产物门禁只能通过全部审核所针对的同一版本。

### 持久化上下文和状态机

多个 Agent 共享逻辑上的 `WorkflowContext`，但不转发完整聊天记录。WorkflowContext 只是 append-only artifact ledger 的按 workflow 视图，不另建一份可变的完整上下文副本；每个阶段读取前序已确认版本，并追加自己的结构化结果。

```text
WorkflowContext
  ├── DocumentContext
  ├── LearningUnitPlan[] / PlanReviewBundle[] / PlanGateResult[]
  ├── PracticeCandidate[] / RunnableSpec[]
  ├── ArtifactReviewBundle[] / ArtifactGateResult[]
  ├── ArtifactReference / RunnableRevision
  ├── VerificationReport / VerificationReviewBundle
  └── PublicationManifest
```

主状态按以下顺序推进：

```text
Planning -> PlanReviewing -> Generating -> ArtifactReviewing
         -> MaterializingArtifact -> Verifying -> VerificationReviewing -> Publishing -> Published
```

- 每个状态转换都校验前序 artifact ID、版本、digest 和门禁结果；后续 Agent 不能静默覆盖历史结果或跳过审核、构建和验证。
- 计划、产物或验证审核被拒绝时，当前版本终止；编排器可以在有明确结构化反馈时创建下一版本，但必须有最大修订次数，不能无限自循环。
- 基础设施失败在同一阶段使用有上限的重试；语义、协议或安全失败不能伪装成基础设施重试。
- 编排器为每个角色生成最小必要上下文视图；共享上下文不保存隐藏思考过程，也不保存 JWT、用户数据、生产凭据和用户终端连接信息。

### 真实验证和发布

- Runtime Worker 从干净状态创建隔离环境，执行已批准 `RunnableRevision` 的确定性动作和断言；静态语法检查不能替代真实执行。
- 验证审核 Agent 读取不可变 `VerificationReport`、已批准计划和文档证据，检查现象是否可观察、机器结果是否足以支持文档结论；它不能重写 assertion result 或 `passed`。
- Server 只能针对通过计划门禁、产物门禁、机器验证和验证审核的确切 digest 执行发布。Server 在一个事务中写入不可变 `PracticeRevision`、`RunnableRevision` 引用和实践索引；发布由 Server finalizer 完成，不设置独立发布角色。
- `PublicationManifest` 保存 `DocumentContext`、`PracticeRevision`、`RunnableRevision`、environment profile、`VerificationReport`、各审核门禁结果及策略版本的 ID/digest 引用；不复制前序 artifact 的完整内容。
- 实践索引只把固定文档页面和锚点映射到已发布的 PracticeRevision，不复制或重新解析文档正文。Breakfix 继续通过独立文档 origin 和上下文脚本提供阅读。

## 本阶段不做

- 不设置人工审核或人工发布门禁；
- 不实现上游文档更新后的增量识别、影响分析和场景复验；
- 不批量提取正文或固定筛选所有代码块；
- 不要求每个段落或示例都生成实践场景；
- 不让清理结果进入内容验证或阻塞发布；
- 不在本阶段加入标题滚动识别、实践按钮和终端联动的交互增强。

## 实施任务列表

以下任务按依赖顺序执行。每项完成后勾选对应验证；没有通过该项验证时，不进入下一项。任务中的“公共”只表示运行底座，不表示把两个产品合并成一个内容模块。

勾选状态按当前代码与测试证据维护；未勾选项表示仍有实现或验收工作未完成。

### 1. 固定公共运行契约

- [x] 在 `internal/domain` 中定义 `RunnableSpec`、`ArtifactReference`、`RunnableRevision`、`ActionSpec`、`ActionResult`、`AssertionSpec`、`AssertionResult`、`ValidationPhase`、`ValidationPlan` 和 `VerificationReport`；`ArtifactReference` 作为 `RunnableRevision` 的内嵌值，不单独建业务 aggregate。
- [x] 为每个公共类型定义 JSON 字段、格式版本、必填字段、大小上限、枚举值、稳定 ID 规则和禁止字段；拒绝未知字段，避免 Agent 通过额外字段注入未声明行为。
- [x] 实现规范化序列化和 digest 计算，固定 `spec_digest`、source archive digest、artifact digest 和 `runnable_revision_digest` 的算法、输入范围及显示格式。
- [x] 实现 `RunnableSpec` 的完整校验：runtime profile、source、initialization、执行边界、validation plan 和 lifecycle policy 必须互相一致。
- [x] 实现 `ArtifactReference` 与 `built_from_spec_digest` 的绑定校验，禁止用另一个 spec 的构建产物拼接出新的 revision。
- [x] 实现 `RunnableRevision` 的不可变组合校验；验证环境、用户环境和内容发布只能接受完整且已核验的组合。
- [x] 固定平台执行边界：允许的目标位置、读写权限、网络范围、资源上限和超时上限；运行时重新校验，不信任 archive 中的自由文本声明，也不引入独立权限 Registry。
- [x] 实现通用断言协议解析：每个 ID 恰好一次，未知、重复、缺失、非 JSON 和超限输出分别返回确定性 artifact failure。
- [x] 实现公共 `passed` 计算，确保 Agent、适配器和上层产品不能覆盖机器结果；区分业务断言失败、协议失败和基础设施失败。
- [x] 为执行日志、原始输出、环境身份、attempt 和 artifact digest 定义不可变引用格式；阶段结果树是 `VerificationReport` 中唯一的动作/断言结果来源，不把清理状态写入报告。
- [x] 为上述类型补充纯 domain 单元测试、边界测试、digest 稳定性测试、未知字段测试和反序列化拒绝测试。

### 2. 将 Operations 编译为公共运行输入

- [x] 新增 Operations 内容适配器，将现有不可变场景 revision 编译为 `RunnableSpec`，并保留 Operations 自己的内容校验和展示投影。
- [x] 将现有 `reproduction`、`reference repair`、`answer`、`checkpoint` 和固定脚本路径编译为通用阶段、动作和断言；这些名称不进入公共 JSON。
- [x] 将 Node 多节点和 K8s management 位置映射为平台执行边界和 target location，禁止在公共 Worker 中出现 runtime/content 分支。
- [x] 将 Operations 的 archive、初始化入口、版本、拓扑、资源和网络快照完整写入 `RunnableSpec`，并为每次变更生成新 digest。
- [x] 保留 Operations 对验证结果的领域投影，使现有场景界面仍能显示复现证据和参考修复，但投影不得反向修改公共报告。
- [x] 用现有 Node、K8s、无参考修复和带参考修复 fixture 覆盖观察型、修复型和多阶段计划；补充编译失败和执行边界越权测试。

### 3. 迁移 Runtime Worker 与 Provider 执行器

- [x] 将 Runtime Action 的输入改为 `RunnableSpec`、阶段结果和通用 artifact 引用；公共层以 `MaterializeArtifact` 表示构建与 artifact 发布，移除 `ScenarioID`、`ScenarioRevisionID`、`ScenarioPublishing` 及其专属状态分支。
- [x] 将 action identity 改为公共的 content kind/id/revision、spec digest、阶段和 state version；基础设施 retry 不改变外部资源 identity。
- [x] 让公共 `MaterializeArtifact` executor 消费 `RunnableSpec` 和 source archive，内部可以拆分 build/publish，但对上层只输出绑定 spec digest 的 `ArtifactReference`。
- [x] 只有 artifact 构建和发布完成后，编排器才写入不可变 `RunnableRevision`；Worker 不在 revision 中逐阶段回填字段。
- [x] 让 Verify executor 只消费完整 `RunnableRevision` 与 `ValidationPlan`，按阶段执行动作和只读断言，并返回通用 `VerificationReport`。
- [x] 将 Node、K8s、Registry、Incus 和环境 provider 的调用参数改为公共 target、执行边界和 lifecycle 数据；SDK 类型不得泄漏到 domain。
- [x] 将断言执行身份限制为只读权限，将动作执行身份限制为已批准的执行边界；增加执行前后的资源和权限边界检查。
- [x] 保留 lease fencing、动作 deadline、幂等创建/获取、attempt 上限和 artifact failure 分类；语义失败不得自动当作基础设施重试。
- [x] 将 Operations 的内容发布移回 Operations application service；Runtime Worker 只负责构建、artifact、环境、验证和资源生命周期。
- [x] 为 Worker 增加观察型、修复型、多阶段、断言失败、协议失败、lease 丢失、重启接管和重复结果测试。

### 4. 迁移 Controller、Environment 与异步回收

- [x] 定义单一 `breakfix.dev/v2` `RuntimeEnvironment` CRD：`runnableRevisionRef`、purpose、lease、幂等 `resetNonce`、phase、operation、conditions、progress、failure 和稳定 resource/endpoint refs；Provider 从 `RunnableRevision.runtime_profile` 解析，不维护 Node/VK8s 两套 schema。
- [x] 为 CRD 增加严格 OpenAPI schema、admission/webhook 不可变性校验和合法 phase 单向转换；拒绝未知字段、digest/revision 不一致、倒退时间和超限生命周期参数。
- [x] 将 Controller 中 checkpoint 结果的 Operations 投影移出公共 Environment 状态；CRD progress 只保存当前阶段、attempt 和 `VerificationReport` 引用，不复制动作/断言结果树。
- [x] 明确 Server、Controller、Reaper 对 spec/status/finalizer/releaseAt 的写权限和并发条件，确保 Server 不能通过 CRD 修改绕过 Runtime Worker 的前置校验。
- [x] 为 reset、stop、reap 定义稳定资源 identity、lease fencing、超时、最大存活期和幂等完成协议。
- [x] 验证完成后只持久化“环境已可释放”事实，由独立 Reaper 异步领取和执行 stop/reap；清理失败不改变验证结果或发布状态。
- [x] 为 Reaper 增加断点恢复、lease 接管、重复执行、provider 暂时不可用、永久资源缺失和积压告警测试。
- [x] 验证终端、日志、事件、资源状态和只读观测接口不依赖 Operations 或 Documentation 的字段名称。
- [ ] 先排空并回收旧 `NodeEnvironment`/`VK8sEnvironment` v1 对象，再安装 `RuntimeEnvironment` v2 CRD、更新 RBAC/客户端和生成清单；不提供 conversion webhook、双写或旧字段兼容读取。
- [ ] 在 Kind 和 Incus 目标上各完成一次从干净环境创建、阶段执行、验证、重置、停止和回收的真实测试。

### 5. 迁移持久化、内部 API 与发布边界

- [x] 新增保存 `RunnableSpec`、内嵌的 `ArtifactReference`、`RunnableRevision`、阶段结果、验证报告 digest 和审核/发布前置条件的持久化记录；不为 `ArtifactReference` 建独立业务状态表。
- [x] 将 Runtime Worker 内部 API 改为公共契约，所有请求校验 spec/revision digest、lease credential、state version 和 action identity。
- [x] 删除 Runtime Worker 对 `content/scenario`、Operations repository 和产品发布状态的依赖；产品 application 通过公共引用读取运行结果。
- [ ] 将 Operations 发布事务改为由 Operations application 原子写入自己的 revision、active pointer 和索引；为未来 Documentation 发布保留独立入口。
- [ ] 提升 schema version，清理或重建明确可丢弃的本地数据库、候选 archive、临时运行记录和未发布 artifact；不提供旧公共模型的兼容读取。
- [ ] 明确已发布内容、历史学习记录和仍被 Environment 引用的 artifact 的保留边界，迁移脚本不得默认删除这些数据。
- [x] 更新 OpenAPI、CRD、内部 API 客户端、生成代码、配置和部署清单，并通过 `make verify-generated` 和 `kubectl kustomize .`。

### 6. 公共底座验收

- [ ] 全仓确认 Runtime Worker、Controller、Environment、artifact、terminal、logs、events、state、reset、stop、reap 和 retry 逻辑不包含产品词汇或内容分支。
- [x] 运行公共 domain、application、adapter、worker 和 controller 单元测试，覆盖至少一个 Operations observation-only、repair-style 和 multi-stage 计划。
- [ ] 在专用 Kind target 上完成构建、artifact 发布、真实验证、lease 接管、重启恢复和异步回收验收。
- [ ] 在 Node/Incus target 上完成相同流程，并确认完整 fingerprint、资源限制和回收重试行为。
- [x] 验证任何 `VerificationReport` 都能由 `RunnableRevision` digest、artifact digest 和 environment profile revision 重现。
- [x] 验证 `RuntimeEnvironment v2` 的 strict schema、不可变字段、phase/operation 转换、稳定资源引用和 lease 回收语义；CRD 不包含内容专属字段或完整验证结果。
- [x] 新增一个仅依赖公共适配器接口的最小 fake content kind，证明无需修改 Runtime Worker 核心即可执行。
- [ ] 完成公共底座切换后，再开始以下文档实践 Agent 任务；不在同一阶段并行引入文档领域字段。

### 7. 固定文档来源与只读 Agent 工具

- [ ] 固定 Kubernetes website source、repository、commit、版本、语言、许可证和 Hugo 构建镜像 digest；将构建出的只读镜像 digest 写入 `DocumentContext`。
- [ ] 为文档页面、标题锚点、源码文件、include、示例资源和渲染片段定义来源 ID、路径、digest、行/片段范围和引用关系。
- [ ] 提供只读页面浏览、页面元数据、源码片段和 include 读取工具；工具只允许固定 origin、固定 revision 和受限路径。
- [ ] 拒绝任意公网访问、源码写入、文档命令执行、用户数据读取、生产 API、凭据和用户终端访问。
- [ ] 将文档内容作为不可信数据传给 Agent，与系统指令和工具权限隔离；为提示注入、超长页面、循环读取和越权路径补测试。
- [ ] 对 Hugo 页面、标题锚点、源码证据和镜像 build-info 进行固定版本 smoke test；首版只覆盖一个 Pod 生命周期页面范围。

### 8. 持久化 Agent WorkflowContext 与策略

- [ ] 定义 append-only artifact ledger：`DocumentContext`、`LearningUnitPlan`、review bundle、确定性 gate result、candidate、`RunnableSpec`、`RunnableRevision`、`VerificationReport` 和 `PublicationManifest`；验证审核意见作为 `VerificationReviewBundle` 保存，由 Server finalizer 计算发布前置条件；`WorkflowContext` 只提供按 workflow 的逻辑视图，不另存完整副本。
- [ ] 为每个 artifact 固定 schema version、owner role、parent artifact ID、content revision、digest、created_at 和策略版本；后续阶段不能静默覆盖历史记录。
- [ ] 实现 Planning、PlanReviewing、Generating、ArtifactReviewing、MaterializingArtifact、Verifying、VerificationReviewing、Publishing、Published 及终止状态的状态机。
- [ ] 为每个状态转换定义确定性前置条件、幂等键、lease、最大重试、修订次数和失败分类；禁止通过 Agent 请求跳过阶段。
- [ ] 将模型、prompt、工具、审核规则和门禁策略版本写入 AgentRun 审计元数据，但不保存隐藏思考过程、JWT、生产凭据或用户终端连接信息。
- [ ] 对计划审核、产物审核和验证审核使用独立 AgentRun；同一个 AgentRun 不得审核自己生成的 artifact，审核后的 approve/reject 由 Server 按固定规则计算。
- [ ] 明确硬性否决项、通过条件、意见冲突、超时、模型失败和结构化输出失败的机器处理规则。

### 9. 规划与审核 Agent

- [ ] 实现规划 Agent：从页面和证据工具中提出 `LearningUnitPlan`，包含学习目标、边界、运行环境约束、用户步骤、观察点和逐项 evidence references。
- [ ] 允许规划 Agent 明确返回 `no_practice`；不按代码块、段落或页面数量强制生成场景。
- [ ] 实现独立计划审核 Agent，覆盖文档证据一致性、学习价值和范围；执行安全、资源和步骤契约由 Server 及后续产物审核确定性校验。
- [ ] 实现 Server 计划门禁：只根据结构化审核结果、引用证据和固定策略产生 `PlanGateResult` 的 `approve`/`reject`，保留意见、证据和策略版本，不得覆盖硬性否决。
- [ ] 限制场景 Agent 的输入视图为已批准计划、证据和由 Server 解析的固定 runtime profile/执行边界；测试其无法扩展目标、权限、网络或无证据行为。
- [ ] 为计划版本、审核 bundle 和门禁结果增加幂等、重试、拒绝后修订和最大修订次数测试。

### 10. 场景生成与实际产物审核

- [ ] 实现场景 Agent：生成 `PracticeCandidate`、source archive、`RunnableSpec`、用户步骤、观察点、动作、断言和 lifecycle policy；发布通过后才冻结 `PracticeRevision`。
- [ ] 生成后立即冻结 archive 和 spec digest；任何文件、计划引用、runtime profile、资源或验证计划变化都创建新 candidate 并使旧批准失效。
- [ ] 实现独立产物审核 Agent 集群，逐项核对计划绑定、文件完整性、入口和 target、镜像/网络/资源/超时、只读断言、结果协议、凭据和下载边界。
- [ ] 实现 Server 产物门禁：只能批准所有审核针对的同一个 `candidate_digest + spec_digest`；缺少审核、版本不一致或硬性拒绝时必须拒绝。
- [ ] 将 candidate 编译为公共 `RunnableSpec`，通过公共 Worker 的 `MaterializeArtifact` 构建和发布 artifact，再冻结 `RunnableRevision`；不复制 Operations 的产品字段。
- [ ] 为生成、产物审核、候选修订、digest 失效、拒绝反馈和重新生成补充持久化与集成测试。

### 11. 真实验证审核与文档实践发布

- [ ] 在干净隔离环境中回放已批准 `RunnableRevision`，执行初始化、用户关键步骤的自动路径和结论断言；静态语法检查只能作为辅助。
- [ ] 持久化不可变 `VerificationReport`，绑定 runnable revision、artifact、环境 profile、阶段动作/断言结果和原始输出引用；不写清理状态或重复保存顶层机器断言。
- [ ] 实现验证审核 Agent，核对文档证据、计划结论、机器断言和用户可观察现象；不能重写 assertion result 或机器 `passed`。
- [ ] 实现 Server 原子发布事务：只有计划门禁、产物门禁、构建、机器验证和验证审核全部通过，才能写入 PracticeRevision、RunnableRevision 引用和实践索引；发布由 Server finalizer 完成，不设置独立发布角色。
- [ ] `PublicationManifest` 保存 `DocumentContext`、practice revision、runnable revision、environment profile、verification report 和审核门禁结果的 ID/digest 引用及策略版本；不复制前序 artifact 的完整内容。
- [ ] 实践索引只保存页面/锚点到已发布实践 revision 的映射；阅读器仍通过独立 docs origin 和上下文脚本工作，不复制文档正文。
- [ ] 对发布幂等、重复请求、状态过期、digest 不匹配、部分事务失败、Worker 重启和 Reaper 同步运行补集成测试。

### 12. 文档实践端到端验收

- [ ] 使用固定 Kubernetes 文档 fixture 验证页面浏览、证据读取、规划、审核、门禁、场景生成和产物门禁完整链路。
- [ ] 在真实固定文档镜像和目标 Kind 环境中完成至少一个 Pod 生命周期实践：从干净状态创建资源，执行自动步骤，观察状态并通过结论断言。
- [ ] 验证无参考修复、无用户操作、多个阶段、断言失败、协议失败和环境基础设施失败的状态与报告语义。
- [ ] 验证用户步骤、自动回放和文档锚点的关联能由 `PublicationManifest` 复现；页面和实践索引均不能漂移到其他 revision。
- [ ] 验证 Agent 无法通过文档提示、生成 archive、审核意见或发布请求扩大工具权限、访问用户数据或绕过机器前置条件。
- [ ] 运行 `make test-unit`、文档构建/smoke test、Agent workflow 集成测试、Kind 真实运行测试和 `make verify-generated`。

## 提交清单

按以下顺序拆分提交。每个提交只完成一个可审查的边界，并在提交前完成该项对应的测试；不把公共底座迁移和 Agent 流水线混在同一个提交中。重构允许一次性破坏旧公共契约，但每个提交都应保持代码可构建、测试可运行，不能先删除唯一实现再等待后续提交补回能力。

1. **公共 Runnable 契约与 digest**：新增 `RunnableSpec`、`ArtifactReference`、`RunnableRevision`、通用验证类型、规范化 digest、执行边界校验和 domain 测试；不接入产品流程。
2. **Operations 编译适配器**：将现有 Operations revision 编译为 `RunnableSpec`，把旧 reproduction/answer/checkpoint 协议封装在 Operations 适配器中；完成 Node/K8s fixture 编译测试。
3. **通用 Worker action 与 Artifact materialization**：迁移 Runtime Action、Worker、Node/K8s builder 和 artifact publisher，移除产品发布字段；完成 lease、幂等和 digest 绑定测试。
4. **通用 Validation 与 Provider 执行**：迁移验证器为阶段/动作/断言模型，接入只读断言权限、执行边界校验、公共报告协议和 Node/K8s provider；完成观察型、修复型和多阶段单元测试。
5. **RuntimeEnvironment/Controller 与异步 Reaper**：切换单一 `breakfix.dev/v2` `RuntimeEnvironment` CRD、Controller、终端/观测状态、reset/stop/reap 和资源 lease；完成严格 schema、不可变字段、Kind/Incus 生命周期与清理不阻塞测试。
6. **持久化和内部 API 切换**：迁移 schema、Runtime Worker HTTP、客户端、状态恢复和 artifact 记录；删除 `ScenarioPublishing`、Scenario identity 和旧报告字段，完成生成代码与 API 契约验证。
7. **Operations 发布边界与底座收尾**：将 Operations 内容发布事务移回 Operations application，清理旧公共别名和不可迁移本地数据，补齐公共底座文档、测试和真实运行验收。
8. **固定文档来源与只读工具**：固定文档 snapshot、镜像 digest、页面/锚点/源码证据模型和受限只读工具；完成提示注入、越权路径和固定页面 smoke test。
9. **Agent WorkflowContext 与状态机**：新增 append-only artifact ledger、状态转换、幂等/lease/retry、AgentRun 审计元数据和审核策略版本；完成失败恢复和拒绝修订测试。
10. **规划与计划审核流水线**：实现规划 Agent、独立计划审核 Agent 和 Server 计划门禁；完成 `no_practice`、证据追溯、硬性否决和意见冲突测试。
11. **场景生成与产物审核流水线**：实现 PracticeCandidate/source archive/RunnableSpec 生成、candidate digest 冻结、产物审核 Agent 集群和 Server 产物门禁；完成版本失效和执行边界越权测试。
12. **文档实践真实验证与发布**：接入公共 Worker 构建/验证、验证审核 Agent、原子 PracticeRevision/索引发布和 PublicationManifest；完成固定 Pod 生命周期实践的真实 Kind 验收。
13. **端到端验收与文档收尾**：补齐全链路 fixture、重启/重复请求/基础设施失败测试，更新 README、架构、API、运行和恢复文档，执行本阶段全部静态、构建、集成和真实环境检查。

每个提交的提交说明应包含：变更边界、更新的公共或内容契约、执行过的测试命令、已知未完成任务和是否产生不兼容的 schema/API/artifact 变化。每项任务在对应实现和验证完成后立即勾选；未完成项持续保留未勾选状态，不在最后一次提交中批量补勾。
