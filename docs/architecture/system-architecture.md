# 系统架构

Breakfix 让用户在真实、隔离且可回收的 `node` 或 `k8s` 环境中研究可运行内容。当前公开产品是带简单标签的
`operations-scenario`。文档实践化将使用独立的文档来源、页面和锚点模型；它复用运行环境底座，但不进入当前 Scenario、Catalog
或作者投稿链路。

```text
Browser / breakfix-mcp
          |
          v
        Server ---------------- PostgreSQL
          |                       |
          |                       +-- Scenario + immutable revisions
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
 Controller ------------------- RuntimeEnvironment CRD
```

## 所有权

| 数据或行为 | 权威所有者 | 说明 |
| --- | --- | --- |
| 账户、会话、学习事实、AgentRun、CandidateRevision、GenerationWorkflow、CatalogRelease、Scenario 与 revision | PostgreSQL，经 Server 写入 | Runtime Worker 不持有数据库凭据。 |
| 运维场景 Catalog | active `operations-scenario` revision 与对应 materialized source | 每次读取严格核验 revision 与目录。 |
| 便携场景 source、candidate archive、已发布目录 | Server data PVC | 发布目录不可变，历史 revision 继续可读。 |
| K8s artifact | 部署者提供的 OCI Registry | 按 immutable digest 读取。 |
| Node artifact | Incus image project | 按完整 fingerprint 读取。 |
| Environment CRD 和 provider 资源 | Controller | Controller 只调和 Environment，不写 Catalog 或 authoring 状态。 |

Server 是唯一的 HTTP、认证、Catalog、Authoring、Assistant 和 durable workflow 协调者。Authoring Agent 和本机
`breakfix-mcp` 调用同一 GeneratorService；Judge 是唯一后台模型角色。Runtime Worker 只运行 lease-fenced 公共 materialize、
verify 与资源清理；Server application finalizer 原子写入产品发布状态。

## Catalog 与发布

空平台可在 Server 启动时按 immutable `catalog.release_reference` 安装一个 Catalog Release。所有 entry 的公共 materialize 和真实验证
成功后，Server 在一个事务中写入 stable Scenario、active revision 及公共引用，并将 release 置为 Ready；因此不会公开部分 Catalog。
基线建立后，新增与修订走 Authoring 的 `GenerationWorkflow`。详细的 portable source、bootstrap 和完整性契约见
[Catalog Release](catalog-release.md)。

运维场景 Catalog 只读取 `active operations-scenario` 及其 active immutable revision。每个摘要返回标签、runtime、标题、描述、
发布时间和可用状态。标签是 revision 级、规范化的字符串集合。文档实践化预留 `/api/documentation` namespace，并在
`internal/application/documentpractice` 中维护上游页面位置；在完成来源同步和阅读器前不开放空导航或复用此 Catalog。课程层级、
关系边、推荐图和后台分类任务不属于系统。

## Server 生命周期

`internal/bootstrap/server` 是 Server 进程生命周期的唯一所有者。它先恢复 materialization、Catalog installer、未完成 Generator
workspace、Judge 和 interactive AgentRun、学习投影、Assistant lease 与 publication finalizer，再构造 HTTP Handler 和 Router。
`SetupRouter` 只登记路由，不读取或修改持久状态，也不启动 goroutine。

固定 Deployment 为 Server、Controller、Runtime Worker 和 PostgreSQL。生产 Registry 由运营方独立提供；只有 Kind 开发 overlay
额外部署 Registry。Server 当前使用 RWO data PVC，因此以单副本运行；扩展到多副本需要先单独解决流式连接和共享 artifact 存储。
