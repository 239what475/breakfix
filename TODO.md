# TODO：移除 Roadmap 并简化内容模型

## 目标

从当前项目中完整移除 Roadmap、课程图和自动分类流程，使 Catalog 直接由已发布内容及其 active revision 构成。运维现场仅
保留简单标签；文档可执行示例将来按上游文档位置组织，不进入标签或课程体系。发布版本、真实运行验证、环境隔离、历史
revision 与学习/运行记录必须保留。

这项工作只清理现有架构并建立简单的发布与检索边界，不在同一批提交中实现文档同步、文档阅读器或完整的运维现场投稿体验。
两个产品方向的定义见 [NEXT.md](NEXT.md)。

## 已确认的设计决策

1. 删除 Domain、Topic、Topic/Challenge edge、Challenge binding 和 `RoadmapRevision`，不建立替代关系图。
2. 删除 Roadmap Planner、双 Reviewer、maintenance workflow、相关 Agent prompt、lease、调试入口和配置。
3. 删除发布流程中的 Classifier 与人工分类审核。内容通过 Judge 和真实运行验证后，由作者确认即可进入发布。
4. Catalog 直接读取状态为 active 的内容及其 active immutable revision；旧 revision 继续供既有 Environment 和历史记录读取。
5. 初始 Catalog Release 只列出内容 entry。所有 entry 验证和 artifact promotion 成功后，在一个数据库事务中创建内容身份、
   active revision 并将 release 标记为 Ready，避免暴露半次安装。
6. 运维现场的标签直接属于其不可变内容 revision，采用经过规范化的简单字符串集合；不单独维护 Tag 实体、定义、关系或审核
   workflow。每个现场最多有 8 个标签；每个标签去除首尾空格后包含 1～32 个字符，只允许中文、英文字母、数字、`.`、`+`、
   `-`，英文字母统一转为小写。标签按规范化结果去重和确定性排序，并直接使用该结果展示。平台暂不合并 `k8s` 与
   `kubernetes` 等同义标签。标签修改视为内容修订，并重新走发布流程。
7. 文档可执行示例不使用这些标签。其来源、上游 revision、页面路径和章节锚点将在后续文档实践化设计中定义。
8. 删除 `difficulty`。它不属于文档示例，也不能客观描述真实运维现场；portable manifest、materialized content、数据库投影、
   API 和界面均不再保留该字段或筛选条件。
9. `Scenario` 是 Roadmap 清理后的通用可运行内容对象。现有代码中的 `Challenge` 命名在清理期间作为兼容术语保留，避免
   机械改名掩盖数据与发布语义的变化；清理完成后统一改为 `Scenario`。内容类型至少区分
   `documentation-example`（文档可执行示例）和 `operations-scenario`（可复现运维现场），用户界面分别显示为“可执行示例”和
   “运维场景”。两者共享运行底座，但不合并内容来源、组织方式或发布标准。
10. 当前数据库只支持开发期整库重建，因此本次通过提升 schema version 和重建 schema 完成，不编写旧 Roadmap 数据迁移。

## 不做的事项

- 不保留“以后也许有用”的 Roadmap 表、API、类型或死代码；Git 历史已经保存旧实现。
- 不用新的 Taxonomy、Category、Knowledge Graph 或推荐系统替换 Roadmap。
- 不让 AI 自动决定标签，也不增加发布后的后台关系维护任务。
- 不改变 Node/VK8s Environment 的供应、终端、checkpoint 执行、重置和回收语义，除非它们直接依赖 Roadmap 标识。
- 不在这项清理中同步 Kubernetes 文档或设计第三方文档许可证数据库。

## 实施清单

### 1. 固定替代契约

- [ ] 为现有可运行内容定义最小 Catalog 读模型：稳定 ID、active revision、类型、标题、描述、runtime、简单标签、发布时间和
      可用状态；从内容模型中删除 `difficulty`。
- [ ] 将规范化标签加入 portable manifest、materialized content 与 immutable revision 元数据；按已确认的字符、大小写、长度、
      数量、去重和排序规则实现同一套确定性校验。
- [ ] 明确 list/get/start Environment 均解析同一个 active revision，历史 Environment 始终按创建时保存的 revision 读取。
- [ ] 明确弃用只切换内容状态并阻止新 Environment，不删除旧 revision、artifact、运行记录或用户进度。

### 2. 简化数据库与 Catalog

- [ ] 从 schema 中删除全部 `roadmap_*` 表和外键，提升开发数据库 schema version。
- [ ] 让 Catalog 查询直接联结 active content identity 与 active revision，并在数据库层提供搜索、标签、runtime 和状态筛选。
- [ ] 删除 `RoadmapStore`、Roadmap repository、retrieval、content parser/compiler、testkit 与对应测试。
- [ ] 保留并补测 materialized source 的 hash 校验、active revision 一致性以及缺失内容时 fail closed 的行为。
- [ ] 更新弃用和内容修订事务，使其只维护 stable identity 与 active revision pointer。

### 3. 简化发布流程

- [ ] 将 portable Catalog Release 改为只有 entries 及各自 content revision，删除 `roadmap/` 目录和
      `release.yaml.roadmap`。
- [ ] 更新 `cmd/catalog-release` 的校验、revision 计算和打包逻辑，并迁移正式 Catalog 与测试 fixture。
- [ ] 修改 Catalog Release finalizer：所有 entry 就绪后原子提交内容身份/revision 与 release Ready 状态，不再编译或发布
      `RoadmapRevision`。
- [ ] 从 GenerationWorkflow 删除 `Classifying`、`NeedsClassificationReview`、classification proposal/feedback 和
      Roadmap revision fence；作者确认已验证内容后直接进入 `ChallengePublishing`。
- [ ] 删除 Classifier executor、prompt、retrieval tools、AgentRun purpose、HTTP/MCP 操作以及 Authoring 中的分类审核交互。
- [ ] 保留 Judge、Build、ArtifactPublish、Verify、作者内容审核、显式发布和可恢复 finalizer，并补测简化后的完整状态转移。

### 4. 删除 Roadmap 运行时与接口

- [ ] 删除 Roadmap maintenance domain/application/adapter/bootstrap 代码以及启动、接管、lease 和恢复逻辑。
- [ ] 删除 Roadmap export、maintenance/debug HTTP API 及 OpenAPI schema，重新生成 Go 与 TypeScript client。
- [ ] 从 Challenge/Catalog 响应移除 Domain、Topic、关系边和 Roadmap 对象，改为返回简单标签。
- [ ] 从 portable manifest、materialized content、数据库投影、OpenAPI 和前端中删除 `difficulty`。
- [ ] 删除工作台 `RoadmapPanel`、Catalog 的 Domain/Topic/难度筛选及关系图；Catalog 只保留搜索、标签和必要的运行属性筛选。
- [ ] 更新 Authoring 状态文案与界面，移除分类 proposal、分类反馈和分类确认步骤。

### 5. 清理配置、部署和文档

- [ ] 删除 Roadmap maintenance 的模型、轮询、阈值、并发和调试配置，以及对应部署参数与 Secret 要求。
- [ ] 更新 README、架构、工作流、Catalog Release、恢复、测试和产品文档，确保 Roadmap、Domain/Topic 和 Classifier 不再被描述
      为现行能力。
- [ ] 删除正式 Catalog 和测试 fixture 中的 `roadmap/` 内容，给运维现场添加直接标签。
- [ ] 全仓搜索 `roadmap`、`Roadmap`、`Domain`、`Topic`、`Classifier` 和 classification，只保留历史说明或确有其他含义的用法。

### 6. 统一 Scenario 术语

- [ ] Roadmap 清理并验收通过后，将领域模型、内容模型、数据库、API、前端和运行记录中的 `Challenge` 统一改为 `Scenario`，
      同步更新稳定 ID 之外的类型名称、路由、事件和错误信息。
- [ ] 为 Scenario 增加明确的内容类型：`documentation-example` 与 `operations-scenario`；禁止用类型名称重新引入课程层级或
      关系图。
- [ ] 为两种类型分别定义最小元数据和发布校验：文档示例绑定上游文档位置，运维场景保存简单标签；两者继续共享环境构建、
      验证、重置和回收能力。
- [ ] 补充迁移后的 API、前端、运行记录和历史 revision 兼容性测试，确保已有 Scenario 的 stable ID、artifact 和学习记录不变。

### 7. 验证

- [ ] 单元测试覆盖 manifest 标签校验、Catalog 查询、发布、修订冲突、弃用与历史 revision 读取。
- [ ] Catalog Release 集成测试证明失败不会暴露部分内容，重启可恢复，重复提交保持幂等。
- [ ] Authoring 集成测试证明 verified content 经一次作者确认即可发布，失败和重试仍保留正确 candidate/revision。
- [ ] Web 测试覆盖搜索、标签筛选、启动现场和历史记录，不再请求 Roadmap API。
- [ ] 运行 `make test-unit`、`make verify-generated`、`kubectl kustomize .`，并在专用 Kind target 上执行 Catalog prepare 与平台验收。

## 提交拆分

按以下顺序拆分提交。每完成一个独立步骤的内容并通过该步骤对应的验证，就立即创建一个单独提交；不要把多个步骤、多个
模块或一次完整清理攒成一个大提交。每个提交都应能被独立审查、回滚，并保持仓库处于可构建状态。不能先删除约束再等待
后续提交恢复正确性：

1. **Catalog 简化契约与标签**：加入 revision 级简单标签和直接 Catalog 读模型，暂时从旧 Roadmap 投影做兼容读取。
2. **发布链路去分类化**：缩短 GenerationWorkflow，删除 Classifier 和分类审核，发布时写入直接标签。
3. **Catalog Release 去 Roadmap 化**：修改 portable bundle 与原子安装事务，迁移正式内容及 fixture。
4. **删除 Roadmap 后端**：切换 Catalog 到直接读取后，删除 Roadmap schema、repository、maintenance、Agent 和 HTTP API。
5. **删除 Roadmap 前端**：移除图和 Domain/Topic 交互，完成标签搜索与简化后的 Authoring 流程。
6. **生成物、部署和文档收尾**：重新生成 API，清理配置和文档，执行本轮 Roadmap 清理的单元、集成与真实环境验收。
7. **统一 Scenario 术语**：在前六步完成并验收后，单独提交 `Challenge` 到 `Scenario` 的类型和产品用语重命名，以及两种
   Scenario 内容类型的契约和测试。
