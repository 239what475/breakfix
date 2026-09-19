# TODO

上一阶段"多页铺开与批次控制"已于 2026-09-19 完成并验收：提交 1–7
（`0cab872`、`ed21878`、`471cd5f`、`65a337e`、`79d1100`、`1a97306`、
`37293e3`）逐提交绿；提交 8 的批次/看门狗/控制流场景随 `129ca59` 与 E2E
结构重构（`8c8eaed` + prepare 瘦身 `3a0197d`）落地，期间修复三个产品缺陷
（`358e890`：批次 retry 审计主键冲突、语料汇总超龄终态误计卡住、语料加载
watch 缺 immediate）；提交 9 的 worker 尺寸实测随 `129ca59` 落地。收尾全量
回归通过：docs-smoke、test-unit 45 包、verify-generated、web 构建，ui
3/3、node 3/3、k8s 1/1、recovery 2/2、documentation 10/10、admin 8/8。
更早阶段（阅读器实践入口、切库与阅读器统一切换、管理控制台重设计等）见
git 历史；本文件保留当前阶段与未立项事项。

## 测试分层与 E2E 降级（当前阶段，2026-09-19 立项）

目标：E2E 断言迁到"故障根源所在的最低层"，E2E 只保留跨系统接线与真实
浏览器才有的行为（视口、布局、WebSocket 终端）。Go 三层（handler、
DB-gated、application fakes）已存在且比预期厚，本阶段 Go 侧只删不建；唯一
新基建是 web 前端 Vitest 层（当前零覆盖）。完成后 E2E 从 21 条降到 10 条
（5 条真链 + 阅读器薄 smoke + ui 3 条 smoke + setup），admin 链区 8.6 分钟
降到约 6 分钟，日常开发回路由 Vitest 秒级承担。

关键事实（2026-09-19 探查，设计前提）：

- handler 层 18 个测试文件已覆盖：普通角色对 admin 端点与 practice 点火
  的 403（auth_flow_test）、TOTP 重置的密码确认与轮换、force-fail/restart
  重复 409、批次端点+审计、语料搜索汇总、审计 keyset 分页；
- DB-gated 层 40+ 测试（BREAKFIX_TEST_DATABASE_URL，每测试一 schema）已
  覆盖 watchdog 映射、语料汇总 SQL、批次状态围栏；
- application 层 fakes 已覆盖调度器 pause/resume/cancel/retry；
- web 的 API client 是 openapi.yaml 生成的 hey-api client，与 Go server
  同源——契约漂移由生成链 + vue-tsc 编译 + handler 测试钉真实 JSON + 薄
  smoke 四重兜底，mock 降级的残余风险有界；
- happy-dom/jsdom 的盲区：布局、真实滚动、视口、WebSocket——刻意留在
  E2E。

核心决策（2026-09-19 与用户确认）：

- 判定标准：断言住在故障根源所在的最低层且只住一层（SQL→DB-gated、动词
  语义→handler、调度语义→application、组件行为→Vitest、跨系统接线与
  视口/终端→E2E）；
- Vitest 的 mock 边界切在生成的 client 模块（src/api/generated），不起
  msw/网络栈；
- 保留脊柱 5 链：发布全链（重启/证据/幂等重放/环境回收）、会话终端、
  控制台救援（瘦身）、批次发布（瘦身）、watchdog+controls 合并链；
- 生成内容契约（doc-alert/pre.shiki 类名来自外部 docs-project 生成器）
  本仓无可承载层，留在阅读器薄 smoke；
- 验收门槛：每个删除性提交附"被删 E2E 断言 → 新家"对照；A/B/C 波完成
  各跑全量回归；
- 不动：ui 3 条 smoke（公共门面唯一覆盖，低优先级可后并）、node/k8s/
  recovery/acceptance/agent-*（worker+incus+真实模型即产品本身）。

迁移清单：

- admin：auth-roles 整条删；users-totp、audit-ui 的 UI 部分降 Vitest 后
  整条删；workflow-rescue 删尾部纯 API 探测（audit 列表/queue 汇总/
  environments），stepper/溢出菜单/原因必填降 Vitest；batch-rollout 的
  控制台断言（语料树已发布计数、批次详情展开、只看失败过滤含"勾选后需
  再点应用"）降 Vitest；watchdog 与 controls 两场景并一条链——共用一次
  park：SQL-fail → watchdog 判 Failed（ledger watchdog.force_fail、无
  human 审计）→ 批次项跟随 Failed → pause/resume/cancel 薄验证（200+
  状态+审计计数，重语义删）→ companion 保活 → retry 到 Published；
- documentation：reader 的导航/URL/hash/前进后退、错误重试、非 docs 路径
  回退降 Vitest；实践锚点按钮唯一性、面板切换/Steps 折叠/冻结投影降
  Vitest；上述连同生成内容契约、移动端菜单、移动端无入口合成阅读器薄
  smoke（排 practice 链后复用已发布状态），发布链尾部留一行按钮存在性兜
  契约。

提交拆解（A/B/C 波完成即全绿检查点；D 波可选，不阻塞验收）

**A 波（清场 + Vitest 基建）**

### 提交 1 test(e2e): drop admin assertions covered by the lower tiers

- [x] 删 auth-roles fast spec（403 门禁与角色分配 handler 已逐条断言）；
      剪 workflow-rescue 尾部的审计列表/queue 汇总/environments 探测
      （handler 已覆盖），场景保留状态可见性与 DB 断言；admin 8→7

### 提交 2 test(web): scaffold vitest and cover the admin corpus section

- [x] web/ 装 vitest + @vue/test-utils + happy-dom；package.json 加
      test:unit；Makefile 加目标并纳入全量回归清单；mock 边界 = vi.mock
      生成的 client 模块
- [x] 第一批组件测试：语料区（树渲染、已发布计数、批次详情展开、只看
      失败过滤的应用提交语义，含路由已激活时 watch immediate 回归）

### 提交 3 test(web): console component tests replace the admin fast specs

- [x] 用户区 TOTP 重置对话框、审计区行渲染与 payload 展开、工作流列表
      stepper/终态无 stepper/溢出菜单/确认按钮原因必填
- [x] 删 users-totp、audit-ui 两条 e2e，摘除 admin-fast 项目与
      test:e2e:admin:fast 脚本；admin 7→5

**B 波（阅读器降级）**

### 提交 4 test(web): reader and practice panel component tests

- [x] 阅读器导航（大纲懒加载、URL 同步、前进后退、hash 用事件模拟）、
      加载失败与重试、非 docs 路径回退
- [x] 实践锚点按钮唯一性、面板开关与布局类切换、Steps 折叠、冻结投影
      渲染；提交小型 tree/页面 HTML/投影 fixture

### 提交 5 test(e2e): condense the reader suite into a thin smoke

- [x] 薄 smoke：大纲走到 pod-lifecycle + 生成内容契约（h2#pod-lifetime、
      doc-alert、pre.shiki）+ 移动端菜单/抽屉 + 实践按钮恰好一个 + 移动端
      无入口；reader-fast 项目由 smoke 项目接替（依赖 practice 链）
- [x] 删 5 条 reader fast 与 3 条实践 UI e2e；发布链尾部加一行按钮存在
      性；documentation 10→3

**C 波（控制台链合并）**

### 提交 6 test(e2e): merge watchdog and controls into one chain

- [x] 合并链：park（worker 0）→ SQL-fail → watchdog 判 Failed → 批次
      （ingress@what-is + autoscale@algorithm-details companion）项跟随 →
      pause/resume/cancel 薄验证 → 第二批次重映射 Failed + companion 保
      活 → retry-failed → worker 回 → Published；重语义断言删（三层已
      覆盖）；admin 5→4

**D 波（可选加固）**

### 提交 7（可选，不阻塞验收）test(postgres): live ticks for watchdog and scheduler

- [x] DB-gated 集成测试：真 watchdog tick + scheduler tick 对真 Postgres
      驱动 parked→Failed→条目跟随，进一步压薄合并链

## 挂起待决策（不排期）

- **内容治理/紧急下架**：场景侧非 authoring 内容无法下架、无管理员覆盖;实践内容
  PracticeRevision 一侧 append-only(索引只进不改)是刻意设计,与"紧急摘除"冲突。若做,
  方向是索引摘除/tombstone 而非删除数据——独立设计决策后另行立项,当前不做。
