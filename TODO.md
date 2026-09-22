# TODO

已完成的阶段见 git 历史(最近:文档入口换轨 c5e6bc0..3588e93(+审查修正 22e1a0b)——退役
解析管道,换 admin 维护的 key+url iframe 聚合页;交付记录与验收证据见 3588e93 的 TODO
收口章)。本文件保留当前阶段与未立项事项。

## 阶段:遗留词汇清理——documentation-example 退役(2026-09-22 定案,本文件即执行计划)

### 0. 背景与定案

1. **最后的死词汇**:文档练习产品删除(9809890)后,`documentation-example` 已无任何
   生产者,现存引用全是校验分支、两条 DB CHECK 与 my-space 来源徽标;且与新的
   /documentation 聚合页名词撞车,持续制造混淆。文档换轨时按当时 0.4 决策刻意留下,
   本阶段独立清掉。
2. **收窄而非掘除(定案)**:`ScenarioType` 与 `scenario_type` 列保留为类型扩展缝
   (scenario.go 注释本就如此声明),仅删除 documentation-example 值与全部分支;
   `MySpaceScenarioContentSource`(content_source 字段、openapi 枚举、三个 my-space
   组件的来源徽标与 sourceLabel)因只剩单一值而整体删除——恒定徽标是噪音,字段是
   死词汇。
3. **破坏性演进可接受**:schema baseline 55→56,两条 CHECK 收窄为
   ('operations-scenario')(开发式破坏迁移重库,无存量保留问题,前例 53→54);openapi
   MySpaceScenario 移除必填 content_source,前后端生成物同提交同步;首次真实发布尚未
   发生,无外部消费者。

### 1. 改动清单(单提交)

- [ ] content/scenario:删 ScenarioDocumentationExample 常量,Valid() 收窄,
      ScenarioType 与 RequireOperationsScenario 注释改写(去掉"Documentation examples
      use their own source"的过时叙述);materialize.go 两处"must not contain tags"
      分支删除;portable.go 类型文案改为仅 operations-scenario、删 tags 分支;
- [ ] 三处 tag 规范子句收窄:domain/scenario/lifecycle.go、domain/catalog/runtime.go、
      adapter/postgres/generation_repository.go 的
      `|| (Type==DocumentationExample && len(tags)!=0)`;
- [ ] schema:schema_catalog.go 与 schema_generation.go 的 CHECK 收窄,baseline 55→56;
- [ ] my-space:my_space.go 删 mySpaceContentSource 映射与 ContentSource 赋值;openapi
      删 content_source(属性+required),make generate 两侧同步;三个组件删
      sourceLabel 与徽标(AuthoringOverview 的 eyebrow 与 LearningHistory/
      ActiveEnvironmentList 的 pill,注意分隔符残留),ActiveEnvironmentList.spec 的
      fixture 同步;
- [ ] 测试:handlers_test 删 ContentSource 用例;content_validation 的"tagged
      documentation-example"负向测试改期类型错误;generator/source 两个 ingress 负向
      测试断言对齐新错误路径(documentation-example 从"ingress 拒绝"变为"内容校验
      即拒");
- 验证:`make test-unit`;`make test-race`;`make web-test-unit`;
  `make verify-generated`;`make lint`;`npm run --prefix web build`;
  `kubectl kustomize .`。

### 2. 风险与对策

- **错误文案变更是行为变化**:既有两个负向测试期望"operations module accepts only
  operations-scenario",收窄后 content 校验先行拒绝、文案不同——测试对齐,无对外契约。
- **CHECK 收窄 + 破坏迁移**:开发库重置语义已接受;生产未发生。

## 未立项事项

- 死信 reap 的人工重试动作(观测页已可见,处置留人工);
- 容器级资源指标(Prometheus 接入后另立项)、平均使用时长等厚统计、按用户分层限额、
  前端轮询退避;
- playground 的 node/Incus 类型、多实例(集合 API)、终态断言(学习闭环)若做另行立项;
- 工作区崩溃孤儿沙箱/PVC 清扫(authoring 侧遗留);
- js-yaml ×3 等 Dependabot;ollama critical 无上游修复;#15 typescript 7 等 vue-tsc 跟进;
- 首次真实发布后部署侧验证无凭证直拉。

## 挂起待决策(不排期)

- **内容治理/紧急下架**:场景侧非 authoring 内容无法下架、无管理员覆盖;索引 append-only 是
  刻意设计,与"紧急摘除"冲突。若做,方向是索引摘除/tombstone 而非删除数据——独立设计后另行
  立项,当前不做。
