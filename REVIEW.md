# 当前架构审查

## 总体判断

当前主架构已经收敛到清晰的所有权边界：

- Server 是业务状态、Agent、Catalog、Roadmap 和 Server data PVC 的唯一所有者。
- Runtime Worker 只执行 Build、Artifact Publish、Verify、Challenge Publish 和 provider resource reaping，不访问数据库、模型或 Server PVC。
- Controller 只调和 NodeEnvironment 与 VK8sEnvironment。
- PostgreSQL 保存 workflow、lease、AgentRun、revision 和学习事实。
- Registry、Incus 与 OpenSandbox 分别提供 immutable artifact、Node runtime 和 Generator workspace。

这些边界与当前实现基本一致，不需要再次拆分 Deployment、引入通用任务队列，或合并领域表。当前仍有一个需要优先闭环的设计问题，以及三个代码和文档整洁性问题。

## P0：发布与 Catalog 契约

### 1. Catalog Release 同时存在“初始化”与“持续追加”两套契约

[`docs/architecture/system-architecture.md`](docs/architecture/system-architecture.md) 将 CatalogRelease 定义为空平台的初始化流程，但当前详细文档和实现允许切换到后续 append-only Release：

- [`docs/architecture/catalog-release.md`](docs/architecture/catalog-release.md) 描述“后续 release 只能新增 source_ref”。
- [`internal/application/catalog/installer.go`](internal/application/catalog/installer.go) 会基于现有 Roadmap 校验并安装追加内容。
- [`internal/application/catalog/availability.go`](internal/application/catalog/availability.go) 始终以当前配置的 bundle digest 作为可用性门禁。

这会产生不干净的行为：平台已有可用 Catalog 时，如果配置切换到一个正在安装或最终失败的新 digest，旧 Roadmap 和题目仍然完整，但 Catalog 与相关作者操作会返回 503。

按当前已确定的简单定位，Catalog Release 应只负责一次性 baseline bootstrap：

- 空平台可以安装一个 immutable Catalog Release，也可以显式以空 Catalog 启动。
- 一旦建立 baseline，不能通过修改 `catalog.release_reference` 继续追加或替换题库。
- 同一 digest 只负责幂等恢复。
- 已有 baseline 时配置不同 digest 应明确拒绝启动或报告配置冲突，不能让旧 Catalog 进入 503。
- 后续题目与修订只走 Authoring、Generation、Classification 和 Roadmap 流程。
- 删除持续追加所需的实现与文档，不引入 `desired release / active release` 双版本机制。

## P1：代码与文档整洁性

### 2. Server 后台生命周期被隐藏在 HTTP transport 中

[`internal/transport/httpapi/server.go`](internal/transport/httpapi/server.go) 的 `SetupRouter` 在创建 Gin Router 时同时执行启动恢复，并启动以下后台循环：

- learning cleanup
- Environment status projection
- Assistant environment lease maintenance
- Generation publication finalizer
- Roadmap maintenance

但 [`internal/bootstrap/server/server.go`](internal/bootstrap/server/server.go) 关闭时只显式等待 Generation AgentRunner、Generator workspace cleanup 和 Catalog installer。其余循环只共享父 Context，没有独立的完成等待。

这与 [`docs/architecture/code-layout.md`](docs/architecture/code-layout.md) 中“transport 只负责认证、输入输出和流传输，bootstrap 负责进程生命周期”的规则不一致，也让 Router 测试、启动失败和关闭顺序承担了隐藏副作用。

建议把这些循环的启动、取消和等待收回 Server bootstrap/application lifecycle。HTTP Handler 只保留路由方法及其依赖，不新增进程或 Deployment。

### 3. TODO 与 REVIEW 缺少完成态收口规则

已完成的“E2E 分层与可丢弃验收基线”曾长期以 P0 和提交计划的形式保留在 [`TODO.md`](TODO.md)，旧 REVIEW 也混有已经实现的 revision、恢复、finalizer 和并发问题。这说明工作文档没有在实现提交完成时同步收口，会直接误导下一阶段判断。

当前 P0 规划已经替换旧 E2E 内容，但该问题只有在后续每个实现提交同步删除对应 REVIEW 条目、最终再从 TODO 删除完成的 P0 后才闭环。稳定契约只进入 [`docs/`](docs/README.md)，TODO 与 REVIEW 不保存已完成方案。

### 4. Checkpoint JSON 协议存在两套解析实现

相同的 `{"checks":[...]}` 协议分别由以下代码解析：

- [`internal/domain/environment/checkpoints.go`](internal/domain/environment/checkpoints.go)
- [`internal/content/challenge/checkpoints.go`](internal/content/challenge/checkpoints.go)

两者当前行为基本一致，但字段、空值、重复 ID 或完整性校验以后容易发生漂移。应提取一个不依赖 Kubernetes、数据库或 HTTP 的共享 checkpoint report 协议，由 challenge 验证和 Environment status 解析共同使用。

## 当前不需要处理

- Server 单副本与 RWO data PVC 在当前阶段可以接受。
- Generation 和 Roadmap Agent 当前不设置固定并发上限是已确认的设计；等真实排队或模型压力出现后再处理。
- Controller 周期执行 checkpoint 在当前规模下足够，`checkpointd` 仍按指标触发。
- PostgreSQL 领域表数量、Runtime Worker 单 Deployment、Environment CRD 和外部 Registry/Incus/OpenSandbox 都不是当前需要重构的问题。
- Runtime Worker 的宽 egress、Controller RBAC 和多副本 Server 属于生产或规模触发项，不阻塞当前题库建设。

## 建议顺序

1. 将 Catalog Release 明确并实现为一次性 bootstrap。
2. 清理已完成的 TODO 和对应错误文档描述。
3. 收回 Server 后台生命周期。
4. 合并 checkpoint report 协议。
