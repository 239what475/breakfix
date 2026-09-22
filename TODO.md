# TODO

已完成的阶段见 git 历史(最近:Playground 治理 5a2ce56..f8021b2——并发上限、admin 环境观测页、
My space 可见性与 e2e 收口)。本文件保留当前阶段与未立项事项。

## 阶段:环境重置的真实完成——擦除、代际与回收退避(2026-09-22 定案,本文件即执行计划)

### 0. 背景与定案(与用户逐条确认)

1. **症状与取证**:治理阶段 e2e 复盘发现 reset 在物理擦除完成前就报告完成——reset 请求后约
   4 秒 API 即回 Ready+None,而真正的资源清理(reap)43 秒后才落地;期间服务端没有任何新终端
   会话,但终端面板显示 Connected、e2e 断言照样通过(弱验证)。
2. **根因在控制器状态机,不是通知机制**:Provider.Reset 的设计是"删净旧资源→重建→新终端
   Ready 才算完成"(注释明言 namespace 异步删除时由 Controller 重试 Reset),但 Decide 里
   DecisionReset 分支先写 observed-reset-nonce 注解,此后 nonce 不再大于观察值,后续 reconcile
   全部落到 state.go:152 的 DecisionProvision——一条 ensure/采纳式路径,对删除中的残留资源
   "存在即采纳",观察到 ready 即清 Resetting(reconciler.go:238-241)。"重试 Reset"在状态机里
   不可达,这是意图与实现的裂缝。
3. **次级缺陷**:终端面板的 Connected 与 WS 真实生命周期脱钩;球的 resetWipePending 是对同一
   问题的时间性前端补丁(靠"GET 观察到过 creating"来猜测),服务端修正后应以代际判定替代。
4. **回收重试平铺 5 秒、无上限**:正常路径 7 次重试,上次 stuck 场景 110 次;观测页已可见,
   缺退避、上限与死信态。重置的真实完成依赖回收时延可控,本阶段一并处理。

### 1. 契约定稿

- **Reset 语义**:`operation=Resetting` 期间 Decide 持续选择 DecisionReset(采纳式 Provision
  不再参与 reset 生命周期);完成的唯一定义 = 旧资源删净(namespace 消失)→ 重建 → 新终端
  Ready,届时 phase=Ready、operation=None。超时仍走 ResetTimeoutSeconds → Failed(可重建),
  不再假 Ready。该修正作用于共享状态机,operations 场景的 reset 同步受益并同提交改绑单测。
- **代际号**:CRD status 回显 `observedResetNonce`(创建为 0,每次 Reset 递增);
  `GET /api/playground` 响应增加可选 `generation`。前端与 e2e 以"generation 变化"判定某个
  ready 属于擦除后的一代,替代时间性猜测。
- **终端面板**:Connected 仅在 WS open 时显示;断开立即转重连提示,重连必须走新 ticket。
- **回收退避与死信**:runnable_reaps 重试由平铺 5s 改为指数退避(基数 5s、封顶 5min、尝试
  上限 20 次,约 75 分钟进入死信),超限置 `dead`(schema baseline 53→54,CHECK 与 openapi
  枚举同步);admin 回收队列展示 dead 徽标。死信只做可见:环境名按用户确定性派生,重建换 UID
  即新 namespace,死信只泄漏资源不阻塞用户,人工重试动作列已知后续。
- **e2e**:reset 段强断言——回显新 marker、断言旧 marker 消失、GET 的 generation 已变化;
  reset 段时限按真实擦除时长放宽(冷目标分钟级)。

### 2. 提交 1:后端 reset 语义与代际

- [ ] Decide:operation=Resetting 持续 DecisionReset;DecisionProvision 不再处理 Resetting 态;
      DecisionReset 分支的注解写入幂等化;phase 合法性校验同步调整;
- [ ] reconciler:reset 全程一条路径(Delete-until-gone → 重建 → Ready 才清 operation);复核
      vk8s ResetTimeoutSeconds 量级(需覆盖 namespace 删除 + 重建);
- [ ] CRD `status.observedResetNonce` + `make generate`;`GET /api/playground` 响应增加
      generation(openapi 演进,可选字段);
- [ ] 单测:state(Resetting 持续选 Reset、超时/非法相位)、reconciler(删除未净仍 Resetting、
      删净重建后 Ready+清 operation、reset 期间采纳式 Provision 不可达)、operations 场景
      reset 单测改绑;
- 验证:`make test-unit`;`make verify-generated`;`make lint`。

### 3. 提交 2:回收退避死信与前端消费

- [ ] reap 指数退避 + 尝试上限 + `dead` 态(schema baseline 53→54、CHECK/openapi 枚举、
      postgres 单测覆盖退避序列与上限置 dead);
- [ ] admin 回收队列 dead 徽标(openapi 再生成横跨两侧,同提交同步);
- [ ] 终端面板 Connected 对真:状态绑定 WS 生命周期,断开转重连提示,重连走新 ticket;
- [ ] 球:resetWipePending 改为代际判定(记录点击时的 generation,ready 且 generation 未变
      不算数),组件测以代际语义重写"陈旧 ready 不可信"断言;My space playground 行在
      Resetting 期间的显示对齐(可选小改);
- 验证:`make test-unit`;`make web-test-unit`;`npm run --prefix web build`;
  `make verify-generated`;`make lint`。

### 4. 提交 3:e2e 强断言与收口

- [ ] playground e2e reset 段:新 marker 回显、旧 marker 消失、generation 变化断言;时限放宽;
- [ ] 快车道(`test-unit`/`test-race`/`web-test-unit`/`verify-generated`/`lint`)与
      `make test-e2e-documentation` 全绿;关闭后无残留复核(CR 清空、无 vk8s 命名空间、
      reap 行终态);
- [ ] TODO 收口章:交付记录、验收证据。

### 5. 风险与对策

- **reset 时长显性化**:等待真擦除+重建,冷目标分钟级;ResetTimeoutSeconds 不足会转 Failed
  (用户重建)——比假 Ready 诚实,可接受;e2e 时限放宽与 prepare 镜像预热缓解。
- **共享状态机改动波及 operations 场景**:场景 reset 单测同提交改绑;operations 真实链路
  (node/k8s e2e)本阶段不重跑,以单测为准——如需真实目标回归,执行时另行确认成本。
- **dead 态是 schema/openapi 破坏性演进**:baseline 递增与枚举同步,生成物同提交全绿。
- **代际判定重写球逻辑的回归风险**:保留"陈旧 ready 不可信"语义的组件测等价断言(以代际
  语义重写)。
- **退避拉长故障恢复间隔**:next_attempt_at 已在观测页可见;死信进入 admin 视野,处置留人工。

### 6. 已知后续(不属本阶段)

- 死信 reap 的人工重试动作(本阶段只做可见);容器级资源指标(Prometheus 接入后另立项)、
  平均使用时长等厚统计、按用户分层限额、前端轮询退避;playground 的 node/Incus 类型、
  多实例(集合 API)、终态断言(学习闭环)若做另行立项;
- 工作区崩溃孤儿沙箱/PVC 清扫(authoring 侧遗留);js-yaml ×3 等 Dependabot;ollama critical
  无上游修复;#15 typescript 7 等 vue-tsc 跟进;
- 首次真实发布后部署侧验证无凭证直拉。

## 挂起待决策(不排期)

- **内容治理/紧急下架**:场景侧非 authoring 内容无法下架、无管理员覆盖;索引 append-only 是
  刻意设计,与"紧急摘除"冲突。若做,方向是索引摘除/tombstone 而非删除数据——独立设计后另行
  立项,当前不做。
