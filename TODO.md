# TODO

已完成的阶段见 git 历史。本文件只保留最近一个阶段的收口记录与未立项事项。

## 阶段收口:文档空白实践场景(2026-09-21 定案,2026-09-21 完成)

### 方向变更决策

文档侧放弃一切生成:页面本身是教材,系统只提供"场地"。原先的答案——按页面锚点生成实践
(planner→门禁→generator→门禁→物化→验证→发布)、批次铺量、看门狗、门禁拒绝自动重试——
在投入 live 验收前被产品决策整体否决。替代答案是**每用户一个空白 vk8s 场景**:钉住库范围内
全局唯一,跨页面携带;右侧竖向工具栏三动词(创建/重置/关闭);无锚点、无目录建模、无发布语义。
空白场景就是 RuntimeEnvironment + 终端会话。

### 交付(五笔提交)

1. `b9424a2` 空白场景后端:RuntimeEnvironment CRD 增加可选 `blankRuntime` 臂(CEL 与
   `runnableRevisionRef` 互斥且不可变);reconciler 从控制器装配的 BlankRuntimePlan 解析空白环境,
   Reset/Reap 走既有队列;EnvironmentProvider 以管理终端镜像供给空白 k8s 环境,终端通过
   `BREAKFIX_DEFER_INITIALIZATION` 延迟初始化(k8s-base 入口脚本扩展);传输层 GET/POST/DELETE
   `/api/documentation/scenario` + `/reset` + 终端票据;确定性环境名(用户+库钉住身份)即配额,
   创建不阻塞、就绪靠轮询,重置是 Ready 态动词,关闭幂等。
2. `bf72ebe` 前端:右侧竖向工具栏(状态徽标+三按钮,可用性矩阵 none/creating/ready/failed);
   空白场景终端面板替换实践面板;`useBlankScenario` 组合式(非阻塞创建、轮询到就绪、匿名经
   登录对话框续接);移除锚点实践按钮与每页实践索引查询。
3. `9809890` 一次性删除(105 文件,-20423 行):domain/application 两个 documentpractice 包、
   llm 适配器、postgres 仓库与 document_* 表(破坏式基线 51→52)、审计动作词表、runnable 队列
   文档绑定列、admin 工作流/语料/批次端点与页面、实践链 e2e、admin e2e 工程、模型凭证注入。
   幸存:docs-site 库与页面渲染、读者 API、RuntimeEnvironment/controller/vk8s/终端、runnable
   公共队列、admin 环境与队列观测、request_timeout 15m。库内容类型(DocumentContext 等)移入
   docsource 适配器。
4. `809cf5a` 收口前加固(2026-09-22,首跑验收暴露的真实缺陷,一并修复后重验):
   - **reaper 零超时**:空白绑定携带零值 runnable 修订,Stop/Release 直接读其 LifecyclePolicy
     得到零超时、上下文即刻过期,回收永不成功。`EnvironmentBinding.Lifecycle()` 改为按臂取策略。
   - **reap 队列外键**:`runnable_reaps.runnable_revision_digest` 原有指向 runnable_revisions 的
     外键,空白计划摘要无对应行,入队即失败(破坏式基线 52→53,去外键,摘要语义改为"内容修订
     或空白计划"二选一的栅栏)。
   - **Release 所有权门**:b9424a2 声称空白 Release 以环境身份为栅栏,但 vk8s 所有权门未实现
     ——Release 请求不带摘要,严格校验必败,而宽容路径要求 vcluster 标记(正常供给的 namespace
     从未有)。现场:空白环境回收重试 110 次卡死 finalizer,e2e 首跑 prepare 因此超时。修复:
     空白 Release(Blank 且无摘要)跳过摘要注解比对,UID 注解仍是栅栏;供给/重置保持摘要严格围栏。
   - **供给期不可见**:label 查找在控制器首次观察前看不到环境(runtime provider 投影为空),
     工具栏回退 none;改为按确定性名直读 + 身份校验。轮询 GET 在 Pending/Provisioning/Resetting
     顺带续租,慢供给不再被空闲 TTL 饿死;Ready 后仍只认真实使用续租。
   - **CRD 引用指针化**:`runnableRevisionRef` 值类型改指针,空白环境不再携带空壳引用;CEL 改
     `has()` 语义("存在即完整"),id/digest 落 `MinLength=1` 字段级校验。
   - e2e 登录辅助本地化(套件无 baseURL,经绝对 URL 驱动),`totpCode` 导出复用。
5. 收口提交(本笔):文档 e2e 全绿证据 + 本记录。

### e2e 形态

`test/documentation/scenario.e2e.spec.ts`:注册登录→工具栏创建→轮询 Ready→终端连接与命令回显
(真实 vcluster)→重置回 Creating→再就绪→关闭回 None;TTL 兜底不测时长只测动词。
`reader.smoke.spec.ts`:生成内容契约(锚点/告警/shiki)+视口驱动的目录抽屉+匿名工具栏静默。
文档套件不再依赖模型(`RUN_AGENT_LIVE_E2E` 门取消,prepare 不再注入凭证);admin e2e 工程随对象
一起消失,admin 控制台保留用户与审计两页。

### 风险核实记录

- **Reset 语义**:`EnvironmentProvider.Reset` 对 k8s 是先 `Delete`(整个 namespace/vcluster/
  kubeconfig/终端 pod)再从零 `provisionK8s`,即"清空重建",符合定案,无需 Release+Provision 替代。
- **计划摘要漂移**:空白环境的 Release 以环境身份为栅栏(UID)——首跑实测证明原实现并未兑现
  (所有权门拒空白 Release,见交付 4),修复后供给/重置路径仍以摘要严格围栏(状态绑定 profile
  digest,漂移即终态 Failed),Release 路径按 UID 放行。
- **每活跃读者一个 vcluster 成本**:IdleTTL 900s/MaxLifetime 1800s 沿用实践环境既有值兜底;
  全站并发上限留作后续项。

### 验收证据

- 快车道:`make test-unit`、`make test-race`、`make web-test-unit`、`make verify-generated`、
  `make lint` 全绿(2026-09-21 首验,2026-09-22 加固后复验)。
- Kind 验收:`make test-e2e-documentation`。
- E2E 结果:2026-09-21 首跑 PASS;2026-09-22 加固批后复跑 PASS(scenario-e2e + reader-smoke
  2 passed),关闭后的空白环境 reap 两跳内 succeeded,集群无残留 RuntimeEnvironment/vk8s
  namespace。

## 已知后续(不属本阶段)

- node/Incus 空白场景类型;全站并发上限配置;终态断言(学习闭环)若做另行立项;
- 工作区崩溃孤儿沙箱/PVC 清扫(authoring 侧遗留);js-yaml ×3 等 Dependabot;ollama critical
  无上游修复;#15 typescript 7 等 vue-tsc 跟进;
- 首次真实发布后部署侧验证无凭证直拉。

## 挂起待决策(不排期)

- **内容治理/紧急下架**:场景侧非 authoring 内容无法下架、无管理员覆盖;索引 append-only 是
  刻意设计,与"紧急摘除"冲突。若做,方向是索引摘除/tombstone 而非删除数据——独立设计后另行
  立项,当前不做。
