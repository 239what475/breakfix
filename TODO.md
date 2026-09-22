# TODO

已完成的阶段见 git 历史(最近:GeneratorWorkspace 所有权化 7910656..本提交——CR 为唯一
owner、数据库降级为投影;交付记录与验收证据见本提交的 TODO 收口章)。本文件保留未立项
事项与挂起决策。

## 阶段:GeneratorWorkspace 所有权化——CR 为唯一 owner,数据库降级为投影(2026-09-22 交付)

### 交付记录

- **提交 1(7910656)CRD 类型与授权(纯增量)**:`breakfix.dev/v2` GeneratorWorkspace(+List),
  名 = workspace ID,spec 刻意为空(Server 是唯一写者,"存在即期望"),status 携带所有权
  事实(workflowID/namespace/pvcName 必填、sandboxID 可选、phase 枚举 Pending/Active/
  Deleting);finalizer `breakfix.dev/workspace-cleanup` 常量与类型同文件。scheme 注册、
  根 kustomization 部署 CRD、breakfix-server ClusterRole 增 generatorworkspaces 全权
  (+status,+finalizers)。
- **提交 2(f0e822e)Manager 所有权化**:Ensure 在建 PVC 前先 EnsureWorkspaceOwner(CR),
  PVC 挂 ownerReferences(uid 来自 CR);沙箱创建元数据增 `breakfix.app=generator`;
  沙箱 ID 落库即回填 CR status(不等激活),激活再回填一次;Record/Activate 失败的补偿
  删沙箱同步改写 CR status(清空 sandboxID)。Retire/Cleanup 先 DB 转 deleting 再标 CR,
  失败由 reaper 下一轮收敛;Cleanup 终态行重试 owner 删除。Recover 增收养:有 CR 无行
  →按 CR 事实重建投影行并直接转 deleting,再 retireOwner。opensandbox 的
  workspaceMetadata 抽出并加 app key。controller-runtime client 实现的
  GeneratorWorkspaceOwner 落在 kubernetes 适配器,Server 从共享 RESTConfig 装配。
- **提交 3(本提交)调和器、兜底与收口**:Server 内 WorkspaceReconciler(ticker 分钟级,
  OnTick 进服务注册表,与 reaper 同节奏):每轮先收养(CR 无行→建行转 deleting),再逐
  CR 调和——deleting 行执行 drop(删已记录沙箱→行标 deleted→EnsureWorkspacePVCOwner
  补挂存量 claim 的 owner 引用→移 finalizer 删 CR,k8s GC 级联收 PVC),deleted 行的
  CR 残留重放同一幂等 drop,活行上的 deleting CR 反向治愈(行是服务状态权威);
  DB→CR 方向为无 CR 的现存行(pending/active/deleting)补建 CR。leak-sanitizer:按
  `breakfix.app=generator` 分页 ListSandboxes,与 CR status 的 sandboxID 集合求差,
  告警+结构化审计记录+删除;行内缺 ID 不再 FindWorkspace 反查——无记录引用的沙箱归
  sanitizer,provider 不可达不会楔住集群侧级联。Cleanup 收敛为纯状态转移,drop 全权
  归调和器。rbac 为 PVC 增 patch(补挂 ownerRef 所需)。
- **schema baseline 56→57**:删 `generator_workspaces.workflow_id` 的外键——行是投影,
  收养(schema 全量重置后)必须在 workflow 行不存在时也能重建;真实集群验证了保留 FK
  时收养必然违反约束(23503)。
- **finalizer 卡死的两处实测缺口(来自真实 e2e)**:①status 为空的 CR 曾使
  ListWorkspaceOwners 整表报错并卡死 Server 启动——空/未知 phase 不再是列表级错误,
  呈现为"无事实 owner",由调和器跳过;②无事实 CR 的显式删除曾永久卡在 finalizer——
  调和器对 Terminating 且无事实的 CR 直接移除 finalizer 放行(无事实即无 Server 资源
  可丢),有事实仍不可重建的留人工清单。
- **kind e2e(`make test-workspace-ownership`,core profile 即可)**:级联证明——建 CR+
  带 ownerRef 的 PVC→删 CR→断言 PVC 被 GC;收养证明——CR(带 finalizer、status 事实、
  无 sandboxID)+owned PVC→删 DB 行→轮询至 CR 消失且行 state=deleted→断言 PVC 级联
  消失。脚本经 status 子资源写入事实(普通 apply 的 status 会被子资源剥离)。e2e-target
  的 reset 对每行工作区同步删除 CR(server 已缩容,finalizer 需显式解除)。

### 验收证据

- 快车道全套:`make test-unit`、`make test-race`、`make verify-generated`(生成物无
  漂移;web 侧本阶段零改动)、`make lint`(0 issues)、`kubectl kustomize .` 全绿。
- PostgreSQL 集成(schema 57 破坏迁移重建):`BREAKFIX_TEST_DATABASE_URL` 门控套件通过
  (含 generator_workspace 全生命周期仓储用例)。
- 真实 Kind 验收:`make test-workspace-ownership` 通过(e2e-breakfix target,core 依赖
  面):级联证明与收养证明均绿;OpenSandbox 行为全部由 fake 单测覆盖,凭据不参与。
- 调和器/sanitizer 单测:deleting CR 的 drop(沙箱删除、行完成、owner 显式删除、PVC
  owner 补挂计数)、deleted 行残留重放、活行补建 CR、无事实 terminating CR 放行、
  sanitizer 只删无主沙箱且幂等。

## 未立项事项

- 死信 reap 的人工重试动作(观测页已可见,处置留人工);
- 容器级资源指标(Prometheus 接入后另立项)、平均使用时长等厚统计、按用户分层限额、
  前端轮询退避;
- playground 的 node/Incus 类型、多实例(集合 API)若做另行立项;
- #15 typescript 7 等 vue-tsc 跟进(上游阻塞);ollama 警报若在模块图升至 v0.34.2 后仍有
  残留,按"传递依赖、未链接进产物"显式忽略——go mod why 证实无任何 ollama 包被引用
  (js-yaml ×3 与 shiki 遗留已随 2026-09-22 卫生批次解决);
- 首次真实发布后部署侧验证无凭证直拉。

## 挂起待决策(不排期)

- **内容治理/紧急下架**:场景侧非 authoring 内容无法下架、无管理员覆盖;索引 append-only 是
  刻意设计,与"紧急摘除"冲突。若做,方向是索引摘除/tombstone 而非删除数据——独立设计后另行
  立项,当前不做。
