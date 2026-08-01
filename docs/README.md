# Breakfix 文档

本文档目录记录稳定的架构决策、产品流程、内容不变量与运行说明。完整 HTTP、CRD 和
题目字段由代码与生成物维护，不在 Markdown 中复制为第二份契约。

## 权威来源

| 主题 | 权威来源 |
| --- | --- |
| HTTP JSON 契约 | [`api/openapi.yaml`](../api/openapi.yaml) |
| Kubernetes CRD | [`internal/k8s/apis/breakfix/v1/`](../internal/k8s/apis/breakfix/v1/) |
| Challenge 文件契约 | [`internal/challenge/`](../internal/challenge/) |
| 运行时配置 | [`config/breakfix.example.yaml`](../config/breakfix.example.yaml) |
| 构建与部署命令 | [`Makefile`](../Makefile) |

## 架构

- [系统架构](architecture/system-architecture.md)：Server、Controller、两个 Worker 与数据所有权。
- [Agent Runtime](architecture/agent-runtime.md)：Eino、AgentRun、直接对话与后台 Workflow 边界。
- [运行环境](architecture/runtime-environments.md)：`NodeEnvironment`、`VK8sEnvironment`、生命周期和检查点。
- [作者生成与真实验证](architecture/authoring-workflow.md)：AuthoringSession、GenerationWorkflow、CandidateRevision 与发布。
- [Taxonomy 与 Catalog 发布](architecture/taxonomy.md)：Skill、Tag、mapping 委员会和公开题目准入。
- [HTTP 与终端接口](architecture/http-api.md)：认证、接口分组、WebSocket/SSE 边界与生成契约。

## 产品与内容

- [学习与创作体验](product/learning-experience.md)
- [题目内容格式](content/challenge-format.md)

## 运维与资源

- [部署与运行](operations/deployment.md)
- [Telepresence 本地调试](operations/telepresence.md)
- [测试与真实验收](operations/testing.md)
- [`assets/`](assets/)：产品设计草图与参考截图，仅用于设计沟通。

根目录 [`NEXT.md`](../NEXT.md) 是下一阶段方向，[`todo.md`](../todo.md) 是当前执行清单；两者
不是长期架构规范。
