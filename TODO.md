# TODO：完成运维场景闭环并拆分产品模块

## 目标

在已经移除 Roadmap、课程图、自动分类和旧 `Challenge` 术语的基础上，完成 [NEXT.md](NEXT.md) 定义的可复现运维现场闭环，
并为“文档实践化”和“运维场景”建立独立的上层产品边界。当前阶段不实现 Kubernetes 文档同步和文档阅读器，但不能继续让
文档示例与运维场景共用同一套 Catalog、详情页和发布规则。

## 现有基线

- Catalog 已直接读取 active Scenario 及其 immutable active revision，历史 Environment 和运行记录按创建时的 revision 解析。
- portable Catalog Release、作者生成、Judge、真实 Build/Verify、显式发布、修订和弃用链路已经存在。
- Node 与 VK8s Environment 已具备终端、重置、停止、闲置回收和资源隔离能力。
- `operations-scenario` 已有规范化简单标签、搜索和筛选；Roadmap、Domain/Topic、Classifier 和 `difficulty` 已移除。
- 当前内容与界面仍强制采用题目、答案、提示和检查点形态，且 `documentation-example` 还只是同一 Scenario 模型中的类型值。

## 已确认的设计决策

1. 文档实践化与运维场景是两个独立产品模块。它们共享环境与 revision 底座，但分别拥有内容模型、Catalog、详情界面和发布规则。
2. 运维场景必须证明目标现象可从干净基线确定性复现。复现验证与参考修复验证是两个不同阶段，不能再由一次
   `answer.sh -> checks.sh` 隐式代表。
3. 检查点、提示、诊断思路、参考修复和自动答案保留为一等能力，并在提供时接受完整验证；它们用于增强学习和研究体验，
   但不是每个运维现场发布所必需的内容。
4. 第一阶段不建设复杂的自动脱敏系统。发布前继续展示完整 candidate 和 diff，由作者显式确认后发布；真实凭据、个人信息和
   客户数据仍禁止进入公开内容。
5. 离开工作台不自动停止环境。Stop 是用户明确执行的资源操作，工作台和“我的空间”都应提供入口；历史记录在停止后保留。
6. 稳定 Scenario ID、历史 revision、artifact identity、现有运行记录以及 Node/VK8s 供应和回收语义必须保持不变。

## 实施清单

### 1. 修复 Node 场景初始化路径

- [x] 将 Node 基础镜像 `runtime-init.sh` 的 bundle 路径从 `/opt/breakfix/challenge` 改为
      `/opt/breakfix/scenario`，与镜像构建、Controller 检查点和 Verifier 使用的路径保持一致。
- [x] 清理 systemd unit 和 `.golangci.yml` 中剩余的 `challenge` 描述，并全仓确认稳定 ID 前缀之外不再存在旧术语。
- [x] 增加能够阻止 Node bundle 写入路径与初始化读取路径再次漂移的自动化检查。
- [x] 通过单元测试和静态构建；真实 Node 验收继续使用专用环境，在得到明确执行指令前不运行。

### 2. 拆分复现核心与学习辅助

- [x] 为 `operations-scenario` 定义最小复现核心：现场说明、runtime、版本、环境拓扑、初始化步骤、目标现象、关键证据和复现验证。
- [x] 将“目标现象复现验证”从现有参考答案验证中拆出；发布必须先确认初始化后的现场与描述一致。
- [x] 将 checkpoints、hints、参考诊断、`solution.md` 和 `answer.sh` 改为成组的可选学习辅助；存在时继续执行严格的结构和真实运行
      校验，不存在时不得伪造完成度或空答案。
- [x] 明确条件约束，例如提供参考修复时必须同时提供修复后断言；提供 checkpoint 时只要求它实际引用的检查脚本和提示资产。
- [x] 更新 portable manifest、candidate archive、materialized revision、Judge prompt、验证报告和场景格式文档，并补充有/无学习
      辅助的 Node 与 K8s 单元测试。

### 3. 建立两个独立产品模块

- [x] 将现有 Scenario Catalog 明确收敛为运维场景 Catalog，只公开 `operations-scenario`，保留搜索、标签和 runtime 筛选。
- [x] 为文档实践化建立独立模块边界和 API namespace，后续由文档来源、版本、页面路径和章节锚点组织
      `documentation-example`，不经过运维场景标签与投稿流程。
- [x] 调整前端功能目录和应用状态，使“文档”“运维场景”“我的空间”成为可独立进入的页面；“创建场景”作为运维场景模块的
      操作入口。
- [x] “我的空间”统一展示两个模块产生的活动环境和历史记录，并标明内容来源；共享展示不反向合并两个 Catalog。
- [x] 在 Kubernetes 文档最小闭环开始实现时再开放“文档”导航，不提前发布空页面。

### 4. 补齐环境 Stop 交互

- [x] 在场景工作台标题栏为现有 Stop API 增加明确按钮，和 Reset 区分；执行前说明当前未保存状态会被销毁。
- [x] Stop 成功后返回运维场景 Catalog、刷新活动环境和运行记录，并保留已写入的历史事实。
- [x] 在“我的空间”的活动环境列表为每个环境增加 Stop 操作，支持不重新进入工作台就释放资源。
- [x] 离开工作台或切换页面只保留环境供稍后恢复，不隐式 Stop；界面应让用户看见环境仍在占用配额及其过期时间。

### 5. 清理旧产品语义

- [x] 将 README、OpenAPI 描述、Authoring Agent prompt、MCP 工具说明和前端中的“面试练习平台”“题目”“挑战”等旧定位改为
      “运维场景”“现场说明”“参考修复”和“复现验证”。
- [x] 保留学习记录、提示、检查点和参考答案等真实功能，但不再让它们定义所有 Scenario 的产品形态。
- [x] 全仓搜索受版本控制文件中的 `challenge`、Roadmap、Classifier、classification、Domain/Topic、curriculum 和 difficulty，
      只保留确有其他技术含义的词以及兼容稳定 ID。

### 6. 验证

- [x] 已运行 `make test-unit`、`make verify-generated`、`npm run build --prefix web`、`kubectl kustomize .` 和 `git diff --check`，结果均通过。
- [x] 现有单元和集成测试覆盖目标现象复现失败、可选学习辅助、参考修复失败、发布重试、revision 切换及历史 revision 读取，并已随 `make test-unit` 通过。
- [ ] Web 测试已有导航、运维场景筛选、启动/学习记录和停止相关用例；重置及无 checkpoints/参考答案展示仍需在专用环境执行 Playwright 验收。本轮按要求不运行真实 E2E。
- [ ] 在专用、可丢弃的真实环境中分别验收 Node 和 VK8s 运维场景；本轮按要求暂缓，不运行真实 E2E 或 Kind 验收。

## 提交拆分

每完成一个独立步骤及其对应验证就立即提交，不把多项工作积累成一次大提交：

1. **Node 路径修复**：修正基础镜像初始化路径、旧术语和防漂移检查。
2. **运维场景复现契约**：定义复现核心并拆出目标现象验证，不同时改前端导航。
3. **可选学习辅助**：放宽内容约束，保留 checkpoints、hints、solution 和 answer 的条件校验与展示。
4. **产品模块边界**：拆分文档与运维场景的 API/前端边界，调整顶层导航和“我的空间”来源标识。
5. **Stop 交互**：补齐工作台与活动环境列表的显式停止流程。
6. **产品文案与验证收尾**：清理旧定位、重新生成客户端并执行本轮静态和单元验收。
