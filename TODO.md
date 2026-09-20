# TODO

已完成的阶段见 git 历史（最近：fixture 退役与 reviewer 契约修复，验收章见 119f034；docs
上游 canary 与发布前收尾含 major PR 清偿见 dc85e1d；PR 门禁与 Dependabot 自动合并见
3b2c47d；工程缺口修复含 release 链真实走通见 1b7c94e；CI 快车道与按需 nightly 见
dcaac06；E2E 剖面化见 aaaf01d、bf766e5、1446833）；本文件保留当前阶段与未立项事项。

## 文档管线门禁拒绝自动重试（当前阶段，2026-09-21 立项）

背景：fixture 退役后 live 验收打通真实模型全链——reviewer 契约修复（载荷收缩 + 服务端
盖戳 + 枚举入 schema）后 typed rejection 归零，管线机械贯通。随后真门禁做出了一次正确
裁决：planner 生成的练习存在逻辑矛盾（观察点不可观察——启动命令 `echo hello >
/data/note` 每次容器启动重建文件，"重建后消失"的断言永假），evidence reviewer 以精准
理由拒绝。问题在拒绝之后：文档管线 `Rejected` 即终态，恢复只能靠管理员 Restart。

运维 GenerationWorkflow 的既有模式不是这样——**拒绝后带反馈回到 Generating**（三层：
artifact 语义错误自动重试 generation_repository.go:446、验证未过自动 :551、作者修复
请求 :669；`last_error` 反馈随重试回灌下一轮生成）。文档侧重试机器其实齐备（Restart
递增 WorkflowRevision、账本写全新条目），缺的只是"拒绝后自动带理由回灌重跑"这一跳。

关键决策（2026-09-21 与用户确认，对齐运维模式）：

- **门禁拒绝 → 内部复用 Restart 机器**：plan-gate / candidate-gate 拒绝且 revision <
  max_revisions 时，管线自身触发工作流重启（等价管理员 Restart：WorkflowRevision 递增、
  账本全新条目），触发者从管理员变为管线；
- **拒绝理由回灌**：门禁 `reasons` 注入下一轮 planner/generator 提示词，等价运维侧
  `last_error` 反馈；文档正文仍是不可信数据的原则不变，反馈属于可信协议层；
- **有界**：默认重试 2 次（共 3 次尝试）；预算耗尽或 `HardReject` 落终态 `Rejected`
  交管理员救援——对应运维"确定性错误进终态"的同一原则；
- **账本不可变**：每次尝试独立条目，审计链完整（WorkflowRevision 语义即为此设计）。

提交拆解：

### 提交 1 状态机：拒绝 → 有界自动重启

- [ ] GatePlan / candidate gate 拒绝分支：revision 预算检查 + 内部 Restart（拒绝理由
      入参）+ 下一轮调度衔接；
- [ ] max_revisions 语义定稿（3 次尝试）；HardReject 与预算耗尽 → Rejected 终态，
      admin rescue 语义不变；
- [ ] 单测：拒绝后自动重启一次即成功发布 / 预算耗尽落终态 / HardReject 不重试 /
      账本每次尝试独立成条。

### 提交 2 反馈回灌

- [ ] planner/generator 提示词携带上一轮门禁 reasons（feedback 上下文）；
- [ ] 单测：反馈进入 prompt；反馈不触碰 Server 拥有的约束解析与盖戳字段。

### 提交 3 live 验收收口（承接 fixture 退役阶段遗留两项）

- [ ] RUN_AGENT_LIVE_E2E=1 make test-e2e-documentation 全绿（发布经重试轮次收敛，
      等待窗覆盖 max_revisions 全预算）；
- [ ] spec 2 的 ensurePracticePublished 死代码分支修复（fixture 时代从未触发：新注册
      用户非 bootstrap admin，调用即 403 "admin role required"）——发布已在则早退，
      未在则以正确身份语义驱动；
- [ ] RUN_AGENT_LIVE_E2E=1 make test-e2e-admin 全绿；
- [ ] 快车道/nightly 绿。

### 已知后续（不属本阶段）

- js-yaml ×3 告警等 Dependabot 分组更新；ollama critical 无上游修复版；#15
  typescript 7 等 vue-tsc 跟进；
- 首次真实发布后部署侧验证无凭证直拉。

## 挂起待决策（不排期）

- **内容治理/紧急下架**：场景侧非 authoring 内容无法下架、无管理员覆盖;实践内容
  PracticeRevision 一侧 append-only(索引只进不改)是刻意设计,与"紧急摘除"冲突。若做,
  方向是索引摘除/tombstone 而非删除数据——独立设计决策后另行立项,当前不做。
