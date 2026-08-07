# 系统架构

Breakfix 将用户交互、环境调和和后台内容执行分开。已发布题目是 Server data PVC 中的文件 artifact；学习和验证环境是 Kubernetes CRD；持久化状态在 PostgreSQL。

```text
Browser
  |
  v
Server <----------------------> PostgreSQL
  |                                  |
  |-- Agent Runtime                  |
  |   Authoring / Assistant           |  GenerationWorkflow
  |   Generator / Judge / Classifier  |  CatalogRelease
  |   Roadmap Planner / Reviewers     |  RoadmapRevision
  |
  +--------------------------> Runtime Worker x N
  v
Controller
  |
  v
NodeEnvironment / VK8sEnvironment
```

Catalog installer 是 Server 内的可恢复协调器，不是另一个 Deployment。Registry 保存 K8s OCI artifact，Incus 保存 Node system-container image；两者都是运行时依赖，不是浏览器 API 的一部分。

空平台的题库基线由 Server 启动配置中的 immutable Catalog Release 安装。它的 portable source、真实验证和原子公开语义见 [Catalog Release](catalog-release.md)。

## 所有权

| 数据或副作用 | 权威所有者 | 其他组件的边界 |
| --- | --- | --- |
| 用户、作者会话、学习事实、AgentRun、CandidateRevision、GenerationWorkflow、CatalogRelease、RoadmapRevision | PostgreSQL，经 Server 写入 | Runtime Worker 不持有数据库凭据。 |
| challenge 目录和持久化 Catalog source | Server data PVC，经 Server 写入和回收 | Server 从 PostgreSQL 的 revision 与非终态 publication intent 派生保留集合；Worker 只提交 typed 结果，不访问该 PVC。 |
| `NodeEnvironment`、`VK8sEnvironment` spec/status | Server 写 spec，Controller 写 status | Server 不直接写 status。 |
| Node image | Incus image project | Runtime Worker 构建/发布；Controller 只消费正式 artifact。 |
| K8s image | 运营方提供的 OCI Registry；Kind 开发环境使用 NodePort Registry | Runtime Worker 和节点都使用 `registry_repository` 的同一 authority。 |

Server 是业务状态的唯一写者。每个 Worker 请求都携带 lease owner 与 state attempt；Server 在同一事务中校验租约、保存阶段输出并推进状态。迟到或失去租约的结果被拒绝。

## 后台执行

`GenerationWorkflow` 是一位作者确认生成后创建的唯一持久流程。Server 内的 role-specific Agent Runtime 执行 Generator、Judge 和 Classifier；Runtime Worker 只执行 Build、Artifact Publish、真实验证、正式发布和当前的资源回收。两者都不创建通用任务或按 `kind` 分发阶段。

`CatalogRelease` 是 Server-owned 的初始化流程。其 Entry 与 Commit 使用同一套 Runtime Worker 构建、发布、验证和 final promotion 能力，但不进入作者工作流；全部 entry 就绪后，Server 在同一 Roadmap 写锁下公开 release revision 并建立已处理基线。

Authoring 对话和学习 Assistant 不属于后台 Workflow。Server 直接运行模型调用、保存会话与 AgentRun，并向浏览器提供对话结果；同一会话同时只允许一轮运行，不同会话可并发。

## 部署边界

固定 Deployment 为 Server、Controller、Runtime Worker 和 PostgreSQL。生产 Registry 由运营方独立提供；只有 Kind 开发 overlay 会额外部署 Registry。Environment 是按用户或验证需求创建的 CRD 与动态资源，不是常驻 Deployment。

Server 当前使用 RWO data PVC，因此以单副本运行。PostgreSQL 支持 release/worker lease 的跨副本接管；将 Server 扩为多副本需要先单独解决流式连接和共享 artifact 存储，不能通过复制 Pod 伪装完成。
