# 系统架构

Breakfix 让用户在真实、隔离且可回收的 `node` 或 `k8s` 环境中学习可运行内容。平台同时支持两种内容方向：按上游文档组织的
`documentation-example`，以及带简单标签的 `operations-scenario`。两者共享构建、验证、发布、环境与历史 revision 底座，
不共享课程图或自动分类。

```text
Browser / breakfix-mcp
          |
          v
        Server ---------------- PostgreSQL
          |                       |
          |                       +-- Challenge + immutable revisions
          |                       +-- GenerationWorkflow / CatalogRelease
          |                       +-- users, sessions, learning facts, leases
          |
          +-- Authoring / Assistant / Judge / Catalog reads
          +-- Server data PVC: materialized sources, candidate archives
          |
          v
     Runtime Worker ----------- Registry / Incus / Kubernetes
          |
          v
 Controller ------------------- NodeEnvironment / VK8sEnvironment CRDs
```

## 所有权

| 数据或行为 | 权威所有者 | 说明 |
| --- | --- | --- |
| 账户、会话、学习事实、AgentRun、CandidateRevision、GenerationWorkflow、CatalogRelease、Challenge 与 revision | PostgreSQL，经 Server 写入 | Runtime Worker 不持有数据库凭据。 |
| 公开 Catalog | active Challenge revision 与对应 materialized source | 每次读取严格核验 revision 与目录。 |
| 便携场景 source、candidate archive、已发布目录 | Server data PVC | 发布目录不可变，历史 revision 继续可读。 |
| K8s artifact | 部署者提供的 OCI Registry | 按 immutable digest 读取。 |
| Node artifact | Incus image project | 按完整 fingerprint 读取。 |
| Environment CRD 和 provider 资源 | Controller | Controller 只调和 Environment，不写 Catalog 或 authoring 状态。 |

Server 是唯一的 HTTP、认证、Catalog、Authoring、Assistant 和 durable workflow 协调者。Authoring Agent 和本机
`breakfix-mcp` 调用同一 GeneratorService；Judge 是唯一后台模型角色。Runtime Worker 只运行 lease-fenced 构建、artifact
publish、验证、正式场景 publish 与资源清理。

## Catalog 与发布

空平台可在 Server 启动时按 immutable `catalog.release_reference` 安装一个 Catalog Release。所有 entry 的真实验证和 artifact
promotion 成功后，Server 在一个事务中写入 stable Challenge、active revision，并将 release 置为 Ready；因此不会公开部分题库。
基线建立后，新增与修订走 Authoring 的 `GenerationWorkflow`。详细的 portable source、bootstrap 和完整性契约见
[Catalog Release](catalog-release.md)。

Catalog 只读取 `active` Challenge 及其 active immutable revision。每个摘要返回 type、标签、runtime、标题、描述、发布时间和
可用状态。`documentation-example` 不使用标签；`operations-scenario` 的标签是 revision 级、规范化的字符串集合。课程层级、
关系边、推荐图和后台分类任务不属于系统。

## Server 生命周期

`internal/bootstrap/server` 是 Server 进程生命周期的唯一所有者。它先恢复 materialization、Catalog installer、未完成 Generator
workspace、Judge 和 interactive AgentRun、学习投影、Assistant lease 与 publication finalizer，再构造 HTTP Handler 和 Router。
`SetupRouter` 只登记路由，不读取或修改持久状态，也不启动 goroutine。

固定 Deployment 为 Server、Controller、Runtime Worker 和 PostgreSQL。生产 Registry 由运营方独立提供；只有 Kind 开发 overlay
额外部署 Registry。Server 当前使用 RWO data PVC，因此以单副本运行；扩展到多副本需要先单独解决流式连接和共享 artifact 存储。
