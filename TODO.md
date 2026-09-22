# TODO

已完成的阶段见 git 历史(最近:遗留词汇清理 0672621..a8e8ab5、playground 边界定案 6163113、
依赖卫生 3755635)。本文件保留当前阶段与未立项事项。

## 阶段:GeneratorWorkspace 所有权化——CR 为唯一 owner,数据库降级为投影(2026-09-22 定案,本文件即执行计划)

### 0. 背景与定案(与用户逐条确认)

1. **现状是记录驱动的单向清理**:生成工作区 = DB 行(pending/active/deleting/deleted 四态,
   无 retired——RetireXxx 系列是转入 deleting 的方法名)+ PVC(`breakfix.dev/workflow`
   标签)+ OpenSandbox 沙箱(`breakfix.generator_workspace_id` 元数据)。
   WorkspaceReaper 每分钟跑 CleanupDue(过期 pending/deleting/终态 → Cleanup:删沙箱——
   行内缺 ID 时 FindWorkspace 按元数据反查兜底——→删 PVC→标 deleted),重启时 Recover()
   将未完成工作区转入 deleting。**记录在,清理健全。**
2. **缺口是 owner 与资源不同生共死**:破坏式 schema 迁移(schema.go 明言 "a previous
   schema must be reset")会把全部行 mem::forget——本周 baseline 连升两级即是两次实例。
   行没了,集群里的 PVC 与 OpenSandbox 沙箱成永久孤儿:reaper 遍历数据库,再也看不见它们。
   生产侧任何一次 DB 重置/恢复同理。
3. **定案(参照 Rust 所有权)**:owner 必须与资源同生共死——把所有权写进资源所在的集群。
   新增 GeneratorWorkspace CR(breakfix.dev/v2,名 = workspace ID):PVC 挂
   ownerReferences 级联删除(k8s 原生 drop);沙箱是集群外资源,finalizer 保证先删外部
   再让 CR 消失;DB 行降级为投影(轮次围栏/空闲/快照等服务内部状态照旧),升库只丢缓存,
   CR 是收养依据。此前讨论的"标签对账清扫"降级为 leak-sanitizer 兜底,不再是清理机制。
4. **reconcile 放 Server 侧**:OpenSandbox 凭据与生成流程只在 Server 装配
   (server.go:117,凭据为空时生成侧整体关闭);Controller 保持"只调和
   RuntimeEnvironment"的宪章不动。

### 1. 契约定稿

- **CR 形态**:api/v2 增 GeneratorWorkspace(+List):spec 最小,status 携带所有权事实
  (workflowID/namespace/pvcName/sandboxID/phase);finalizer
  `breakfix.dev/workspace-cleanup`;controller-gen object+crd(make generate),
  deploy/crds 增 `breakfix.dev_generatorworkspaces.yaml`;rbac.yaml 的 breakfix-server
  ClusterRole 增 generatorworkspaces(+status,+finalizers)——server 已有 PVC CRUD 与
  runtimeenvironments 全权(rbac.yaml:19-36),纯增量。
- **生命周期映射(Manager 改造为 CR-first)**:Ensure 写 pending 行后即建 CR → PVC 创建时
  挂 ownerReferences(同 namespace,生成 namespace 单一)→ CreateWorkspace 元数据在
  workspace id key 之外增应用级 `breakfix.app=generator`(兜底列表的过滤面,避免扫到
  同账号其他应用)→ 沙箱 ID 回填 CR status;Retire/Cleanup → CR phase=deleting → 调和器
  删沙箱 → 删 CR(finalizer 先行,k8s GC 级联删 PVC)→ DB 标 deleted。EnsureFresh 的换
  沙箱分支即所有权 move:旧 CR 走 deleting,新 CR 并行。
- **调和器**:Server 内 ticker(照 WorkspaceReaper 分钟节奏,OnTick 进服务注册表),
  controller-runtime client(模块依赖已在,Server 首次引入);两个方向:CR→资源执行
  drop;DB↔CR 对账——CR 在而 DB 无行(升库后)→按 CR 重建投影行并直接转 deleting。
- **leak-sanitizer 兜底**:周期以 `breakfix.app=generator` 元数据 ListSandboxes,与 CR
  集合求差,无主者告警+删除并记审计;存量活工作区按 DB 行一次性补建 CR(与收养路径同一
  实现);既无行又无 CR 的历史孤儿接受人工清单——不做全租户扫描。
- **领域状态机不动**:四态与轮次围栏(ActiveTurnID)/空闲(IdleSince)/快照引用语义
  原样,改动只在"哪些事实以谁为准"。

### 2. 提交 1:CRD 类型与授权(纯增量)

- [ ] api/v2 generator_workspace_types.go(+deepcopy 经 make generate)、
      deploy/crds/breakfix.dev_generatorworkspaces.yaml;
- [ ] rbac.yaml breakfix-server ClusterRole 增 generatorworkspaces、
      generatorworkspaces/status、generatorworkspaces/finalizers;
- [ ] 单测:status 必填与 phase 枚举校验;
- 验证:`make generate`;`make verify-generated`;`make test-unit`;`make lint`;
  `kubectl kustomize .`。

### 3. 提交 2:Manager 所有权化

- [ ] Ensure/EnsureFresh:建 CR、PVC ownerReferences、沙箱 app 元数据、status 回填;
      失败路径的补偿删除同步改 CR;
- [ ] Retire/Cleanup/Recover:状态转移写 CR;Recover 增收养(有 CR 无行→建行转
      deleting);
- [ ] opensandbox client:CreateWorkspace 元数据扩 key,单测同步;
- [ ] 单测改造:k8s fakeclient(ownerReferences 挂接与显式删除断言)+ OpenSandbox fake;
- 验证:快车道全套。

### 4. 提交 3:调和器、兜底与收口

- [ ] Server 调和器(ticker + controller-runtime client):执行 deleting CR 的 drop、
      DB↔CR 对账、服务注册表上报;
- [ ] leak-sanitizer:app key ListSandboxes 求差、告警+清理、审计;
- [ ] kind e2e(不依赖 OpenSandbox 凭据,k8s 侧即可证明):建 CR + 带 ownerRef 的 PVC →
      删 CR → 断言 PVC 级联消失;升库模拟(删 DB 行)→ 断言按 CR 收养清理;sanitizer 的
      OpenSandbox 行为以 fake 单测覆盖;
- [ ] 文档:README 架构边界(Server 调和 GeneratorWorkspace,Controller 宪章不变)、
      system-architecture;TODO 收口章。

### 5. 风险与对策

- **finalizer 卡死**(调和器不可达时 CR 删不掉):调和分钟级,OnTick 进服务注册表可见;
  runtime 侧已有同类语义经验。
- **同账号多应用误删**:sanitizer 只认 `breakfix.app=generator` 元数据,不做全列表。
- **存量迁移**:活工作区按行补 CR;无任何记录的历史孤儿人工清单,不为它们扩大扫描面。
- **Server 引入 controller-runtime client**:模块依赖已在(Controller 使用中),无新依赖。
- **e2e 边界**:OpenSandbox 行为全部 fake 单测;e2e 只断言 k8s 侧,不需凭据。

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
