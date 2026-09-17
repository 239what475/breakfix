# 下一阶段:管理员控制面

上一阶段"离线文档库生成器"已全部完成并验收(版本推进至 **docs-project-v10**:全量基线
854 页 / 217 孤儿 / 433 重定向 / 12,476 锚点 / 63 个入库资产;v10 完成全库 854 页逐页审查
并修复十类共性缺陷,dropped 链接 294→179)。实施规格、提交序列与验收记录见 git 历史。

文档工作流的后续推进(运行时切换到文档库、多页批次铺开)**移至 NEXT.md 记录方向**,不在
本 TODO 内;管理员控制面与其无依赖关系,可独立推进、先行实施。

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

## 设计决策(已确认)

- 单一 admin 角色,**首注册用户即管理员**(memos 模式);已有部署迁移按 `created_at`
  最早用户提为 admin。不做 RBAC、不做团队管理。
- 管理员定位是**操作者+观察者**,不进内容门禁——门禁仍由 Agent 评审承担。
- 控制动词最小集:点火(升权)、解卡、TOTP 重置、环境释放;不新增绕过状态机的旁路。
- 内容治理(场景/实践内容紧急下架)与 append-only 证据链设计冲突,**挂起待决策**(1.7)。
- 不做:维护模式(维持 runbook"停 ingress"的处置)、配置热改、管理员重置密码(需要时按
  runbook 走数据库)。

提交纪律(全程有效):做完一个可审查单元立即提交,不攒批;每提交保持构建与当包测试通过;
TODO 勾选随对应提交更新,禁止收尾批量补勾;规格与实现变更在同一提交内同步;提交信息沿用
仓库格式(`类型(范围): 摘要` + Boundary/Validated 结构化正文)。

勾选状态按当前代码与测试证据维护;未勾选项表示仍有实现或验收工作未完成。

## 1. 管理员控制面实施规格

### 1.1 角色模型与账号管理

行为规格:

- `users` 新增 `role` 列(`admin`/`user`,默认 `user`)。注册在**同一事务内**判定用户数为
  零 → admin;存量库迁移将 `created_at` 最早的用户提为 admin。
- JWT claims 新增 `role`;**无 role claim 的旧 token 一律按普通用户**(24h 过期自然收敛,
  不强制重登)。新增 `requireAdmin` 中间件。
- config 新增 `allow_registration`(默认 `true`;部署可钉 `false` 关闭注册,关闭时
  register 返回 403,登录不受影响)。
- `POST /api/documentation/practice` 由"登录即可"升为 admin-only。
- `GET /api/admin/users`(admin):只读列表(id/用户名/角色/创建时间);不做启停、删除、
  角色提升。
- `POST /api/admin/users/:id/totp-reset`(admin):重置目标用户 TOTP,调用方须重新提交
  **自己的登录密码**确认;成功返回一次性新 secret/URL,目标用户旧 TOTP 立即失效;
  写审计(1.2)。

验收标准:

- [ ] 空库首注册得 admin、次注册得 user;存量库迁移后最早用户为 admin。
- [ ] 旧 JWT(无 role)在用户级端点正常、在 admin 端点 403。
- [ ] `allow_registration: false` 时注册 403,登录正常。
- [ ] 非 admin 调用点火 / users / totp-reset 均 403。
- [ ] totp-reset 后旧 TOTP 登录失败、新 TOTP 成功;密码确认错误时不重置且留痕。
- [ ] OpenAPI 规格与生成代码同步;单测覆盖上述路径(含首注册并发的同事务判定)。

提交:`feat(auth): bootstrap first-user admin with role claims`。

### 1.2 人操作审计

行为规格:

- 新表 append-only `human_action_audits(id, user_id, action, target_type, target_id,
  detail, created_at)`,只增不改,与状态变更同事务写入。
- 首批记录动作:点火、解卡、TOTP 重置、环境释放;后续新动词随实现登记。
- `GET /api/admin/audit`(admin):分页列表,可按 action/target/user 过滤。
- 机器侧审计(document_agent_audits)不变;两者互补——一个回答"谁点的火",一个回答
  "机器怎么跑的"。

验收标准:

- [ ] 每个管理动词落地一行审计,字段完整;审计表无更新/删除路径。
- [ ] 列表过滤与分页可用;非 admin 403。
- [ ] 单测:写入、过滤、append-only 约束。

提交:`feat(audit): append-only human action ledger`。

### 1.3 文档工作流观测与解卡

行为规格:

- `GET /api/admin/documentation/workflows`(admin):列表——id、状态、进入当前状态时间、
  停留时长、修订版本、`stuck` 标记。
- `GET /api/admin/documentation/workflows/:id`(admin):详情 + artifact ledger 时间线 +
  AgentRun 审计 + 发布清单(已发布时)。
- **卡死判定**(只读推导,不改状态):Agent 阶段(Planning/PlanReviewing/Generating/
  ArtifactReviewing)停留超过由 Agent 超时推导的阈值,或 MaterializingArtifact/Verifying
  对应 runnable action 已终态失败 / 重试耗尽(attempt 达上限)且未 reconciled → 详情标记
  `stuck:true` 并给出原因。阈值常量由现有配置(Agent 超时、物化/验证生命周期 1800s、重试
  上限 5)推导,实现时定稿。
- **解卡**:`POST /api/admin/documentation/workflows/:id/force-fail`(admin)——状态机补齐
  各非终态(Planning/PlanReviewing/Generating/ArtifactReviewing/MaterializingArtifact/
  Verifying/VerificationReviewing)→ Failed 的受控转换;仅非终态可解,终态 409;写账本与
  审计。**不做自动看门狗**(自动失败映射随多页批次设计,见 NEXT)。
- 解卡不删除任何数据:工作流、账本、action 记录全部保留,只推进状态。

验收标准:

- [ ] 构造 PlanReviewing 停滞(模拟 Server 中途崩溃):列表/详情可见、stuck 原因正确;
      force-fail 后转 Failed、账本+审计有记录、重复调用 409。
- [ ] 构造物化 action 终态失败/attempt 耗尽:详情正确归因;force-fail 同上。
- [ ] 终态(Published 等)不可 force-fail(409);后端不依赖 stuck 标记,允许对任意
      非终态解卡,前端仅在标记 stuck 时展示入口。
- [ ] 单测:各非终态→Failed 的转换合法性、终态拒绝、审计与账本写入。

提交:`feat(documentpractice): admin workflow observation and force-fail`。

### 1.4 队列观测与指标

行为规格:

- `GET /api/admin/runnable-actions`(admin):按 state(queued/running/completed/failed)
  的计数与 attempt 分布,附明细(目标、attempt、lease 到期时间、绑定工作流);重点暴露
  attempt≥4 与 failed 未映射工作流的行。
- `/metrics` 新增 `breakfix_document_workflows{state}` 计数;runnable 队列深度以
  `breakfix_runnable_actions{state}` 计数(标签形状实现时定稿)。

验收标准:

- [ ] 队列端点数字与数据库实况一致(单测构造多态 action 断言);非 admin 403。
- [ ] `/metrics` 文本含新增指标,文档工作流状态计数与库中一致。

提交:`feat(ops): runnable queue observation and workflow metrics`。

### 1.5 管理界面

行为规格:

- 顶部导航新增"管理",仅 admin 可见(前端读 JWT role;前端隐藏不承担权限,后端中间件
  已保证)。
- 工作流页:列表(状态、停留时长、stuck)+ 详情(账本时间线、AgentRun 审计、发布清单)
  + force-fail 按钮(二次确认,展示将写入的审计内容)。
- 用户页:列表 + TOTP 重置(密码确认;新 secret 一次性展示)。
- 审计页:列表与过滤。
- 队列积压摘要并入工作流页或独立页,按信息密度实现时定。
- 最小实现,不做设置系统。

验收标准:

- [ ] admin 登录可见"管理",可完成:查看工作流列表/详情、解卡、重置 TOTP、查审计;
- [ ] 普通用户不可见"管理"入口,直调 admin API 由后端 403;
- [ ] 现有页面回归无变化。

提交:`feat(web): admin console for workflows, users, and audit`。

### 1.6 第二批:环境观测与释放、系统信息

行为规格:

- `GET /api/admin/environments`(admin):全量 RuntimeEnvironment(跨用户 learning +
  verification),字段:名称、phase、purpose、绑定内容/用户、创建时间、到期时间;Failed
  堆积一眼可见。
- `POST /api/admin/environments/:name/release`(admin):设置 `lease.releaseAt` 触发受控
  排空——复用既有 Draining→Reaper 机制,不旁路;已 Released 409、不存在 404;写审计。
- `GET /api/admin/system`(admin):后台服务清单与运行状态、catalog 完整性(复用
  CheckIntegrity 只读执行)、controller/runtime-worker 存活、版本与钉住配置摘要(上游
  commit、页面/锚、catalog release 引用、generator 版本)。

验收标准:

- [ ] 环境列表与集群实况一致;release 后走完既有 Draining→Released 流程,不产生旁路状态;
- [ ] system 端点各字段为真实值;catalog 完整性异常时可定位到具体 revision;
- [ ] release 动作有审计;非 admin 403。

提交:`feat(ops): environment and system observation for admins`。

### 1.7 挂起待决策(不排期)

- **内容治理/紧急下架**:场景侧非 authoring 内容无法下架、无管理员覆盖;实践内容
  PracticeRevision 一侧 append-only(索引只进不改)是刻意设计,与"紧急摘除"冲突。若做,
  方向是索引摘除/tombstone 而非删除数据——独立设计决策后另行立项,当前不做。
