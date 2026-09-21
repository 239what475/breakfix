# TODO

已完成的阶段见 git 历史(最近:用户 Playground 03eed97..c9a7f39——空白练习环境从文档库改绑
到用户,全站悬浮球入口)。本文件保留当前阶段与未立项事项。

## 阶段:Playground 治理——闸门与可见性(2026-09-22 交付)

### 交付记录

- **提交 1(42b651a)后端闸门与端点**:`playground.max_active`(config/app,默认 10,环境变量
  可覆盖)在 `POST /api/playground` 创建路径前做全站软闸门,达限返回 429 "playground is at
  capacity";计数口径与 findPlaygroundEnvironment 一致(content-kind 标签、blank、活跃相位、
  非 Deleting),Draining 在回收器完成前仍占额度;GET/reset/close 永不受限。新端点
  `GET /api/admin/runnable-reaps`(state/attempt/last_error/next_attempt_at/updated_at,
  updated_at 倒序,封顶 100)。My space `ActiveEnvironments` 增加 `kind: playground|operations`
  并追加 playground 条目(状态徽标 + 到期,无内容进度);openapi 破坏性演进,web 行组件同提交
  同步。顺带修复四个被 DB 跳过掩盖的存量测试/SQL 缺陷(playground helper switch 少空格、
  failed 相位断言、runnable_actions 过滤别名缺失、attempt-high 期望值)。
- **提交 2(333a601)admin 环境页与用户侧可见性**:AdminEnvironmentsPage(第三个页签"环境",
  路由 `/admin/environments`):总览卡(playground 占用/上限、占用用户、今日创建、按相位——
  前端聚合)、相位过滤表格、卡住高亮(Draining/Failed 超 10 分钟,纯 UI 启发式)、强制释放
  (确认对话框,走既有 release 端点,审计已有)、回收队列小节。AdminSystemStatus 增加
  `playground_max_active` 让总览卡显示真实配置值。球的 429 文案转译为可行动提示且按钮可重试。
- **提交 3 e2e 与收口**:prepare 显式设 `max_active="1"`;playground 套件尾段第二用户创建被
  429 拒、状态保持 none;admin 环境页冒烟(套件首账号在新库上即 bootstrap admin——核实 prepare
  每次重置数据库,被删的 admin e2e 工程的凭据机制不需要重建,冒烟并入 playground 套件,组件测
  已覆盖页面细节)。e2e 首跑暴露 Reset 后球会采用擦除前的陈旧 Ready(one poll interval 的闪烁,
  且会吃掉终端重连断言的整窗时间):`usePlayground` 在擦除未获 GET 确认前压制 ready 采用。

### 验收证据

- 快车道:`make test-unit`(42 包,postgres 全接)、`make test-race`、`make web-test-unit`
  (31 通过)、`make verify-generated`、`make lint` 全绿。
- `make test-e2e-documentation`:2 通过(reader smoke + playground 全链路:create→ready→
  终端 marker→reset→重连→第二用户 429→close→none→admin 环境页 "/ 1")。首跑失败定位为上述
  陈旧 ready 闪烁,修复后复跑通过。
- 关闭后无残留复核:runtimeenvironments 清空、无 breakfix-vk8s 命名空间、
  `runnable_reaps` 对应行 `succeeded`(attempt 7——重试链路本身被观测页覆盖)。

### 已知后续(不属本阶段)

- 容器级资源指标(Prometheus 接入后另立项)、平均使用时长等厚统计、按用户分层限额、
  轮询退避;playground 的 node/Incus 类型、多实例(集合 API)、终态断言(学习闭环)若做
  另行立项;
- 工作区崩溃孤儿沙箱/PVC 清扫(authoring 侧遗留);js-yaml ×3 等 Dependabot;ollama critical
  无上游修复;#15 typescript 7 等 vue-tsc 跟进;
- 首次真实发布后部署侧验证无凭证直拉。

## 挂起待决策(不排期)

- **内容治理/紧急下架**:场景侧非 authoring 内容无法下架、无管理员覆盖;索引 append-only 是
  刻意设计,与"紧急摘除"冲突。若做,方向是索引摘除/tombstone 而非删除数据——独立设计后另行
  立项,当前不做。
