# TODO

已完成的阶段见 git 历史(最近:门禁拒绝自动重试状态机与反馈回灌见 cfc5c9d/956f401;fixture 退役与 reviewer 契约修复见 119f034;docs 上游 canary 与发布前收尾见 dc85e1d;PR 门禁与 Dependabot 自动合并见 3b2c47d);本文件保留当前阶段与未立项事项。

## 状态机统一 U2:文档工作流并入 generation 机器(当前阶段,2026-09-21 立项,2026-09-21 与用户确认方案)

背景:门禁拒绝自动重试机器落地后,live 验收暴露生成质量根因——文档侧生成是"盲写单发 + 盲审 + 验证失败即终态",而运维侧 GenerationWorkflow 的生成是"工作区真实执行 + 自验 + 三层带反馈回灌"。与用户确认:**两边 agent workflow 大部分公用,只是入口不同;采用 U2 深度——两套状态机合一,文档入口插件化**(否决了两条更浅路径:平行新建文档工作区机器,以及仅挂行复用工作区)。

架构定稿:

- **一个状态机、一张权威表(generation_workflows)、一套驱动循环;文档与 authoring 是两个 source 插件**;
- 统一状态集 14 态:运维 9 态 + 文档缺的相(Planning/PlanReviewing/VerificationReviewing/NoPractice/Rejected);迁移合法性从"repository WHERE 谓词分散"集中为域内迁移白名单(还运维侧结构债);
- 文档入口:IgnitionDispatcher/批次 ignite 驱动 planner+plan 门禁,门禁拒→AutoRestart 回 Planning(预算内,本阶段已建机器保留);
- Generating 两入口同形 = **GeneratorService 工作区回合**(PVC/沙箱/快照/Reaper 全套直用);文档生成 Agent 经 GeneratorOperations 端口驱动,提交入口为文档形状(工作区归档→CompileCandidate,agent 只交 execution plan,杜绝测一套交一套)+ `verify_draft` 真环境自验(dry-run 走 RunnableStore 仓库层,StateVersion=0 不 Bind,脱离 watchdog/Reconcile;env 由现有 controller/reaper 回收);
- Judging = AgentRunner 租约认领(扩为 purpose→executor 注册表):文档注册双角色门禁 Judge(plan/candidate)与验证评审 Judge;运维 Judge 原样;
- Materializing/Verifying = 公共 runnable 队列,RunnableCoordinator 替代文档 Reconcile 出站箱;文档 Bind 语义并入;
- 回灌统一带反馈:authoring 读 last_error(无界,行为不变);文档从账本 pipeline.auto_restart 取,预算 MaxRevisions=3;**验证失败也回灌**(Gate="verification",对齐运维 :551 语义);
- Publishing:运维 PublicationFinalizer 模式 + 文档 Publish finalizer 插件(发布校验/PracticeRevision/practice index/reader projection 原样);
- 表策略:权威迁 generation_workflows,文档页身份/预算入 document_workflow_details 扩展表;**旧 document_workflows 转 SQL VIEW**——e2e 14+ 处直查断言、已发布锚点、批次/管理读模型零改动兼容(spec 改写列后续);管理 API/Prometheus/前端状态名经映射层保持对外不变。

提交拆解:

### 提交 0 live 基座(异步点火与窗口)

- [x] 点火端点 202 异步:AgentPipeline.Prepare + IgnitionDispatcher 后台驱动链路并按工作流去重(live 单轮链路 5 分钟以上,同步 POST 连一轮都装不下——live 套件从未绿过的根因);
- [x] spec 2 ensurePracticePublished 死代码修复(首注册即 bootstrap admin 的选举语义,否则按角色断言明确失败,不再 403 迷雾);
- [x] 轮询终态快速失败(awaitWorkflowState:落定集=接受集∪终态)+ 等待窗按 max_revisions 全预算放大(30 分钟档);
- [x] 模型单请求超时 5m→15m(推理模型补全可超 5m,重试耗尽即链路放弃,live 实测);
- [x] 指令修正:planner 澄清"agent 自身无集群访问 ≠ 实践不能用隔离 k8s 集群"(live 曾因此直接弃做);generator 补运行时契约事实(/bin/bash 执行、归档路径、kubectl 在标准 PATH,live 曾捏造绝对路径致 exit 127)。

### 提交 1 域统一

- [ ] generation 状态集扩 14 态 + 集中迁移白名单(单测:全迁移矩阵);
- [ ] source_kind 加 'documentation'(schema CHECK 与域);归属谓词按 source 分派(documentation owner=文档工作流存在,类比 release 无 owner);
- [ ] SubmitCandidate 按 source 分派;generation 既有测试全绿。

### 提交 2 权威迁移

- [ ] documentpractice Store 状态方法改由 generation 仓库泛化实现;workflow id 沿用 `"document-workflow-"+ContentID` 作 generation 行主键(批次绑定零迁移);
- [ ] document_workflow_details 扩展表(页身份 + max_revisions);document_workflows 转 VIEW(generation JOIN details);
- [ ] watchdog/批次/观察/admin 队列读模型三处 JOIN 迁到权威表;document_runnable_actions.state_version 对齐权威表;StateVersion-1 不变量测试先行;
- [ ] 管理 API 状态名映射层(对外文档状态名不变);Prometheus/前端经视图与映射不动;documentpractice/generation/transport 单测 + web 组件测全绿。

### 提交 3 工作区生成

- [ ] GeneratorService 守卫泛化(source 分派落地);文档生成 Agent:GeneratorOperations 工作区五件套子集 + submit_practice_blueprint(归档编译)+ 照 authoring executor 骨架(authoringTool/toolresult/invalidToolInput 同对话自修正);
- [ ] 实现 BlueprintGenerator/FeedbackBlueprintGenerator 既有接口(管线与测试假件零改动);bootstrap 与 authoring 共享同一 GeneratorService 实例;
- [ ] 假工作区单测:写入/执行回灌/提交取归档。

### 提交 4 评审统一 + 自验 + 验证回灌

- [ ] AgentRunner purpose→executor 注册表;文档双角色门禁 Judge 与验证评审 Judge 挂入;
- [ ] verify_draft 工具(dry-run,StateVersion=0 不 Bind);spec 的 runnable_actions 轮询改经 document_runnable_actions JOIN(dry-run 行不再干扰);
- [ ] 验证失败回灌:AutoRestart 复用(Gate="verification",reasons 取报告摘要),反馈从 pipeline.auto_restart 账本推导(含崩溃恢复);预算耗尽落 Failed 终态;
- [ ] 单测:验证失败→回灌→重试发布 / 预算耗尽终态 / 批次与直发两路再驱动。

### 提交 5 live 验收收口(承接原阶段遗留项)

- [ ] RUN_AGENT_LIVE_E2E=1 make test-e2e-documentation 全绿(发布经重试轮次收敛);
- [ ] RUN_AGENT_LIVE_E2E=1 make test-e2e-admin 全绿;
- [ ] 快车道/nightly 绿;TODO.md 阶段收口(记录统一决策与验收章)。

### 横切风险对策

- state_version 三方对齐(watchdog/观察/绑定)→ 绑定表指权威表,不变量测试先行;
- 审计/账本 payload 状态字面量与 Prometheus 12 态、前端 STAGE_STATES → 映射层原样保留;
- e2e 直查 → VIEW 兼容(spec 改写为后续独立项);
- lease 不变式扩展为"lease ⟺ 被认领相"(文档入口/评审相不经 lease,队列相走公共队列);
- 运维回灌维持无界(不给 authoring 加预算,行为不变更);文档账本保留(审计支柱)。

### 已知后续(不属本阶段)

- 工作区崩溃孤儿沙箱/PVC 的账本记录 + 启动清扫;e2e spec 直查改写(脱离 VIEW);
- js-yaml ×3 告警等 Dependabot 分组更新;ollama critical 无上游修复版;#15 typescript 7 等 vue-tsc 跟进;
- 首次真实发布后部署侧验证无凭证直拉。

## 文档管线门禁拒绝自动重试(前一阶段,收口并入 U2 提交 0/5)

状态机(拒绝→有界自动重启,cfc5c9d)与反馈回灌(956f401)已提交;live 验收两项与 spec 2 死代码修复作为前置并入上方 U2 的提交 0(已完成,待提交)与提交 5。阶段背景与决策见 8bfa990 的 TODO 版本。

## 挂起待决策(不排期)

- **内容治理/紧急下架**:场景侧非 authoring 内容无法下架、无管理员覆盖;实践内容 PracticeRevision 一侧 append-only(索引只进不改)是刻意设计,与"紧急摘除"冲突。若做,方向是索引摘除/tombstone 而非删除数据——独立设计决策后另行立项,当前不做。
