# 系统架构

Breakfix 将用户交互、环境调和和后台内容工作流分开。已发布题目是文件系统 artifact；学习和
验证环境是 Kubernetes CRD；后台流程以 PostgreSQL 中的两个具体聚合持久化。

```text
Browser
  |
  v
Server <----------------------> PostgreSQL
  |                                  |
  |                           GenerationWorkflow
  |                           TaxonomyWorkflow
  |                                  |
  |                     Generate Worker x N
  |                     Taxonomy Worker x M
  v
Controller
  |
  v
NodeEnvironment / VK8sEnvironment
```

Registry 保存 K8s OCI artifact，Incus 保存 Node system-container image。两者都是运行时依赖，
不是浏览器 API 的一部分。

## 所有权

| 数据或副作用 | 权威所有者 | 其他组件的边界 |
| --- | --- | --- |
| 用户、作者会话、学习事实、AgentRun、CandidateRevision、Workflow | PostgreSQL，经 Server 写入 | Worker 不持有数据库凭据。 |
| challenge 目录与 taxonomy snapshot | Server data PVC，经 Server 写入 | Worker 只提交 typed 结果，不写文件系统。 |
| `NodeEnvironment`、`VK8sEnvironment` spec/status | Server 写 spec，Controller 写 status | Server 不直接写 status。 |
| Node image | Incus image project | Generate Worker 构建和发布；Controller 只消费正式 artifact。 |
| K8s image | 运营方提供的 OCI Registry；Kind 开发环境使用 NodePort Registry | Generate Worker 经 `registry_client_addr` 发布；节点按 `registry_addr` 拉取。 |

Server 是 Workflow 状态的唯一写者。每个 Worker 请求都携带 lease owner 与 state attempt；Server
在同一事务中校验租约、保存阶段输出并推进状态。迟到或失去租约的结果被拒绝。

## 后台流程

`GenerationWorkflow` 是一位作者确认生成后创建的唯一持久流程。Generate Worker 按状态执行
Generator、Judge、Build、Artifact Publish、真实验证、正式发布和 cleanup。它不会创建通用任务
或按 `kind` 分发阶段。

`TaxonomyWorkflow` 在题目 materialize 后由 Server maintenance 发现或创建。Taxonomy Worker 执行
Mapper、两位 reviewer 和 snapshot 发布。它独立于 GenerationWorkflow，因此 taxonomy 的重试或
review 不会占用生成流程的租约。

Authoring 对话和学习 Assistant 不属于后台 Workflow。Server 直接运行模型调用、保存会话与
AgentRun，并向浏览器提供对话结果；同一会话同时只允许一轮运行，不同会话可并发。

## 部署边界

固定 Deployment 为 Server、Controller、Generate Worker、Taxonomy Worker 和 PostgreSQL。生产 Registry
由运营方独立提供；只有 Kind 开发 overlay 会额外部署 Registry。Environment 是按用户或验证需求创建的
CRD 与动态资源，不是常驻 Deployment。

Server 当前使用 RWO data PVC，因此以单副本运行。PostgreSQL 支持 Worker lease 的跨副本接管；
将 Server 扩为多副本需要先单独解决流式连接和共享 artifact 存储，不能通过复制 Pod 伪装完成。
