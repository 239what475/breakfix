# TODO

已完成的阶段见 git 历史；本文件保留当前阶段与未立项事项。

## E2E 剖面化：core/full（当前阶段，2026-09-20 立项）

背景：CI 重写前的测试改造。目标是把 E2E 公共链从 Incus 硬耦合中解出，引入
`BREAKFIX_E2E_PROFILE=core|full`（默认 full，本地现有工作流零变化）。core 剖面只依赖
Kind + in-cluster Registry/PostgreSQL，可在无 Incus 的环境（托管 CI、无 Incus 的开发机）
运行 ui/k8s/admin/documentation 套件；node、recovery 与 live acceptance 保留在 full
剖面。CI workflow 重写不在本阶段，剖面落地后另行立项。

关键事实（2026-09-20 探查，设计前提）：

- 文档实践场景的 runtime 在产品内硬编码为 k8s
  （internal/bootstrap/server/documentation.go 的 constraint `Runtime: RuntimeK8s`）——
  admin/documentation 套件运行时不碰 Incus，卡点只在公共 prepare 链；
- ReconnectableClient 全部方法 nil 安全（clientFor 报 "Node provider is not
  configured"）；provider 层已按 runtime 分派——ArtifactBuilder、EnvironmentProvider、
  VerificationProvider 的 k8s 路径不触碰 Incus client，worker 健康能力的注释明确
  能力探针不是 readiness 门；
- config/app/in-cluster.yaml 的 incus.endpoint 本来就是 `${BREAKFIX_INCUS_ENDPOINT}`
  从 runtime Secret 注入——endpoint 为空即未启用，天然就是开关，无需拆配置文件；
- 耦合点清单：config 三个进程验证器无条件 `Incus.Validate()`；server/controller/
  runtime-worker 三个 bootstrap 无条件构造 client；部署清单的 incus secretKeyRef 非
  optional、incus-tls secret 卷非 optional；e2e-target.sh 的 preflight/ensure-incus/
  configure-runtime/reset、run-e2e.sh 的工具清单与诊断采集、runtime.sh 的 endpoint
  端口校验与 NetworkPolicy incus 端口注入；
- recovery 两条 spec 断言 incus PTY 终端重连与 node answer 完成恢复——这正是 node
  专属恢复路径，保留 node fixture（评估过换 k8s fixture：会丢掉终端重连覆盖，不换）；
- 套件剖面归属：core = ui、k8s、documentation、admin、acceptance-k8s（live 门禁另由
  RUN_AGENT_LIVE_E2E 把守）；full-only = node、recovery、acceptance-node、
  acceptance-mcp、acceptance-interruption、agent-assistant、agent-soak。

提交拆解：

### 提交 1 feat(config): make the Incus provider optional ✅ ad0ab47

- [x] `incus.Config.Enabled()`：endpoint 非空即启用；`Validate()` 对未启用配置放行，
      非空配置维持现状严校验；`NewReconnectableClient` 拒绝未启用配置（防构造出
      必然失败的 client）；
- [x] server/controller/runtime-worker 三个 bootstrap 仅在启用时构造 client，未启用
      传递 nil（方法 nil 安全，node 操作报明确错误）；worker 的 node-provider 健康
      能力仅在启用时注册；
- [x] 单测：空配置 Validate 通过、仅 endpoint 的部分配置拒绝、Enabled 判定、无 incus
      endpoint 的进程配置通过三个验证器。

### 提交 2 feat(deploy): optional Incus secret references ✅ 2ecfe9a

- [x] server/controller/runtime-worker 清单：incus 相关 secretKeyRef 全部
      `optional: true`，incus-tls secret 卷 `optional: true`——生产语义不变（Secret
      存在即挂载），core 目标不建 Incus Secret、runtime Secret 不带 incus 字段即可
      完整启动。

### 提交 3 test(e2e): profile-aware kind e2e chain

- [x] `BREAKFIX_E2E_PROFILE=core|full`（默认 full，非法值显式报错）；
- [x] e2e-target.sh：core 下 preflight 免 incus CLI/共享 project/基础镜像检查（改为
      要求 runtime Secret 不含 incus_endpoint，防带残留字段的目标误跑）、ensure-incus
      与 configure-runtime 的 incus 键跳过、reset 免 incus 工具与 project 清理、诊断
      免 incus 采集；full 下 preflight 追加 incus_endpoint 非空校验；
- [x] runtime.sh：incus endpoint 读取 null 安全；为空时跳过端口校验与 NetworkPolicy
      的 incus 端口注入；
- [x] run-e2e.sh：工具清单按剖面收紧；full-only 套件在 core 下启动即报"requires
      the full profile"；失败诊断的 incus 采集按剖面；
- [x] run-authoring-interruption-e2e.sh：core 剖面拒绝执行（node 场景）；
- [x] docs/operations/testing.md 记录两剖面、归属矩阵与 core 剖面的 Secret 前提。

验收（本地，含 Incus 的专用 target）：

- [ ] full 剖面全量回归与现状一致（ui/node/k8s/recovery/documentation/admin）；
- [ ] core 剖面（runtime Secret 去掉 incus 字段、无 Incus Secret、机器无 incus CLI）：
      e2e-prepare → ui、k8s、admin、documentation 全绿；node/recovery 启动即报需要
      full 剖面；
- [ ] make test-unit、verify-generated、web-test-unit 绿。

## 测试分层与 E2E 降级（剩余未完成部分，2026-09-19 立项）

目标与判定标准见 git 历史（32e1bb8 等）；已完成 A 波（提交 1–4）与 B 波的组件测试
部分（c68ee1e）。剩余：

### 提交 5 test(e2e): condense the reader suite into a thin smoke

- [ ] 薄 smoke：大纲走到 pod-lifecycle + 生成内容契约（h2#pod-lifetime、
      doc-alert、pre.shiki）+ 移动端菜单/抽屉 + 实践按钮恰好一个 + 移动端
      无入口；reader-fast 项目由 smoke 项目接替（依赖 practice 链）
- [ ] 删 5 条 reader fast 与 3 条实践 UI e2e；发布链尾部加一行按钮存在
      性；documentation 10→3

### 提交 6 test(e2e): merge watchdog and controls into one chain

- [ ] 合并链：park（worker 0）→ SQL-fail → watchdog 判 Failed → 批次
      （ingress@what-is + autoscale@algorithm-details companion）项跟随 →
      pause/resume/cancel 薄验证 → 第二批次重映射 Failed + companion 保
      活 → retry-failed → worker 回 → Published；重语义断言删（三层已
      覆盖）；admin 5→4

### 提交 7（可选，不阻塞验收） test(postgres): live ticks for watchdog and scheduler

- [ ] DB-gated 集成测试：真 watchdog tick + scheduler tick 对真 Postgres
      驱动 parked→Failed→条目跟随，进一步压薄合并链

## 挂起待决策（不排期）

- **内容治理/紧急下架**：场景侧非 authoring 内容无法下架、无管理员覆盖;实践内容
  PracticeRevision 一侧 append-only(索引只进不改)是刻意设计,与"紧急摘除"冲突。若做,
  方向是索引摘除/tombstone 而非删除数据——独立设计决策后另行立项,当前不做。
