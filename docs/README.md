# Breakfix 文档

本文档目录保存当前系统的长期说明。完整 HTTP、CRD 和运维场景字段仍以代码与生成物为准；Markdown 解释边界、所有权和操作路径，
不复制第二份机器契约。

## 权威来源

| 主题 | 权威来源 |
| --- | --- |
| HTTP JSON 契约 | [`api/http/openapi.yaml`](../api/http/openapi.yaml) |
| Kubernetes CRD | [`api/v2/`](../api/v2/) |
| Scenario 文件契约 | [`internal/content/scenario/`](../internal/content/scenario/) |
| 运行时配置 | [`config/app/local.example.yaml`](../config/app/local.example.yaml) |
| 构建、生成与测试命令 | [`Makefile`](../Makefile) |

## 架构

- [系统架构](architecture/system-architecture.md)：组件、数据所有权和部署边界。
- [代码布局](architecture/code-layout.md)：目录职责和依赖方向。
- [工作流](architecture/workflows.md)：GenerationWorkflow、CatalogRelease、lease 与阶段语义。
- [Catalog Release](architecture/catalog-release.md)：portable source、启动安装和原子提交。
- [运行环境](architecture/runtime-environments.md)：`RuntimeEnvironment`、生命周期和验证报告引用。
- [Agent Runtime](architecture/agent-runtime.md)：Eino、AgentRun、直接对话与后台 Worker 边界。
- [API 契约](architecture/api-contracts.md)：公开 HTTP、内部 Worker API 和终端流传输。

## 产品与参考

- [学习与创作体验](product/learning-experience.md)
- [运维场景内容格式](reference/scenario-format.md)
- [`assets/`](assets/)：产品设计草图与参考截图，仅用于设计沟通。

## 运维

- [部署与运行](operations/deployment.md)
- [本地开发](operations/development.md)
- [测试与真实验收](operations/testing.md)
- [备份与恢复](operations/recovery.md)

根目录 [`NEXT.md`](../NEXT.md)、[`REVIEW.md`](../REVIEW.md) 与 [`TODO.md`](../TODO.md) 分别记录后续方向、持续审查和
当前可执行工作；它们不是长期架构规范。
