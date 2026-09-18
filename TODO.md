# TODO

上一阶段"切库与阅读器统一切换"已于 2026-09-18 完成并验收：A 波四提交
（`79e4e07`、`324b9ac`、`aa02be1`、`882bd6f`）+ B 波三提交
（`65e121a`、`c7534dd` 及收尾提交）逐提交绿，收尾全量回归（docs-smoke 真实
854 页库、go test、documentation E2E 6/6、admin E2E 1/1）通过。更早阶段（管理
控制台重设计、管理控制面 v1、离线文档库生成器 docs-project-v10）同样见 git
历史；本文件只保留当前阶段与未立项的设计稿。

## 阅读器实践入口（当前阶段，2026-09-19 立项）

目标：已发布的 PracticeRevision 在阅读页可见可达——有实践的章节标题旁挂锚点
按钮，点击即启动临时环境与终端，结束后收起面板回到原文。实践是段落的延伸：
临时、即用、无档案。

核心决策（2026-09-19 与用户确认）：

- 入口：渲染后往有实践的标题插入锚点按钮（事件委托挂 article 容器）；
  ≤900px CSS 隐藏，移动端纯阅读，无入口无终端；
- 布局：默认 `目录 | 正文`，实践打开后目录让位收起、正文左移，右栏使用阅读器
  重建时预留的 rail 槽位显示实践面板；关闭面板恢复目录原状态；打开/关闭均
  scrollIntoView 锚定实践所属标题，对抗正文变窄的重排漂移；
- 点击即会话（无中间页）：find-or-create 环境 → 面板轮询 phase
  （Pending→Provisioning→Ready）→ 终端接入（连接期 lease 自动续期，现有行为）；
  面板 = 上部标题/目标/边界 + 可折叠步骤与观察点 + 下部终端与状态操作；
- 同时仅一个会话：点其他实践按钮自动结束上一会话（stop + toast）；关闭面板仅
  断开终端，环境按 IdleTTL 自然回收；面板提供显式"结束""重置"；再次点击同标题
  重新接上（find-or-create）；
- 无学习记录：实践在"我的空间"不留痕（my_space 对非 ops content-kind 的活跃
  环境本就静默跳过，零改动即所需），无 attempt/完成/断言求值，观察引导只是
  面板说明文本；
- 读者文本发布时固化：PracticeRevision 增可空读者投影（标题/目标/边界/步骤/
  观察点；步骤允许为空，容纳纯观察实践），发布路径从 plan 工件固化写入 revision
  JSONB（无 DDL）；步骤/观察点只取 instruction/description 文本，evidence 绑定
  不进投影；
- 存量唯一一条无投影的已发布记录不回填，读路径只返回带投影的记录（对读者
  不可见），不为此建更新路径；
- 未登录读者：入口可见，点击弹 AuthDialog，登录后自动继续（复用 ops
  pendingStart 模式）；
- 终端桌面端专用：运维工作台现有的移动端 Docs/Terminal 切换随本阶段一并
  移除（2026-09-19 与用户确认）——两个模块的移动端统一为纯查看，终端不再
  作为进度轮询的前提（放宽为页面可见即轮询）。

现状（2026-09-19 探查）：

- `document_practice_index`（`internal/adapter/postgres/
  schema_documentpractice.go`）append-only，唯一写者是
  `PublishPracticeRevision`；今天没有任何读路径（无 repository 查询、无端点），
  `document_practice_revisions` 也只在发布事务内做幂等检查；
- `PracticeRevision`（`internal/domain/documentpractice/model.go`）是薄引用
  （runnable/verification/manifest 引用），不含读者文本；UserSteps/Observations
  只存在于流水线 plan 工件；
- 环境与终端全部钉死 ops：`environmentObjectMeta` 硬编码
  `content-kind: operations`，无 ResolveDocumentationPracticeRevisionBinding，
  终端 ticket/WS 只存在于 `/api/operations/scenarios/{id}/…`；
- 阅读器：`.documentation-reader` 网格 `264px minmax(0,1fr) 0`，rail 槽位是空
  div；标题 ID 来自库 anchors[]（`assignHeadingIds`），插入点可直接对位；
  IntersectionObserver 只做 URL 同步，本线不动（无节区间激活逻辑）。

已于 2026-09-19 完成并验收：A 波两提交（`110c82f`、`f28afbb`）+ B 波两提交
（`a27086c`、`18394cf`）+ C 波三提交（`9e3d8b5`、`f55c2f8`、`79bd9ae`）逐提交
绿（另有 `50a3e49` 修复无 DB 时从未运行的 httpapi 环境生命周期测试基建）。
收尾全量回归（docs-smoke 真实 854 页库、test-unit 含 DB 用例、
ops/documentation/admin 三套 E2E、verify-generated）全绿。

提交拆解（A/B/C 三波；A、B 波完成即全绿检查点）

**A 波（读路径）**

### 提交 1 feat(docs): materialize the reader projection at publish time

- [x] `domain.PracticeRevision` 增可空读者投影字段（标题必填，目标/边界/步骤/
      观察点；步骤允许为空）；发布路径从 plan 工件固化写入 revision JSONB
- [x] 单测：plan 缺标题拒绝发布；投影随记录持久化且读回完整；存量无投影记录
      照常解码（可空）

### 提交 2 feat(api): serve published practices to the reader

- [x] `GET /api/documentation/practices?path=`：按 config 钉死的
      source/commit/language 查索引，只返回带投影的记录
      （`anchor`/`practice_id`/`title`），每页一次拉取；ETag 由返回集 digest
      合成，`If-None-Match` 命中 304
- [x] `GET /api/documentation/practices/{id}`：读者投影详情 + 运行时摘要
      （runtime/基础镜像，经 runnable revision ref 解析）；两者可选 JWT 公开读，
      与 page/tree/asset 同约定；OpenAPI 契约与 web client 同步生成进本提交
- [x] 单测：无投影记录不可见、未知 id 404、返回锚点与索引一致

**B 波（会话后端）**

### 提交 3 feat(api): run practice environments from published revisions

- [x] 环境服务的 content-kind 与 revision binding 参数化
      （findEnvironment/createEnvironment/environmentObjectMeta）；新增
      `ResolveDocumentationPracticeRevisionBinding`（读
      `document_practice_revisions` → runnable revision ref）；label 值
      `documentation-practice`（与 runnable Kind 一致），Purpose learning，确定性
      CR 名复用 learningEnvironmentName 模式
- [x] `POST …/practices/{id}/start`（find-or-create，对齐 ops 语义：
      Existing/Draining 恢复、非 Ready 等待 Ready）、`GET …/environment`
      （phase/终端节点信息）、`POST …/stop`、`POST …/reset`
      （Spec.ResetNonce++）；全部 JWT
- [x] ops 路径行为零变化（参数化而非重写）；单测：find-or-create 幂等、并发
      启动由确定性 CR 名围栏、ops kind 不受影响

### 提交 4 feat(api): attach practice terminals

- [x] `POST …/practices/{id}/terminal-ticket` + `GET …/terminal?ticket=`
      镜像 ops 双端点：一次性 ticket（哈希存储、1 分钟 TTL）+ WebSocket（origin
      允许清单、resize/data/ready 协议、连接期 lease 自动续期）；环境解析改为
      按 (user, content-kind, content-id) 共享，ticket 存储与终端连接记账原样
      复用
- [x] 单测：ticket 一次性与过期拒绝、origin 不在允许清单拒绝、未知实践 404

**C 波（前端 + E2E）**

### 提交 5 feat(web): open the practice panel from heading anchors

- [x] 渲染后按页级 practices 索引往有实践的标题插按钮（事件委托，
      `data-anchor`），≤900px CSS 隐藏；`.practice-open` 网格态
      （`0 minmax(0,1fr) minmax(360px,32%)`），打开时记忆目录原状态、关闭恢复；
      打开/关闭 scrollIntoView 锚定实践所属标题
- [x] 面板纯展示部分：标题/目标/边界 + 可折叠步骤 + 观察点（会话操作随提交 6）
- [x] 阅读器 E2E：按钮按锚点出现/消失、面板开合、布局切换与目录恢复、移动端
      宽度无按钮

### 提交 6 feat(web): run the practice session in the panel

- [x] `useTerminalSession`/`TerminalPane` 泛化（scenario 专用的基础路径与身份
      抽象为参数）；会话接线：未登录 AuthDialog 后自动继续、启动轮询 phase
      （准备环境中状态）、Ready 后终端接入、结束/重置、切换实践自动结束上一
      会话（toast）
- [x] E2E：启动 → Ready → 终端就绪 → 停止；关闭面板再进入重接；ops/
      documentation/admin E2E 回归

### 提交 7 refactor(web): retire the ops mobile terminal toggle

- [x] 删除 ScenarioWorkspace 的 mobileView 与 WorkspaceHeader 的 Docs/Terminal
      分段切换及配套 CSS（`.mobile-view-toggle`、`.workspace-body.mobile-*`
      窗格切换规则）；≤950px 终端窗格隐藏、文档窗格常显，侧栏图标栏不变；
      终端可见性计算简化为窄屏不连（移动端不建立 WS/PTY）
- [x] 进度轮询门槛由"终端已连接且页面可见"放宽为"页面可见"（移除后移动端
      只读查看时检查点列表仍会刷新）；此切换无 E2E 覆盖（2026-09-19 探查），
      ops E2E 回归证明桌面行为不变
- [x] 收尾全量回归：`make docs-smoke`、`make test-unit`、`make test-e2e`、
      `make test-e2e-documentation`、`make test-e2e-admin`、
      `make verify-generated` 全绿

### 边界

- ops 场景侧后端、admin 控制面、学习投影零变化（参数化不得改变 ops 行为，
  由 ops E2E 证明；ops 前端唯一变化是提交 7 的移动端终端移除）；实践环境在
  "我的空间"的静默跳过是本线期望行为，不是缺陷
- 无 attempt/完成记录、无服务端断言求值（若未来要"我实践过什么"，独立立项）
- 多源/多语言不动，practices 查询用 config 钉死的 source/commit/language；
  OpenAPI 只增不改
- 移动端无终端：阅读器无入口纯阅读，运维工作台 Docs/Terminal 切换随提交 7
  移除，终端桌面端专用

### 验收（完成后回填日期）

- 逐提交绿；收尾 docs-smoke（真实 854 页库）、test-unit、ops/documentation/
  admin 三套 E2E 全绿
- 手工：有已发布实践的页面上，按钮 → 面板 → 环境就绪 → 终端可操作 → 结束并
  回收；未登录点击路径登录后自动继续；关闭面板后目录恢复原状态

## 多页铺开与批次控制（设计稿，阅读器实践入口落地后细化为提交拆解）

前提（已落地 2026-09-18）：切库——部署携带全量库，树/页/锚点元数据在
manifest 里现成。

### 管理台"文档"分区：语料目录树

- 数据源 = 库全局 manifest 的 `tree`（上游侧栏结构：Concepts 185 页、Tasks
  211、Reference 344、Tutorials 43…共 854 页/12476 锚点）；管理台镜像上游
  结构，不建第二套层级、不做编辑策展（守住 NEXT.md"不建知识图谱"红线）
- 章节行 = 服务端聚合行（"已发布 x/N · 失败 y · 进行中 z"），子节点展开
  懒加载；单分区数百页，禁止一次性全量拉取
- 页面行复用既有行模式：标题 + 五色状态徽章 + 当前阶段/停留时长 + 行尾 ⋯
  菜单（重试/重启/强制失败/跳转工作流详情）；锚点级信息仅行展开（平均约
  15 锚点/页）
- "只看失败/卡住"过滤 + 标题搜索为日常主视图
- 与"工作流"标签的关系：文档分区 = documentation 工作流按语料组织的投影；
  工作流标签保留队列/救援视角，854 规模下自身需要分页与过滤

### 批次：范围控制的唯一载体

- 铺开不逐页手工触发：范围（页面/章节/全集）在树上勾选后以批次发起；批次 =
  声明式对象（范围 + 并发/限速/重试策略 + 状态）进流水线
- 单页操作 = 现有救援语义（重试/重启/强制失败）+ 发起单页批次
- 批次发起/暂停/取消/重试失败页均为人操作，全部进 admin 审计；不维护持久
  页面黑白名单——范围只是批次声明的一部分，用完留痕于审计
- 调度：并发/限速对齐 Incus 物化与验证环境容量；section 优先级仅作调度顺序，
  不构成用户可见排序，不滑向内容策展

### 后端语义与死代码

- 看门狗：runnable action 失败（attempts_exhausted/action_failed）自动映射
  工作流 Failed，替代人工 force-fail 兜底
- 删除 `ReviseAt`/`Revise` 与 `AcquireWorkflowLease`（含 LeaseOwner/
  LeaseExpiresAt 字段）：修订循环无生产路径，并发已由 StateVersion 乐观锁覆盖
- 工作流按页可查：创建时落 page_path/anchor 冗余列，或按 ContentID 建索引
  （细化时定）

### 前置 API 缺口（细化时补）

- 语料树 + 分区聚合端点；admin 工作流列表项补 page/anchor 身份字段（现仅在
  publication manifest 里）
- 批次对象端点（发起/暂停/取消/重试失败页）与审计 action 枚举扩展

## 挂起待决策（不排期）

- **内容治理/紧急下架**：场景侧非 authoring 内容无法下架、无管理员覆盖;实践内容
  PracticeRevision 一侧 append-only(索引只进不改)是刻意设计,与"紧急摘除"冲突。若做,
  方向是索引摘除/tombstone 而非删除数据——独立设计决策后另行立项,当前不做。
