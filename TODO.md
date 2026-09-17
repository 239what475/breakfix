# 下一阶段:管理员控制面

上一阶段"离线文档库生成器"已全部完成并验收(版本推进至 **docs-project-v10**:全量基线
854 页 / 217 孤儿 / 433 重定向 / 12,476 锚点 / 63 个入库资产;v10 完成全库 854 页逐页审查
并修复十类共性缺陷,dropped 链接 294→179)。实施规格、提交序列与验收记录见 git 历史。

文档工作流的后续推进(运行时切换到文档库、多页批次铺开)**移至 NEXT.md 记录方向**,不在
本 TODO 内;管理员控制面与其无依赖关系,可独立推进、先行实施。

本规格为交付级:实现者不应再做设计决策,一切命名、形状、语义以本文为准;发现规格与代码
现实冲突时,先改规格再改实现,同一提交内同步。

## 背景:摸底结论(2026-09-17)

现状缺口,均经代码核实:

- **无角色模型**:`users` 表无 role 字段,注册开放,任何登录用户都能调用
  `POST /api/documentation/practice` 点火——"谁能启动"没有答案。
- **文档工作流观测真空**:无状态查询/列表端点;`/metrics` 只统计 generation 工作流;
  前端没有任何文档工作流 UI。启动后只有一次性 202 响应,看进度只能查数据库。
- **工作流可永久卡死且无恢复手段**:Agent 阶段(PlanReviewing/Generating/
  ArtifactReviewing)中途崩溃后无续跑路径,重复点火只重放已存状态;runnable action 重试
  5 次耗尽或终态失败后不映射工作流 Failed,物化失败的页面永远停在 MaterializingArtifact。
  今天唯一的解法是删数据库行。修订循环(ReviseAt)与工作流租约获取在生产代码中是死代码。
- **账号无恢复路径**:丢失 TOTP 设备只能数据库手术;无用户列表、无禁用/锁定。
- **环境与队列观测只有 kubectl/DB**:RuntimeEnvironment 跨用户列表、runnable 队列积压、
  13 个后台服务运行状态均无 API 面。

## 设计决策(2026-09-17 与维护者确认)

- **破坏性 schema 变更**(仓库纪律,`schema.go` 注释:不写 ALTER 兼容语句,旧库必须重置):
  无存量部署负担,角色列与新表随 `currentSchemaVersion` bump 直接落地,不做非破坏迁移。
- 单一 admin 角色,**首注册用户即管理员**(memos 模式);不做 RBAC、不做团队管理。
- `requireAdmin` **纯 JWT claim 判定**,不逐请求查库(系统无角色管理面,role 只可能随库
  重置变化);无 role claim 的旧 token 一律按普通用户。
- 解卡为 **force-fail + restart 双动词**;worker 迟到/陈旧的完成上报一律"标记 reconciled、
  不推进状态、留日志";验证环境不显式取消,靠既有 1800s MaxLifetime TTL 兜底。
- force-fail/restart **双记录**:artifact ledger 条目(工作流唯一历史)+ 人操作审计(操作者)。
- Agent 阶段 stuck 阈值默认 **15 分钟**,config 可覆盖。
- 管理员定位是**操作者+观察者**,不进内容门禁——门禁仍由 Agent 评审承担。
- 控制动词最小集:点火(升权)、解卡、重启、TOTP 重置、环境释放;不新增绕过状态机的旁路。
- 内容治理(场景/实践内容紧急下架)与 append-only 证据链设计冲突,**挂起待决策**(1.7)。
- 不做:维护模式(维持 runbook"停 ingress"的处置)、配置热改、管理员重置密码(需要时按
  runbook 走数据库)、system 端点的外部二进制存活检测(v1 只做 server 内,见 1.6)。

提交纪律(全程有效):做完一个可审查单元立即提交,不攒批;每提交保持构建与当包测试通过;
TODO 勾选随对应提交更新,禁止收尾批量补勾;规格与实现变更在同一提交内同步;提交信息沿用
仓库格式(`类型(范围): 摘要` + Boundary/Validated 结构化正文)。

勾选状态按当前代码与测试证据维护;未勾选项表示仍有实现或验收工作未完成。

## 1. 管理员控制面实施规格

### 1.1 角色模型与账号管理

行为规格:

- **DDL**(`schema_identity.go`):`users` 增列
  `role TEXT NOT NULL DEFAULT 'user' CHECK (role IN ('user','admin'))`;
  `currentSchemaVersion` bump(破坏性,无迁移语句)。
- **JWT**(`middleware/jwt.go`):`Claims` 增 `Role string \`json:"role,omitempty"\``;
  `GenerateJWT(userID, userName, role)`;`setClaims` 增设 `"role"`。旧 token 无该字段解析为
  空串,语义即 `user`,无需兼容分支。前端为隐藏入口可 base64 解码 payload 读 role(权限
  由后端保证,前端只做展示层)。
- **requireAdmin 中间件**:读 context role,非 `admin` → 403;纯 claim,不查库。
- **首注册判定**:register 事务内先 `pg_advisory_xact_lock(<固定 key>)` 再
  `count(*) FROM users`,为 0 → `role='admin'`,否则 `'user'`。咨询锁关死并发首注册竞态。
- **注册开关**:config 顶层新增 `allow_registration`(bool,默认 `true`);`ValidateServer`
  校验;`false` 时 register 返回 403,login 不受影响。
- **点火升权**:`POST /api/documentation/practice` 从 JWT 组移入 requireAdmin 组。
- `GET /api/admin/users`(admin):`{users: [{id, subject, name, role, created_at}]}`,
  不含 password_hash/totp_secret;只读,无启停/删除/提升。
- `POST /api/admin/users/:id/totp-reset`(admin):请求体 `{"password": string}` 为调用方
  密码确认(规格修订 2026-09-17:原文"请求体为空"与密码比对及 1.5 的操作者密码输入冲突,
  以实现前的更正为准);服务端**对调用方自己的 password 做 bcrypt 比对**(防 token 窃取后的
  静默重置),失败 403 且不重置;通过则
  `GenerateTOTPSecret` 新密钥、UPDATE 目标用户、一次性返回 `{totp_secret, totp_url}`
  (与注册响应同一形态);目标用户旧 TOTP 即刻失效;允许 admin 重置自己(密码确认即门槛);
  写审计(1.2,action=`user.totp.reset`)。
- OpenAPI(`api/http/openapi.yaml`)同步全部新端点与 bearerAuth 语义,`make generate`
  重新生成服务端与前端客户端;前端 `AuthDialog`/导航按 role 适配。

验收标准:

- [x] 空库并发首注册(构造两并发 register):恰好一人 admin;次注册恒为 user。
- [x] 旧 JWT(无 role)在用户级端点正常、在 admin 端点 403;admin 正常放行。
- [x] `allow_registration: false` 时注册 403,登录正常;缺省 true 行为不变。
- [x] 非 admin 调用点火 / users / totp-reset 均 403。
- [x] totp-reset 后:旧 TOTP 登录失败、新 TOTP 成功;调用方密码错误时不重置且留审计。
- [x] 单测覆盖上述路径;OpenAPI 与生成代码同步提交。

提交:`feat(auth): bootstrap first-user admin with role claims`。

### 1.2 人操作审计

行为规格:

- **DDL**(新 schema group):append-only,无任何 update/delete 路径:

```sql
CREATE TABLE human_action_audits (
  id TEXT PRIMARY KEY,
  user_id TEXT NOT NULL,
  action TEXT NOT NULL,
  target_type TEXT NOT NULL,
  target_id TEXT NOT NULL,
  detail JSONB NOT NULL,
  created_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX human_action_audits_time ON human_action_audits (created_at DESC, id);
CREATE INDEX human_action_audits_action ON human_action_audits (action, created_at DESC);
```

- id 沿用仓库风格(`audit-<unixnano>`)。
- **动作词表(封闭集合,新增动词必须先登记本文)**:`documentation.practice.start`、
  `documentation.workflow.force_fail`、`documentation.workflow.restart`、
  `user.totp.reset`、`environment.release`。
- `detail` 形状:`{"reason": "...", "from_state": "...", "to_state": "...", ...}`;
  force-fail/restart 的 reason 必填(请求体携带,非空,长度上限 500)。
- 写入路径:service 层与状态变更**同一事务**,repo 提供 `RecordHumanAction`;只读端点不审计。
- `GET /api/admin/audit?limit&cursor&action&user_id`(admin):分页信封沿用
  `/api/me/space/learning` 的 limit/cursor 形态。
- 机器侧审计(`document_agent_audits`)不变;两者互补——人审计答"谁点的火",
  Agent 审计答"机器怎么跑的"。

验收标准:

- [ ] 1.1/1.3/1.6 的每个管理动词各落地一行审计,字段完整、事务一致(状态变更失败则审计不落)。
      (1.1 的点火与 totp-reset 已落地;1.3/1.6 动词随各自小节提交后勾选。)
- [x] 列表 action/user_id 过滤与 cursor 分页可用;非 admin 403。
- [x] 单测:写入、过滤、分页;确认无 update/delete 代码路径。

提交:`feat(audit): append-only human action ledger`。

### 1.3 工作流观测、解卡与重启

前置事实(已核实,实现者不必重查):`allowedTransition` 已允许**所有非终态 → Failed**
(`workflow.go` allowedTransition),`AdvanceAt(Failed, now)` 不传 requiredKinds 即合法;
**状态机表无需改动**。`document_workflows.updated_at` 即状态进入时间,dwell = now −
updated_at。`Start` 仅在 `state == Planning` 时驱动 Agent。

行为规格:

- `GET /api/admin/documentation/workflows`(admin):列表
  `{workflows: [{id, state, state_version, revision, updated_at, dwell_seconds, stuck}]}`;
  当前部署 ≤1 行,形状为多页预留。
- `GET /api/admin/documentation/workflows/:id`(admin):详情 = 上述字段 +
  `ledger`(document_artifact_ledger 按 created_at 升序)+ `agent_audits`
  (document_agent_audits 升序)+ `publication`(已发布时的 manifest)。单工作流 ledger/
  audit 有界,不分页。
- **stuck 判定(只读推导,绝不写状态)**:
  - 可运行阶段(MaterializingArtifact/Verifying/VerificationReviewing):取
    `document_runnable_actions` 中 `(workflow_id, phase, state_version)` 当前绑定行,按
    action_key 联 `runnable_actions`:`state='failed'` →
    `{"flag":true,"reason":"action_failed","failure_class":...,"failure_code":...,
    "failure_summary":...}`;`attempt=5` 且 state ∈ {queued,running} →
    `reason:"attempts_exhausted"`;否则 dwell > 1800s+300s 余量 →
    `reason:"dwell_timeout"`。
  - Agent 阶段(Planning/PlanReviewing/Generating/ArtifactReviewing/Publishing):
    dwell > config 顶层 `agent_stuck_after`(默认 `15m`) → `reason:"dwell_timeout"`。
  - 正常时 `{"flag":false}`。
- **force-fail** `POST /api/admin/documentation/workflows/:id/force-fail`(admin):
  请求体 `{"reason": string}` 必填;仅非终态可解,终态 409;并发纪律沿用 service 层既有
  状态推进方式(state_version 条件更新或既有租约助手),不得与 5s 恢复循环竞态。
  成功:state→Failed、state_version+1;**双记录**——artifact ledger 追加
  `kind='admin.force_fail'`、`owner_role='admin'`、digest=`DigestAgentInput(规范化payload)`、
  payload=`{actor, from_state, reason, at}`(工作流无状态历史表,ledger 是唯一历史);
  人审计 `documentation.workflow.force_fail`。
- **restart** `POST /api/admin/documentation/workflows/:id/restart`(admin):
  请求体 `{"reason": string}` 必填;域模型新增 `RestartAt(now)`(ReviseAt 姊妹方法):
  仅 `Failed`/`Rejected` → `Planning`,revision+1、state_version+1;**不受 MaxRevisions
  上限约束**(该上限为自动修订循环设计,管理员重启是人工判断);`Published`/`NoPractice`
  与一切非终态 409。重启只重置状态、**不调 Agent**——随后的 `POST /api/documentation/
  practice`(既有点火端点)在 Planning 状态自然驱动重跑,点火照常写审计。双记录同
  force-fail(ledger `kind='admin.restart'`)。
  - 规格修订(2026-09-17,E2E 实测发现):重跑会重新生成 plan/candidate 工件,而其载荷
    含时间戳,相同派生 ID 载荷不同字节会撞 ledger 不可变约束("artifact id already has
    another digest"),重启后永久无法再点火。故 SubmitPlan/SubmitCandidate 的工件 ID 追加
    attempt 后缀 `-a<workflow.revision>`,PracticeRevision 增记 `workflow_revision`,
    发布校验(`validatePublicationLedger`)按同规则派生;同 attempt 内重试字节相同仍走
    幂等去重。
  - 规格修订(同日,第二次 E2E 实测):确定性重跑会再生成与前一 attempt 字节相同的
    candidate(archive digest 不变),新 ID 旧 digest 撞 `UNIQUE(workflow_id, kind,
    digest)`。该约束把"两次 attempt 提交相同内容"误判为重复,与 attempt 命名空间冲突;
    schema 48 起移除该约束(ID 主键 + ID 等值去重已足够),SaveArtifact 改为按 id 幂等。
  - 规格修订(同日,同源问题):重跑物化出字节相同的 runnable revision(新存储 id、旧
    digest)时,`StoreRunnableRevision` 的 INSERT 撞 `UNIQUE(runnable_revision_digest)`,
    worker 连败 5 次后 attempt 耗尽,工作流停在 MaterializingArtifact。digest 是内容身份:
    同 digest 已存在(无论何 id)即视为存储成功;同 id 绑定不同 digest 仍为完整性错误。
- **迟到/陈旧完成统一规则**(规格修订 2026-09-17:原文单一 state_version 相等校验与
  崩溃恢复的续跑语义冲突——工作流常因同一 action 的先前处理已推进过状态版本,故按状态
  判别):完成上报到达时,若工作流已终态(force-fail 后)、或处于 Planning(restart 重置
  后的重跑尚未点火,而 runnable action 绝不会在 Planning 绑定,故到达 Planning 的完成
  必属重启前旧线)、或 action 阶段早于工作流当前状态所允许的阶段(materialize 在
  MaterializingArtifact 但 state_version 不等;verify 在 Verifying 之前),则仅将绑定标记
  reconciled、不推进状态、留日志;其余情形按既有续跑语义继续,所有状态推进仍由 store 层
  state_version 条件更新把栅。该规则同时覆盖 force-fail 后的迟到完成与 restart 后的陈旧
  完成。
- force-fail/restart 不删除任何数据;在途验证环境不显式取消,靠 1800s MaxLifetime TTL。

验收标准:

- [x] 构造 PlanReviewing 停滞(模拟 Server 中途崩溃):列表/详情可见、stuck 原因正确;
      force-fail 后转 Failed、ledger 与审计各有一条、重复调用 409。
- [x] 构造物化 action 终态失败与 attempt 耗尽:详情分别归因 action_failed /
      attempts_exhausted;force-fail 同上。
- [x] restart:Failed/Rejected → Planning(revision+1),再点火完整重跑;Published /
      NoPractice / 非终态 restart 均 409;MaxRevisions=3 耗尽后仍可 restart。
      (再点火完整重跑由既有 pipeline 测试与 1.5 E2E 覆盖。)
- [x] 迟到完成:force-fail 后补报 action 完成 → 绑定变 reconciled、状态不推进;
      restart 后旧 state_version 的完成同样不推进。
- [x] dwell 阈值:agent 阶段默认 15m、config 覆盖生效;runnable 阶段 1800s+余量。
- [x] 单测:force-fail/restart 全部合法与非法转换、迟到/陈旧完成规则、双记录事务一致性
      (状态变更失败则 ledger/审计不落)。

提交:`feat(documentpractice): admin workflow observation, force-fail, and restart`。

### 1.4 队列观测与指标

行为规格:

- `GET /api/admin/runnable-actions?state=&phase=`(admin):

```json
{
  "summary": {"by_state": {"queued": 0, "running": 1, "failed": 0, "completed": 3},
               "by_attempt": {"0": 3, "4": 1}},
  "items": [{"action_key": "...", "content_kind": "...", "content_id": "...",
              "phase": "verify", "state": "running", "attempt": 4,
              "lease_expires_at": "...", "next_run_at": "...",
              "failure_class": "", "failure_code": "", "failure_summary": "",
              "document_workflow_id": "...", "reconciled": null, "flag": "attempt-high"}]
}
```

- `flag` 服务端判定:`attempt-high`(attempt ≥ 4)、`failed-unreconciled`
  (runnable_actions.state='failed' 且 document 绑定 reconciled_at IS NULL——即"action 已死、
  工作流还挂着"的精确信号);无 flag 时为空串。
- `/metrics` 新增 gauge:`breakfix_document_workflows{state}`(12 状态全量计数)、
  `breakfix_runnable_actions{state}`。

验收标准:

- [x] 队列端点 summary/items 与数据库实况一致(单测构造多态 action 断言,含两类 flag);
      非 admin 403。
- [x] `/metrics` 文本含两个新指标,计数与库中一致。

提交:`feat(ops): runnable queue observation and workflow metrics`。

### 1.5 管理界面

行为规格:

- `web/src/features/admin/`,路由 `/admin/workflows|users|audit`;router guard 解析 JWT
  payload 的 role,非 admin 重定向;顶部导航"管理"仅 admin 可见(展示层,权限在后端)。
- 工作流页:列表(状态、dwell、stuck 徽标)+ 详情(状态头、ledger 时间线、AgentRun 审计、
  发布清单)+ force-fail / restart 按钮——二次确认框含 **reason 必填输入**,确认框展示
  将写入的审计摘要。
- 用户页:列表 + TOTP 重置(输入操作者密码;结果一次性展示新 secret/URL,刷新即不再出现)。
- 审计页:按 action / user 过滤 + cursor 分页。
- 队列积压摘要卡(summary + flag 计数)置于工作流页顶部;明细由 1.4 端点呈现,是否独立
  页面按信息密度由实现者定,不新增设计。
- 最小实现,不做设置系统。

验收标准:

- [ ] admin 登录可见"管理",可完成:查看工作流列表/详情、force-fail、restart、重置 TOTP、
      查审计、看队列摘要。
- [ ] 普通用户不可见"管理"入口,直调 admin API 由后端 403。
- [ ] E2E(复用 kind 流程):空库注册 → admin → 点火 → 模拟卡死 → force-fail →
      restart → 再点火,全链路通过。
- [ ] 现有页面回归无变化。

提交:`feat(web): admin console for workflows, users, and audit`。

### 1.6 第二批:环境观测与释放、系统信息

行为规格:

- `GET /api/admin/environments`(admin):全量 RuntimeEnvironment(跨用户 learning +
  verification,k8s adapter 既有 List),按 label 补 purpose/绑定用户与内容;字段:名称、
  namespace、phase、purpose、创建时间、到期时间(ExpiresAt 语义见
  controller state.Decide);Failed 堆积一眼可见。
- `POST /api/admin/environments/:name/release`(admin):设置 `spec.lease.releaseAt`
  (adapter Update;LeaseSpec 有 immutable-monotonic CEL 约束,设为当前时间即可),之后由
  既有 Draining→Reaper 机制接管,**不旁路**;已 Released 409、不存在 404;写审计
  `environment.release`。
- `GET /api/admin/system`(admin):
  - 后台服务状态:给 bootstrap 的 service runner 加**内存注册表**(name、started_at、
    last_tick_at、last_error),各服务 tick 时上报——本节唯一新框架件;
  - catalog 完整性:复用 `CheckIntegrity` 只读执行,异常时定位到具体 revision;
  - 版本与钉住配置摘要:上游 commit、页面/锚、catalog release 引用、generator 版本。
  - **不做**外部二进制(controller/runtime-worker)存活检测——server 看不到其心跳,v1
    明确缺席,留待需要时另行设计。

验收标准:

- [ ] 环境列表与集群实况一致;release 后走完既有 Draining→Released,无旁路状态;
- [ ] system 端点各字段为真实值;人为破坏一个物化目录后完整性项可定位到 revision;
- [ ] release 有审计;非 admin 403。

提交:`feat(ops): environment and system observation for admins`。

### 1.7 挂起待决策(不排期)

- **内容治理/紧急下架**:场景侧非 authoring 内容无法下架、无管理员覆盖;实践内容
  PracticeRevision 一侧 append-only(索引只进不改)是刻意设计,与"紧急摘除"冲突。若做,
  方向是索引摘除/tombstone 而非删除数据——独立设计决策后另行立项,当前不做。
