# TODO

已完成的阶段见 git 历史(最近:队列两阶段——死信 reap 退场与动作队列诚实失败,均于
2026-09-22 单提交交付;交付记录与验收证据见各自提交的 TODO 收口章)。本文件保留未立项
事项与挂起决策。

## 阶段:死信 reap 退场——封顶退避,永不放弃目标(2026-09-22 交付)

### 交付记录

- **域与 Reaper**:`internal/domain/runnable/reap.go` 删 `ReapDead` 与 `ReapQueue.Deadletter`
  (含 InMemory 实现),队列接口注释改写为"无终态放弃:目标持有者(CR)永不放弃,失败
  尝试一律回 queued 等运维修复底层资源";`internal/controller/runtimeenvironment/reaper.go`
  删 `reaperMaxAttempts`/`reaperDeadDiagnostic` 与 RunOnce 的 deadletter 分支——失败一律
  `Complete(false)` + `reaperRetryDelay`(5s 起倍增,第 7 次起恒 5 分钟,attempt 无上限)。
  Controller 侧零改动(本就 reconcile-forever,`ReapSucceeded` 仍是唯一收口判据)。
- **Postgres 与 schema**:`runnable_repository.go` 删 `Deadletter` 实现,`scanRunnableReap`
  行状态允许集合去 dead;`schema_runnable.go` 的 runnable_reaps CHECK 去 `'dead'`;
  baseline 57→58(破坏式,重置重建)。
- **API 与前端**:`openapi.yaml` 的 `AdminRunnableReap.state` 枚举去 dead → `make
  generate`(server.gen.go 与 web types 同步);AdminEnvironmentsPage 回收队列注解改写
  (封顶 5 分钟永不放弃、资源不存在计成功、长期未成功行需集群侧修复),删 dead 态
  "下次尝试 —"分支;`admin.css` 删 `[data-state="dead"]` 徽标样式。
- **测试**:`reconciler_test.go` 的退避用例重写为 `TestReaperRetriesForeverAtCappedBackoff`
  ——序列 5s/10s/20s/40s/80s/160s 后恒 300s,第 10 次尝试仍在 queued、带诊断、可认领;
  `runnable_repository_test.go` 死信用例重写为"8 次失败后仍 queued、围栏有效、远超历史
  上限仍可认领、观察面可见";`AdminEnvironmentsPage.spec.ts` dead 徽标断言改为长期卡住
  行的高亮断言(attempt 12 的 queued 行带 `admin-row-stuck` 且仍承诺下次尝试)。

### 验收证据

- 快车道全套:`make test-unit`、`make test-race`、`make verify-generated`、`make lint`
  (0 issues)、`kubectl kustomize .`、`make web-test-unit`(8 files / 51 tests)全绿。
- PostgreSQL 门控套件(schema 58 破坏迁移重建)通过。
- 行为断言:任何失败路径都不再产生终态放弃;退避序列 5s,10s,20s,40s,80s,160s 后恒
  300s;attempt 无上限仍回到 queued。
- 未排 e2e:无新集群侧行为契约,快车道+PG 门控足够(与计划一致)。

## 阶段:动作队列诚实失败——退避、显式耗尽、失败传导(2026-09-22 交付)

### 交付记录

- **共享退避曲线(domain/runnable/backoff.go)**:`RetryBackoff(base, attempt)`——5s 起倍增、
  `RetryBackoffCap`=5 分钟封顶;reaper 删本地 `reaperRetryDelay` 改用共享曲线,动作队列
  同曲线,平台一条耐心曲线两个消费者。
- **重试与耗尽(postgres/runnable_repository.go)**:`ReportRunnableActionFailure` 改事务
  实现——FOR UPDATE 锁行读 attempt,infra 分支按 `RetryBackoff(5s, attempt)` 回队并保留
  诊断;attempt 达 `runnableActionMaxAttempts=8` 时不再回队,写显式
  `state='failed'/failure_class='infrastructure'/failure_code='attempts-exhausted'`
  (调用方 summary 作为最后错误保留);artifact 失败照旧一次转 failed。退避间隔合计
  615s(5+10+20+40+80+160+300),最坏耐心为 8 次执行期限 + 615s。`ClaimRunnableAction`
  删 `AND attempt < ?` 截断——耗尽只由失败汇报显式转移,扫描不再静默跳过。
- **重调度语义**:两处 INSERT 的 `ON CONFLICT` 改条件 `DO UPDATE`(共享 SQL 片段
  `runnableActionReschedule`):仅现有行 `state='failed' AND failure_class='infrastructure'`
  时重置 queued、attempt=0、清空 failure 三列、next_run_at 取新调度时刻;artifact 失败
  原样缓存,重装 fail-fast 是特性。
- **到期归因(worker/runnable/runner.go)**:`reportFailure` 将
  `context.DeadlineExceeded`(含包装)归为 artifact、code=`execution-deadline-exceeded`,
  一次完成失败——infra 重试不再把最坏耐心放大成尝试数 × 执行期限。
- **失败传导**:`ResolveVerificationForAction`/`ResolveMaterializedRunnableRevision` 先查
  动作行(failure 三列与身份全列匹配,行不一致视为完整性错误),failed 时返回新
  `runnable.ActionFailure`(携带 class/code/summary,`Unwrap` 到哨兵
  `ErrRunnableActionFailed`)而非 `ErrMaterializationNotReady`;coordinator 对该错误调
  新增 `GenerationRepository.FailRunnableGenerationWorkflow`(Materializing/Verifying →
  既有 `StateFailed` 终态,state_version+1,last_error 带类/码/摘要);installer 的
  `advanceEntries` 两处走既有 `FailRelease`;`report.Passed=false → FailRelease` 既有
  路径不动。
- **schema 58→59(一处对计划的如实偏离)**:计划断言"零 schema 迁移"的前提是"实现时
  核实无其他约束"。核实发现 `runnable_actions.attempt` 有未列入的
  `CHECK (attempt <= 5)`——"claim 无截断、attempt 超限仍可认领、耗尽只由失败汇报显式
  转移"要求租约接管链可把 attempt 推过任何有限上限,CHECK 必须放宽为
  `CHECK (attempt >= 0)`。动作四态、failure_class CHECK、openapi 与前端生成物零改动
  (AdminRunnableActionItem 的 state/failure 为自由字符串,已核实)。
- **测试**:domain 曲线序列与退化输入;仓库层——infra 退避序列(7 次回队 next_run_at
  逐一对上曲线)+ 第 8 次显式 failed(class/code/summary 断言)+ failed 行不可认领 +
  resolver 返回 ActionFailure;claim 无截断(手插 attempt=9 的 queued 行仍可认领至 10,
  其失败汇报即显式耗尽);重调度二分(infra-failed 重置为 attempt=0 的新周期、
  artifact-failed 原样缓存);runner——deadline 包装错误归 artifact/
  execution-deadline-exceeded、"报告携带 artifact 失败即 completed"回归护栏(不再
  ReportFailure、不再请求环境释放);coordinator——物化/验证两侧 failed 动作下一轮
  即 StateFailed 且无投影写入;installer——新增 `installer_test.go`(复用
  writePortableRelease fixture),物化/验证两侧 failed 动作走 FailRelease 且不再标记
  Materialized/Verified。

### 验收证据

- 快车道全套:`make test-unit`、`make test-race`、`make verify-generated`、`make lint`
  (0 issues)、`kubectl kustomize .`、`make web-test-unit`(8 files / 51 tests,
  web 侧本阶段零改动)全绿。
- PostgreSQL 门控套件(schema 59 破坏迁移重建)通过。
- 行为断言:任何路径都不再产生"停在 queued 却永不被认领"的行(claim 无截断 + 耗尽由
  失败汇报显式转移);显式 failed 的动作让等待方在下一个观察周期走到失败出口
  (coordinator→StateFailed、installer→FailRelease),而非无限等待。
- 未排 e2e:无新集群侧行为契约(与计划一致)。

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
