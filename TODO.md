# TODO

已完成的阶段见 git 历史(最近:GeneratorWorkspace 所有权化 7910656..a519fc9——CR 为唯一
owner、数据库降级为投影;交付记录与验收证据见 a519fc9 的 TODO 收口章)。当前阶段:死信
reap 退场、动作队列诚实失败(两者独立,谁先做都行)。本文件保留当前阶段计划、未立项事项
与挂起决策。

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

## 阶段:动作队列诚实失败——退避、显式耗尽、失败传导

### 定案

runnable_actions 的缺陷是一条三环链(均已核实):

- **韧性窗口约 5 秒**:基础设施类失败写死 `next_run_at = now+1s` 平坦回队
  (runnable_repository.go `ReportRunnableActionFailure`),claim 扫描又有
  `attempt < 5` 截断(:222)——provider 抖 10 秒,在飞动作全部烧光重试预算;
- **耗尽即静默楔死**:行停留在 `queued`,永不再被认领,无终态转移无可见性(读起来像
  "排队中");且动作 key 由内容身份确定性派生、INSERT `ON CONFLICT DO NOTHING`,重调度
  永不重置——catalog 重装命中同一行,条目被永久毒化;
- **失败传导缺口(比前两环更根本)**:两个 resolver 都把 failed 与 pending 观察成同一个
  "还没好"——验证侧只按 record ID 查报告表,动作行根本不在查询里;物化侧虽 join 动作行
  但 WHERE 只匹配 `state='completed'`,failed 同样落进 `ErrMaterializationNotReady`。
  不仅楔死行,今天连显式 failed 的动作都会让工作流停在 Verifying、让 catalog 安装无限
  等待。

与 reap 的哲学对照决定方向:reap 之上没有任何角色能对失败做出反应,所以只能永不放弃;
动作之上有能反应的角色——生成工作流有 `StateFailed` 终态(workflow.go:37),catalog 安装
有既有 `FailRelease` 出口——所以诚实的设计是**有界耐心 + 显式失败**(k8s
progressDeadlineSeconds 模式),而非照搬无限重试。

归因两分是本阶段的判定轴:**内容之过**(artifact 类)不重试、失败结果缓存(fail-fast
重装是特性);**世界之过**(infrastructure 类)退避重试、耗尽显式转 failed、重调度可
自愈。验证报告携带 artifact 失败即动作 completed(失败是被记录的合法结果)——这条既有
诚实路径不动。

到期归因随此定案:执行 context 到期(被 `CreateTimeoutSeconds`/`MaxLifetimeSeconds`
砍掉)目前在 `reportFailure` 默认归 infrastructure(runner.go:186-190)——改为归
artifact:场景在自身批准期限内跑不完按内容之过处理,一次完成失败。否则 infra 重试会把
最坏耐心放大成尝试数 × 执行期限的乘积。

边界:单次执行的期限围栏、租约与续租、lease 过期回收、`runnerRetryDelay` 的 worker 主
循环节奏全部不动。

### 任务

- **重试与耗尽(postgres/runnable_repository.go)**:`ReportRunnableActionFailure` 的
  infra 分支按 attempt 指数退避(与 reap 共用同一形状,抽共享 helper:5s 起倍增、5 分钟
  封顶;上限建议 8 次,总耐心约一刻钟量级,实现时定);attempt 达上限时不再回队,同租约
  围栏内写 `state='failed'、failure_class='infrastructure'、
  failure_code='attempts-exhausted'`;`ClaimRunnableAction` 删 `AND attempt < ?` 截断
  ——耗尽由失败汇报显式转移,不再由扫描静默跳过。
- **重调度语义**:`ScheduleMaterialization`/`ScheduleVerification` 的 `ON CONFLICT` 由
  纯 `DO NOTHING` 改为条件 `DO UPDATE`:仅当现有行 `state='failed' AND
  failure_class='infrastructure'` 时重置 queued、attempt=0、清空 failure 三列;
  artifact 失败原样保留。
- **到期归因(worker/runnable/runner.go)**:`reportFailure` 将 `context.DeadlineExceeded`
  归为 artifact(code=`execution-deadline-exceeded`),不进 infra 重试。
- **失败传导**:`ResolveVerificationForAction`/`ResolveMaterializedRunnableRevision` 先查
  动作行,state='failed' 时返回携带 class/code/summary 的显式错误(新
  `ErrRunnableActionFailed`),而非"还没好";coordinator 对该错误走工作流 `StateFailed`
  既有终态,installer 走既有 `FailRelease`;`report.Passed=false → FailRelease` 的既有
  路径不变。
- **测试**:退避序列与封顶;耗尽显式 failed(class/code 断言);claim 扫描无截断
  (attempt 超限仍可认领,直至失败汇报显式转移);重调度对 infra-failed 重置、对
  artifact-failed 缓存;deadline-exceeded 归 artifact;coordinator/installer 对 failed
  动作不再等待、走各自失败路径;既有"报告携带 artifact 失败即完成"不回归。

### 验收门槛

- 快车道全套:`make test-unit`、`make test-race`、`make verify-generated`、`make lint`、
  `kubectl kustomize .`、`make web-test-unit` 全绿。
- PostgreSQL 门控套件通过。**零 schema 迁移、零 openapi 改动**:动作四态
  (queued/running/completed/failed)不动,`failure_class` CHECK
  ('','artifact','infrastructure') 仅用现值,AdminRunnableActionItem 的 state/failure
  字段为自由字符串——实现时核实无其他约束即成立。
- 行为断言:任何路径都不再产生"停在 queued 却永不被认领"的行;显式 failed 的动作让
  等待方在下一个观察周期走到失败出口,而非无限等待。
- 不排 e2e:无新集群侧行为契约。

## 未立项事项

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
