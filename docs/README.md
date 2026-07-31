# Breakfix 文档

本文档目录记录稳定的架构决策、产品流程、内容不变量与运行说明。机器可验证的完整字段和路由不在 Markdown 中复制维护，而以代码和生成物为准。

## 权威来源

| 主题 | 权威来源 | 文档用途 |
| --- | --- | --- |
| HTTP JSON 契约 | [`api/openapi.yaml`](../api/openapi.yaml) | 路由、请求和响应模型；Go 路由模型和前端类型由此生成。 |
| Kubernetes CRD | [`internal/k8s/apis/breakfix/v1/`](../internal/k8s/apis/breakfix/v1/) | `NodeEnvironment`、`VK8sEnvironment` 的字段和校验标记。 |
| Challenge 文件契约 | [`internal/challenge/`](../internal/challenge/) | 题目目录、检查点、归档与发布规则。 |
| 运行时配置 | [`config/breakfix.example.yaml`](../config/breakfix.example.yaml) | 支持的 YAML 键和安全的示例值。 |
| 构建与部署命令 | [`Makefile`](../Makefile) | 本地、测试、构建和集群镜像发布 target。 |

## 架构

- [系统架构](architecture/system-architecture.md)：Server、Controller、固定 Worker 与数据所有权边界。
- [Agent Runtime](architecture/agent-runtime.md)：Eino、PostgreSQL AgentRun/WorkItem、Worker 与 OpenSandbox 边界。
- [运行环境](architecture/runtime-environments.md)：`NodeEnvironment`、`VK8sEnvironment`、生命周期和检查点。
- [作者生成与真实验证](architecture/authoring-workflow.md)：作者会话、CandidateRevision、固定流水线与发布。
- [Taxonomy 与 Catalog 发布](architecture/taxonomy.md)：Skill、Tag、Mapping 委员会和公开题目准入。
- [HTTP 与终端接口](architecture/http-api.md)：认证、接口分组、WebSocket/SSE 边界与契约生成。

## 产品与内容

- [学习与创作体验](product/learning-experience.md)：Catalog、Workspace、Assistant、Authoring 和 My space。
- [题目内容格式](content/challenge-format.md)：发布目录、运行时初始化、检查点和教学资产。

## 运维与资源

- [部署与运行](operations/deployment.md)：本地环境、集群准备、运行时镜像发布和验证。
- [Telepresence 本地调试](operations/telepresence.md)：本地接管集群 Server、Controller 或任一固定 Worker。
- [测试与真实验收](operations/testing.md)：确定性测试、运行时、浏览器和模型验收边界。
- [`assets/`](assets/)：产品设计草图与参考截图，仅用于设计沟通。

根目录 [`NEXT.md`](../NEXT.md) 是下一阶段方向，[`todo.md`](../todo.md) 是当前执行清单；两者不是长期架构规范。
