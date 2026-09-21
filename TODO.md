# TODO

已完成的阶段见 git 历史(最近:文档门禁自动重试三笔 cfc5c9d/956f401/c0d65e5 与更早的文档管线建设——**产品方向已于 2026-09-21 变更,文档侧不再生成实践,全部相关代码在删除提交中移除**;fixture 退役与 reviewer 契约见 119f034;docs canary 与收尾见 dc85e1d);本文件保留当前阶段与未立项事项。

## 阶段:文档空白实践场景(2026-09-21 定案,本文件即执行计划)

### 0. 定案原则(与用户逐条确认)

1. **文档不生成任何实践**。文档页面本身是教材;系统只提供"场地"——每用户一个空白 vk8s 场景,用户照文档自己操作。生成管线、judge、反馈回灌、发布语料、批次——整个问题域取消。
2. **无锚点**。练习场是页面级甚至站点级的东西,不按章节切分。
3. **每用户一个场景(方案 A)**:钉住库范围内全局唯一,跨页面携带;任何文档页右侧工具栏显示同一状态、操作同一实例。配额即"每人 1 个";要干净开始用"重置",不是换页新建。
4. **不建模为目录 scenario**:空白场景就是 RuntimeEnvironment + 终端会话,不进 scenarios 目录、没有 runnable 修订、没有发布语义。
5. **全部复用现有机器**:创建=RuntimeEnvironment 供给(vk8s provider),关闭=Stop/Release,重置=Reset(controller 调和器现有动词);终端 attach 复用实践会话机器(改绑用户);空闲 TTL/最长存活回收兜底,显式关闭是用户主动清理。
6. **暂时只提供 vk8s 场景**;node/Incus 以后需要再加。
7. **删除一次性完成**:旧文档生成世界在单个提交内删净,任何时刻只有一套实现;不做并行、不做兼容层。

### 1. 契约定稿

- **绑定键**:`(user_id, 库钉住身份)`——库钉住身份取 library PinnedContext 的 `source_id+commit+language`(整个库一个钉住版本,不含页面)。RuntimeEnvironment 命名确定性派生自绑定键,重建幂等(同键 AlreadyExists 则收养,同运维验证环境先例)。
- **状态机(会话级,非工作流)**:`None → Creating → Ready → (Reset→Creating | Closed→None)`;供给失败落 `Failed`(可重试创建)。TTL 到期回收视为回到 None。状态查询走现有环境读模型。
- **API(登录用户)**:
  - `GET  /api/documentation/scenario` → `{state: none|creating|ready|failed, environment_id?}`;
  - `POST /api/documentation/scenario` → 创建(幂等:已有非终态即返回现状;Failed/None 可重建);
  - `POST /api/documentation/scenario/reset` → Ready 态专用(Reset 后回 Creating);
  - `DELETE /api/documentation/scenario` → 关闭释放(Stop+Release)。
  - openapi 同步再生成(`make generate`)。
- **终端**:`POST /api/documentation/scenario/terminal`(或复用现有 attach 端点改绑)——Ready 后可 attach,面板复用现实践面板的终端组件。
- **配额与生命周期**:每用户每库 1 个并发环境(CREATE 遇存量非终态返回现状);空闲 TTL/最长存活沿用现有 LifecyclePolicy(配置沿用实践环境既有值);重置不清 TTL 计时(Reset 后重新计)。
- **前端**:文档页右侧**竖向工具栏**(创建/重置/关闭三按钮 + 状态徽标),全页面同一实例状态;实践面板改造为"空白场景终端面板"(去步骤/结论区)。
- **admin**:保留环境与队列观测(现 admin 队列面);工作流/批次/语料观测随删除消失。

### 2. 提交 1:空白场景后端

- [ ] `internal/application/`新建空白场景服务(建议 `documentscenarios/`):绑定键解析、三动词(创建/重置/关闭)映射到 EnvironmentProvider 的 Provision/Reset/Stop+Release、状态读、每用户唯一约束(确定性环境名+收养语义);
- [ ] 传输层:§1 四个端点 + openapi 再生成;读者登录态鉴权(与现实践会话一致);
- [ ] bootstrap:服务装配(复用现有 k8s EnvironmentProvider/controller/LifecyclePolicy);
- [ ] 单测:服务状态机(None→Creating→Ready;Reset;Closed;Failed 重建;幂等创建;配额 1)——用假 provider;handler 测试(含未登录/状态冲突);
- 验证:`make test-unit`;`make verify-generated`;`make lint`。

### 3. 提交 2:前端工具栏与面板改绑

- [ ] 文档页右侧竖向工具栏组件(三按钮+状态徽标,按 GET 状态渲染;Creating 轮询到 Ready);
- [ ] 终端面板改绑空白场景(去实践步骤/结论渲染);移除锚点实践按钮与"已发布实践"查询;
- [ ] web 组件测:工具栏状态渲染、按钮可用性矩阵(none/creating/ready/failed)、面板 attach;
- 验证:`make web-test-unit`。

### 4. 提交 3:旧文档生成世界一次性删除

- [ ] **Go**:`internal/domain/documentpractice/`、`internal/application/documentpractice/`(全部:agent_pipeline/service/batch/batch_scheduler/watchdog/pipeline/candidate_generation/orchestrator/boundary/ignition)、`internal/adapter/llm/documentpractice.go`、`internal/adapter/postgres/documentpractice_repository.go`+`schema_documentpractice.go`、transport 的 documentation 点火/admin 工作流与批次与语料端点、bootstrap 的文档管线与批次装配;`document_batches`/`document_batch_items` 表及仓库方法一并删除;
- [ ] **保留**:docs-site 库与页面渲染服务、读者 API、RuntimeEnvironment/controller/vk8s provider、runnable 公共队列(运维场景仍用)、admin 环境与队列观测、`config/app` 的 request_timeout 15m(通用 agent 配置);
- [ ] **web**:admin 语料/批次/工作流页面与组件、12 态梯子、相关 API client 方法删除;
- [ ] **e2e 同提交改写**:`test/documentation/` 重写为空白场景套件(创建→Ready→终端输入→重置→关闭;TTL 兜底不测时长只测动词);`test/admin/` 的 batch-rollout/watchdog-controls/workflow-rescue 三套件删除(其对象已不存在),admin.setup 与 auth_flow 中文档点火用例改写;`RUN_AGENT_LIVE_E2E` 门对文档侧取消(不再依赖模型);
- [ ] 删除后全仓编译、全量单测、web 测、`make verify-generated` 绿。
- 验证:`make test-unit`;`make web-test-unit`;`make verify-generated`;`make lint`。

### 5. 提交 4:验收收口

- [ ] e2e(Kind 目标):`make test-e2e-documentation`(新空白场景套件)全绿;admin 剩余套件绿;快车道(`make test-unit`/`test-race`/`web-test-unit`/`verify-generated`)与 nightly 绿;
- [ ] TODO 收口章:方向变更决策记录(为什么放弃生成)、删除清单摘要、验收证据。

### 6. 风险与对策

- **环境成本**:每活跃读者一个 vcluster;空闲 TTL+最长存活兜底,必要时后续加全站并发上限(配置项,后续项);
- **Reset 语义**:确认 provider Reset 对 vk8s 是"清空重建 vcluster 数据"而非只重启(实现第一步核实,不符则 Reset=Release+Provision);
- **删除破坏面**:单提交原子完成可整体 revert;e2e 与 web 同提交改写保证仓库任何提交点全绿;
- **状态轮询**:Creating 阶段前端轮询 GET;后端不引入新推送机制。

### 7. 已知后续(不属本阶段)

- node/Incus 空白场景类型;全站并发上限配置;终态断言(学习闭环)若做另行立项;
- 工作区崩溃孤儿沙箱/PVC 清扫(authoring 侧遗留);js-yaml ×3 等 Dependabot;ollama critical 无上游修复;#15 typescript 7 等 vue-tsc 跟进;
- 首次真实发布后部署侧验证无凭证直拉。

## 挂起待决策(不排期)

- **内容治理/紧急下架**:场景侧非 authoring 内容无法下架、无管理员覆盖;索引 append-only 是刻意设计,与"紧急摘除"冲突。若做,方向是索引摘除/tombstone 而非删除数据——独立设计后另行立项,当前不做。
