# TODO

上一阶段"阅读器实践入口"已于 2026-09-19 完成并验收：A 波两提交（`110c82f`、
`f28afbb`）+ B 波两提交（`a27086c`、`18394cf`）+ C 波三提交（`9e3d8b5`、
`f55c2f8`、`79bd9ae`）逐提交绿（另有 `50a3e49` 修复从未运行的 httpapi 环境生命
周期测试基建），收尾全量回归（docs-smoke、test-unit、verify-generated、web 构建、
documentation E2E 10/10、ops E2E 3/3、admin E2E 1/1，验收人独立复跑）通过。
更早阶段（切库与阅读器统一切换、管理控制台重设计等）见 git 历史；本文件只保留
当前阶段与未立项事项。

## 多页铺开与批次控制（当前阶段，2026-09-19 立项）

目标：解除"单页单锚点"的配置钉死，文档实践工作流按页面铺开到全量库（854 页/
12476 锚点）；范围控制的唯一载体是批次——声明式对象（范围 + 并发策略 + 状态）
进流水线，发起后不依赖任何客户端；管理台"文档"分区按语料组织投影；部署携带
全量库。

关键事实（2026-09-19 探查，设计前提）：

- PlanReviewing/ArtifactReviewing/VerificationReviewing 是流水线内部的自动门禁
  （LLM 评审 + 确定性裁决），管理台只有 force-fail/restart 两个人操作——铺开
  不需要、也不引入人工审批流；
- 但整条 Agent 链（规划 + 2 计划评审 + 生成 + 2 工件评审，6 次 LLM 调用）同步
  执行在 `POST /api/documentation/practice` 的 HTTP 请求里，端点无页参数、页
  来自 config 钉死——批次点火必须由服务端持久执行者驱动，不能是前端循环或
  长请求；
- 容量现状：Worker 2 副本 × 每进程串行 1 action；Controller 供应并发 4；每个
  验证环境 1 namespace + 1 vcluster，无全局环境数上限；`runnable_actions` 纯
  FIFO、无优先级、无任何限流机制。action 只在 Agent 链走完后产生，因此
  Agent 链并发是唯一需要的节流阀，不做 action 级限速。

核心决策（2026-09-19 与用户确认）：

- 批次对象：`document_batches`（状态 + scope + 并发策略）+
  `document_batch_items`（页/锚/工作流/条目状态），模式对齐 catalog release
  的分层先例；批次状态机刻意最小：Pending → Running ⇄ Paused → Completed |
  Cancelled；失败是条目级，批次带计数收尾；
- 调度器 = Server 内与既有后台循环（action reconciler、catalog installer、
  学习投影、workspace reaper）同款的第五个循环 + 有界 goroutine 池：把在飞
  Agent 链补足到并发阀值（默认 2、上限 8）、Start 错误记条目 Failed + 摘要、
  条目终态回填、执行暂停/取消/重试；状态全部从 DB 推导、跨重启续跑；section
  优先级只是待调度条目的稳定排序，不构成用户可见排序，不滑向内容策展；
- 锚点规则：批次条目默认页级，锚点 = 页 manifest anchors[] 第一个 level-2 锚
  的确定性规则；index 页与无 level-2 锚的页剔除（记 no-anchor）；scope 支持
  显式 (page, anchor) 覆盖；
- 幂等：已有 Published 工作流的条目 Skipped(already-published)——索引
  append-only 禁止同锚重发；已有 Failed 工作流由"重试失败页"走 restart +
  重新入队；
- 取消语义：停止新点火，未启动条目 Cancelled，在飞条目跑到终态（不强制
  失败）；
- 看门狗：绑定 runnable action `state='failed'` 立即、`state='queued'` 且
  attempt≥5 在 stuck 预算宽限后 → 工作流 Failed；写 ledger 工件
  `watchdog.force_fail`（owner_role=system，含原因），不写 human_action_audits
  （人操作表只留给人）；Agent 阶段 dwell 不自动失败——调度器直接感知 Start
  错误，崩溃遗留的卡住工作流保留 stuck 旗标 + 人工 restart；
- 死代码删除：`Revise`/`ReviseAt`、ArtifactReviewing→Generating 转移、
  `AcquireWorkflowLease`、`lease_owner`/`lease_expires_at` 列（含配对 CHECK），
  均已确认仅测试调用者，并发由 StateVersion 乐观锁覆盖；
- 工作流页身份：`document_workflows` 加 (source_id, commit, language,
  page_path, anchor) 冗余列——选冗余列而非 ContentID 索引：语料聚合、按页
  过滤、条目关联都需要可 JOIN 的裸列（ContentID 只存在于 ID 字符串前缀）；
  存量一行迁移回填；
- 部署：全量库（29MB）走专用库镜像（不可变内容 + digest 寻址，与"解析离线
  完成、版本化、digest 固化"哲学同构），Makefile 串 docs-project → 库镜像 →
  部署；E2E 迷你库保留 ConfigMap 路径不动；config 删 page_path/anchor、保留
  库身份钉定（读者 API 依赖 PinnedContext），`NewPinnedLibrary` 页校验与
  `ReadPage` 钉死解除；
- 批次操作全部是人操作、全部进审计：新增 documentation.batch.create/pause/
  resume/cancel/retry 五个动词（审计枚举闭集扩展，走"先注册后写入"）；不维护
  持久页面黑白名单——范围只是批次声明的一部分，用完留痕于审计与批次记录；
- 全量批次的模型费用与天数（854 × 6 次 LLM 调用，并发 2 下数天级）是发起前
  的运营确认（先拿单个 section 试点），不是机制；
- worker 资源尺寸以实测为据：现有 requests/limits（500m/512Mi 请求、2C/2Gi
  上限）是无测量依据的初值；试点批次期间按"每副本 = Go 基线 + 单 action
  峰值"采样（materialize 与 verify 分开测，前者是重腿），尺寸表进运维文档
  并回填 manifest，给出"批次并发阀值 ↔ worker 副本数"配对建议。

现状（2026-09-19 探查，要点）：

- 点火：`fixedDocumentationApplication` 钉死 config 单页单锚，工作流 ID =
  document-workflow-<ContentID>；重放幂等（非 Planning 态直接返回）；
- runnable action 失败不映射工作流：对账 outbox 只查 `state='completed'`，
  attempts_exhausted/action_failed 仅为管理台只读 stuck 旗标，唯一兜底是人工
  force-fail；
- `document_workflows` 无页身份列、无二级索引；管理台工作流列表无分页无
  过滤，列表项无页身份（仅在 publication manifest JSON 里）；
- 库：manifest.json 222KB（树 90KB/854 节点，仅 title/path/children），磁盘
  29MB；部署侧 ConfigMap 仅映射 5 个 key（E2E 迷你库 316KB），Server 镜像内
  无库；
- 审计动词闭集 5 个；批次对象、语料聚合端点、按页查询均不存在。

提交 1–7 已于 2026-09-19 逐提交落地（`0cab872`、`ed21878`、`471cd5f`、
`65a337e`、`79d1100`、`1a97306`、`37293e3`），代码与配套单测齐备（documentpractice
包现行单测绿）；三套 E2E 的收尾全量回归尚未执行，归提交 8。同日决定：提交 8
暂停——单次验证成本（全量 prepare + 巨石串行用例）约 20 分钟，不可接受——
先完成下方"插入项"的 E2E 结构重构，再回来跑绿提交 8 与收尾回归。

提交拆解（A/B/C/D 四波；A、B、C 波完成即全绿检查点）

**A 波（清场：看门狗、死代码、页身份）**

### 提交 1 feat(docs): map runnable action failures onto workflows

- [x] 看门狗（reconcile 伴生循环）：绑定 action `state='failed'` 立即映射
      工作流 Failed；`state='queued'` 且 attempt≥5 在既有 2100s stuck 预算
      宽限后映射；写 ledger 工件 `watchdog.force_fail`（owner_role=system +
      原因），不动 human_action_audits
- [x] 单测：artifact 失败立即映射；attempt 耗尽快照后映射；运行中的第 5 次
      尝试不误杀（等结果或租约过期后再判）；终态工作流不动

### 提交 2 refactor(docs): remove the revision loop and workflow lease

- [x] 删 `Revise`/`ReviseAt` 与 ArtifactReviewing→Generating 转移、
      `AcquireWorkflowLease`（store 接口 + postgres 实现 + 内存假实现）、
      `lease_owner`/`lease_expires_at` 列与配对 CHECK（schema 版本递增）
- [x] 测试更新；grep 证明无生产调用者残留

### 提交 3 feat(docs): page identity on workflows and a pageable admin list

- [x] `document_workflows` 加 (source_id, commit, language, page_path,
      anchor) 列 + 索引，创建路径写入，存量一行迁移回填
- [x] 管理台工作流列表：keyset 分页 + state/page_path 过滤 + 列表项带
      page/anchor/title（title 从库 manifest 服务端解析）；OpenAPI 与 web
      client 同步生成进本提交
- [x] 单测：分页/过滤/回填

**B 波（部署解钉：全量库进部署）**

### 提交 4 feat(docs): carry the full library through a dedicated image

- [x] 库镜像构建与部署接线（Makefile：docs-project → 库镜像 → kustomize）；
      server.yaml 卷改造，readOnlyRootFilesystem 不破；E2E 迷你库 ConfigMap
      路径不动
- [x] config 删 page_path/anchor（校验与样例更新）；`NewPinnedLibrary` 去掉
      "钉死页必须在库中"启动校验；`ReadPage` 解除钉死页拒绝（digest 校验
      保持）
- [x] docs-smoke、E2E 准备脚本适配

**C 波（批次核心：对象 + 调度器）**

### 提交 5 feat(docs): declarative batches over the corpus

- [x] `document_batches`/`document_batch_items` 表 + 领域状态机（批次
      Pending→Running⇄Paused→Completed|Cancelled；条目 Pending→Scheduled→
      Running→Published|NoPractice|Rejected|Failed|Skipped|Cancelled）
- [x] scope 解析：sections/pages/full → 页集；锚点规则（第一个 level-2 锚；
      index 页与无 h2 页剔除记 no-anchor；显式 (page, anchor) 覆盖）；已有
      终态工作流的 Skipped/可重试语义
- [x] 审计动词扩展 + 端点：发起（202 + 校验）/列表/详情（条目分页）；
      单测：scope 解析、幂等、状态机围栏

### 提交 6 feat(docs): the batch scheduler loop

- [x] 后台循环 + 有界 goroutine 池：并发阀值（默认 2、上限 8）内补足点火；
      Start 错误 → 条目 Failed + 摘要；条目终态回填
- [x] 暂停/恢复/取消/重试失败页端点与语义（取消：在飞条目跑到终态；重试：
      restart + 重新入队）；崩溃续跑（状态全 DB 推导）；与看门狗/force-fail
      的边界——调度器只管本批工作流
- [x] 单测：并发上限围栏、暂停恢复、取消语义、重试失败页、崩溃恢复

**D 波（管理台 + E2E）**

### 提交 7 feat(web): the documentation corpus section in the admin console

- [x] "文档"分区：语料树（懒加载分区 + 聚合行：已发布 x/N · 失败 y · 进行中
      z · 无实践）+ 页面行（五色状态徽章、锚点级行展开、行尾 ⋯ 菜单救援
      操作）+ "只看失败/卡住"过滤与标题搜索（服务端）
- [x] 批次视图：列表 + 详情（条目分页、进度计数）+ 发起（树上勾选范围）/
      暂停/恢复/取消/重试操作（理由弹窗，复用既有确认模式）

### 插入项（2026-09-19 决定，先于提交 8）refactor(test): E2E 结构重构

设计（三层，2026-09-19 定稿并当日实施）：

- 拆分：admin 套件 = admin-setup（bootstrap admin 选举/复用，session 文件按
  target 命名）+ admin-fast（纯控制台断言 auth-roles / users-totp / audit-ui，
  可并行、可在已用 target 上秒级重跑，audit 数据 SQL seed）+ admin-chain
  （真链路 workflow-rescue / batch-rollout / batch-controls，单 worker；每个
  文件独占一个 (page, anchor) 对——pod-lifetime、autoscale 首锚 +
  ingress@terminology、ingress@what-is-ingress、ingress@prerequisites——互不
  依赖且覆盖 scope 的显式锚点覆盖；fresh target 上任意子集可 --grep）；
  documentation 套件 = reader-fast（纯阅读器渲染）+ practice-chain（发布链 +
  面板会话，沿用 ensurePracticePublished 复用）；子集入口
  npm run test:e2e:admin:{fast,chain} 与 :documentation:{reader,practice}
- 快速档：fixture 验证脚本 wait 180s→90s、pod 存活 300s→90s；busybox:1.36.1
  预载进 Kind 节点消掉 vcluster 内首拉；单链 5-6 分钟 → 目标 ≤2 分钟
  （materialize 段镜像层预缓存列为后续项）
- prepare 瘦身：web 构建按输入 stamp 化（仅 Go 改动时跳过多分钟的 vite
  构建）；e2e-prepare 新增 BREAKFIX_E2E_DEFER_SERVER_RESTART=1（跳过自身
  server 重启与投影等待，落盘 catalog reference），doc-prepare 全部 patch 后
  做唯一一次 server rollout + 投影等待 + mark-prepared——server 重启
  3 次→2 次、rollout 等待 3 次→1 次
- 契约：chain 场景假设 freshly reset target（沿用既有语义；fast 场景无此
  约束）；失败重跑只付单场景的链路时间

- [x] 实施已落地（2026-09-19；调试中另修三个产品 bug——批次 retry 审计
      主键冲突、语料汇总把超龄终态计为卡住、语料加载 watch 缺 immediate，
      以及 fixture 工件/实践 ID 未含锚点且超 K8s 标签 63 字节上限两个
      fixture 缺陷，均有回归测试或验收覆盖）
- [x] 验证（2026-09-19）：admin 8/8（套件 8.6 分钟）、documentation
      10/10（套件 5.4 分钟）；单链 1.5–3 分钟、fast 场景秒级。温缓存
      prepare 瘦身后实测 ≈5.0 分钟（镜像每 prepare 只加载一次、Registry
      不再重复重启、修掉 defer 改造引入的多余一轮部署），全套 admin
      端到端 11.3 分钟

### 提交 8 test(e2e): batch rollout end to end

（2026-09-19：迷你库已扩到 3 页；批次铺开/看门狗/暂停-取消-重试场景随
E2E 结构重构全部跑绿；收尾全量回归同日通过。）

- [x] E2E 迷你库扩到 2–3 页；批次发起 → 调度 → 发布 → 语料树聚合断言；
      暂停/取消/重试覆盖；看门狗场景（action 失败自动 Failed，替代人工
      force-fail 兜底）
- [x] 收尾全量回归：docs-smoke、test-unit、verify-generated、web 构建、
      ops/documentation/admin 三套 E2E 全绿

### 提交 9 docs(ops): size the runtime worker fleet from the pilot batch

（尺寸实测与 manifest 回填随 `129ca59` 落地，见
`docs/operations/worker-sizing.md`。）

- [x] 试点批次期间采样 worker 资源（kind 节点 docker stats，或补装
      metrics-server 用 kubectl top）：空转基线、materialize 密集段、verify
      密集段的 CPU/内存峰值；确认镜像层操作是否流式（内存是否驻留）
- [x] 尺寸表进运维文档（每副本基线 + 每 action 增量 + 推荐 requests/
      limits），manifest 数值按测量回填；"批次并发阀值 ↔ 副本数"配对建议
      （默认 2↔2）与扩容路径成文

### 边界

- ops 场景侧、阅读器读者侧零变化（批次是流水线/管理台侧；读者只受益于内容
  变多）
- 不做 action 级限速与队列优先级（Agent 链节流足够；要提速扩 Worker 副本）
- 不做人工审批流（三个 Reviewing 门禁保持自动 LLM + 确定性裁决）
- 多源/多语言不动；管理台镜像上游树，不建第二套层级、不做编辑策展
- 审计枚举只增（5 个批次动词）；索引 append-only 与已发布实践不受影响

### 验收（完成后回填日期）

已于 2026-09-19 完成并验收：提交 1–7（`0cab872`、`ed21878`、`471cd5f`、
`65a337e`、`79d1100`、`1a97306`、`37293e3`）逐提交绿；提交 8 场景随
`129ca59` 与 E2E 结构重构（`8c8eaed` + prepare 瘦身 `3a0197d`）落地，期间
修复三个产品缺陷（`358e890`：批次 retry 审计主键冲突、语料汇总超龄终态误
计卡住、语料加载 watch 缺 immediate）；提交 9 尺寸实测随 `129ca59`。收尾
全量回归（2026-09-19）通过：docs-smoke、test-unit 45 包、verify-generated、
web 构建、ui 3/3、node 3/3、k8s 1/1、recovery 2/2、documentation 10/10
（套件 4.8 分钟）、admin 8/8（套件 6.3 分钟）。

- 逐提交绿；收尾 docs-smoke、test-unit、verify-generated、web 构建、
  ops/documentation/admin 三套 E2E（含 ui/node/k8s/recovery 全套）
- 批次语义：pages 范围批次 E2E 跑通（发起→调度→发布→skip/语料聚合）；暂
  停→恢复续跑；取消在飞条目到终态（看门狗失败路径）；重试失败页；
  sections/full 解析与崩溃续跑由单测覆盖
- 看门狗：action 失败自动映射工作流 Failed（worker 停摆场景不再依赖人工
  force-fail）
- 管理台：语料树聚合正确、过滤搜索可用、批次操作全部留痕审计
- worker 尺寸：requests/limits 有试点批次实测依据，"并发阀值 ↔ 副本数"配对
  建议成文

## 挂起待决策（不排期）

- **内容治理/紧急下架**：场景侧非 authoring 内容无法下架、无管理员覆盖;实践内容
  PracticeRevision 一侧 append-only(索引只进不改)是刻意设计,与"紧急摘除"冲突。若做,
  方向是索引摘除/tombstone 而非删除数据——独立设计决策后另行立项,当前不做。
