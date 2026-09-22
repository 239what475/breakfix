# TODO

已完成的阶段见 git 历史(最近:GeneratorWorkspace 所有权化 7910656..a519fc9——CR 为唯一
owner、数据库降级为投影;交付记录与验收证据见 a519fc9 的 TODO 收口章)。当前阶段:死信
reap 退场。本文件保留当前阶段计划、未立项事项与挂起决策。

## 阶段:死信 reap 退场——封顶退避,永不放弃目标

### 定案

死信是消息队列保护管道吞吐的原语,这里没有吞吐可保:claim 按条扫描认领,卡住的记录不挡
任何人;退避封顶(5 分钟)后,一条卡死记录的成本约等于每天 288 次尝试。而 dead 换来的只有
"目标被遗忘":

- **不省工**:持 finalizer 的 Controller 以 `reapStatusRequeue=2s` 永久轮询 reap 记录直到
  `succeeded`;dead 记录永远不会再变化,轮询空转,被截肢的只有唯一能推进的执行者。
- **人工变三步**:finalizer 只在观察到 `ReapSucceeded` 后移除;dead 后运维要修底层资源、
  手工剥 CR finalizer,死信行还永不收口。反向设计只需一步:`ErrResourceAbsent` 判为成
  功,运维在集群侧修好/删掉卡住资源,下一轮(≤5 分钟)自动成功→记录收口→finalizer 移
  除→CR 消失→队列页清空,全程自动收敛。
- **不自洽**:Controller 与 GeneratorWorkspace 调和器都是 reconcile-forever;死信让 reap
  队列的消费者成了平台里唯一显式进入终态并放弃目标的路径(runnable_actions 的 claim
  上限是另一处同族的静默截断,记入未立项)。且 CR 才是目标持有者(Controller 每轮幂等
  re-Enqueue,连 DB 重置丢了记录都会被重建),队列只是工作通道——目标方永不放弃、通道
  方自我截肢,这个不对称本身就说明 dead 放错了位置。

可见性不需要终态:失败行的 attempt 列与既有派生高亮(非 succeeded 且 attempt≥4 的
`admin-row-stuck`)已覆盖"卡了很久,需要人看";LastError 保留。未立项清单中"死信 reap 的
人工重试动作"随之删除而不是立项——没有死信,就无需人工复活。

围栏与判定全部不变:UID+revision fence、资源已不存在判成功、诊断截断。

### 任务

- **域与 Reaper**:`internal/domain/runnable/reap.go` 删 `ReapDead` 与 `ReapQueue.Deadletter`
  (含 InMemory 实现);`internal/controller/runtimeenvironment/reaper.go` 删
  `reaperMaxAttempts`、`reaperDeadDiagnostic` 与 RunOnce 的 deadletter 分支——失败一律
  `Complete(false)` + `reaperRetryDelay`(5s 起倍增,第 7 次起封顶 5 分钟,此后恒 5 分钟)。
  Controller 侧零改动:它本就 reconcile-forever。
- **Postgres**:`runnable_repository.go` 删 `Deadletter` 实现,行状态允许集合(现 :949)同步
  去 dead;`schema_runnable.go` 的 CHECK 去 `'dead'`;baseline 57→58(破坏式,重置重建)。
- **API 与前端**:`openapi.yaml` 的 `AdminRunnableReap.state` 枚举去 dead→`make generate`;
  AdminEnvironmentsPage 回收队列注解改写(删死信表述),删 dead 态"下次尝试 —"分支;
  `admin.css` 删 `[data-state="dead"]` 徽标样式。
- **测试**:`reconciler_test.go` 的退避用例改为"封顶后恒 5 分钟、attempt 无上限仍回到
  queued";`runnable_repository_test.go` 删 Deadletter 用例;`AdminEnvironmentsPage.spec.ts`
  的 dead 徽标断言改为长期卡住行的高亮断言。

### 验收门槛

- 快车道全套:`make test-unit`、`make test-race`、`make verify-generated`、`make lint`、
  `kubectl kustomize .`、`make web-test-unit` 全绿。
- PostgreSQL 门控套件(schema 58 破坏迁移重建)通过。
- 行为断言:任何失败路径都不再产生终态放弃;退避序列 5s,10s,20s,40s,80s,160s 后恒
  300s。
- 不排 e2e:无新集群侧行为契约,快车道+PG 门控足够。

## 未立项事项

- runnable_actions 的 claim 上限(`attempt < 5`,基础设施失败按 ~1s 间隔重试)与死信同族
  但更隐蔽:耗尽后动作静默停留在 queued,永不再被认领,无终态转移无可见性表面(永久失败
  有显式 failed,耗尽路径无声)。退上限或显式转 failed,另行分析立项;
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
