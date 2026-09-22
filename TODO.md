# TODO

已完成的阶段见 git 历史(最近:遗留词汇清理 0672621..本提交——documentation-example
枚举与 MySpaceScenarioContentSource 退役;交付记录与验收证据见本提交的 TODO 收口章)。
本文件保留未立项事项与挂起决策。

## 阶段:遗留词汇清理——documentation-example 退役(2026-09-22 交付)

### 交付记录

- **提交 1(bdc99b7)单提交实现**:收窄而非掘除——`ScenarioType` 与 `scenario_type` 列
  保留为类型扩展缝,`operations-scenario` 成为唯一合法值(schema baseline 55→56,
  schema_catalog/schema_generation 两条 CHECK 同步收窄);删除 ScenarioDocumentationExample
  常量、materialize 两处与 portable 一处的 tags 分支、lifecycle/catalog-runtime/
  generation-repository 三处 tag 规范子句;RequireOperationsScenario 保留为 ingress
  守卫,过时注释("Documentation examples use their own source")改写为面向未来类型的
  边界叙述。MySpaceScenarioContentSource 因只剩单一值整体删除:openapi 的 content_source
  (必填字段+枚举)、my_space.go 的映射函数、三个 my-space 组件的 sourceLabel 与徽标
  (AuthoringOverview eyebrow 去首段、LearningHistory/ActiveEnvironmentList 去 pill),
  生成物两侧同步,ActiveEnvironmentList.spec fixture 同步。
- **冷坑(oapi-codegen 全局常量名去重)**:content-source 枚举删除后,"operations" 值
  不再跨枚举撞名,生成器把无关的 MySpaceActiveEnvironmentKind 常量静默改名为裸的
  Operations/Playground,编译断裂。以 `x-enum-varnames` 把该枚举常量名钉死
  (openapi 内留注释记录机制)——无关枚举的增删从此不会再改名这些常量。这是生成物
  命名稳定性的第一个已知实例,后续新枚举若含通用值宜同样钉名。
- **测试**:handlers_test 的 ContentSource 用例删除(连同 scenario 导入);三个负向
  测试改期——documentation-example 从"ingress 拒绝"(RequireOperationsScenario 文案)
  变为"内容校验即拒"(portable 的"必须为 operations-scenario"),断言对齐、用例更名
  (Tagged→LegacyType);全库仅剩 3 处 documentation-example 字符串,全部是负向夹具
  (故意写入非法类型断言被拒),属正确形态。

### 验收证据

- 快车道全套:`make test-unit`、`make test-race`、`make web-test-unit`(51 通过)、
  `make verify-generated`(生成物无漂移)、`make lint`(0 issues)、
  `npm run --prefix web build`、`kubectl kustomize .` 全绿。
- 残留扫描:`grep documentation-example|ScenarioDocumentationExample|
  MySpaceScenarioContentSource internal/ api/ web/src/` 仅命中上述 3 处负向夹具,
  生产代码与 openapi 零残留。

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
