# Catalog Release

Catalog Release 是空平台建立初始 Catalog 的一次性 immutable bootstrap 输入。它由 Server 的
`catalog.release_reference` 指定，必须是 OCI digest reference。Server 启动后自行恢复首次安装；没有管理员安装 HTTP API、
用户入口、Git 工作区回写或后续 release 更新入口。

## Portable Source

portable source 是 Git 管理的目录，可在独立内容仓库维护：

```text
release.yaml
challenges/<readable-source>/
```

`release.yaml` 只固定每个场景 source 的内容 revision：

```yaml
apiVersion: breakfix.dev/catalog/v1
kind: CatalogRelease
metadata:
  name: foundation
  version: 2026.08.01
entries:
  - path: challenges/service-recovery
    contentRevision: sha256:...
```

`challenge.yaml` 是 portable candidate：不能携带平台生成的 challenge ID、目录 slug、runtime artifact、发布时间或已发布
content revision。它必须声明 `type: documentation-example|operations-scenario` 和 runtime。运维场景携带规范化的简单 tags；
文档示例不使用 tags。运行时 opaque challenge ID 只在最终 commit 时分配。

编辑 portable source 后，先重新计算声明的内容 revision，再打包：

```bash
go run ./cmd/catalog-release \
  -source /path/to/foundation-catalog \
  -print-content-revisions
```

该命令校验目录布局和每个场景，输出 `entries[].contentRevision` JSON。计算不信任 manifest 中现有 digest，因此可修复过期声明；
普通打包路径会严格拒绝不匹配值。它不写 source、不安装 Catalog，也不联系 Registry。

## Challenge 生命周期

平台为每个可运行内容分配稳定 `Challenge.id`，为每次完整 `Generate -> Build -> ArtifactPublish -> Verify` 结果分配不可变
`ChallengeRevision.id`。Challenge 只保存当前 `active_revision_id` 指针；切换指针不会改写旧目录、artifact 或学习记录。作者修订
沿用稳定 Challenge ID 和 `source_slug`，但必须经过新 revision 的完整验证后才原子切换指针。

作者可以弃用自己的 Challenge。弃用仅将 stable identity 标为 `deprecated`，阻止它进入公开 Catalog 和创建新学习 Environment；历史
revision、materialized source、artifact、Environment 和学习记录保留。Catalog Release 创建的 Challenge 是 immutable 基线，不能通过
作者入口修订或弃用。

Environment、checkpoint evidence 和学习 attempt 保存创建时的 `challenge_revision_id`。任何历史读取按
`Challenge.id + challenge_revision_id` 解析 durable revision，不能跟随 active pointer；当前 Catalog 只读取状态为 active 的 Challenge
及其 active revision。

Catalog bundle 是 OCI Image Spec artifact：一个 immutable manifest、空 config 与 portable source layer。它是 OCI Registry 中的内容
对象，不是可运行镜像。

## 安装与可见性

```text
CatalogRelease: Pending -> Installing -> Committing -> Ready | Failed
CatalogEntry:   Building -> ArtifactPublishing -> Verifying -> ReadyToCommit | Failed
Commit:         Pending -> Prepared -> ArtifactPublished -> Materialized -> Committed
```

Server 为每个 bundle digest 创建确定性 Release 和 Entry identity，并将展开后的 source 持久化到 Server data directory。Runtime Worker
执行每个 Entry 的真实 Build、artifact publish 与 Verify；它们不创建 `GenerationWorkflow`、Authoring Session 或 AgentRun。已完成阶段和
外部资源 identity 都持久化，重启或 lease 接管只恢复未完成阶段。

只有全部 Entry 到达 `ReadyToCommit` 后，Release 才进入 `Committing`。Server 持久化每个 commit intent，Runtime Worker 发布最终 runtime
artifact，Server 以 hash 校验和幂等 materialization 写入 source。最后在一个数据库事务中创建 stable Challenge、active immutable
revision、标记所有 commit 为 `Committed` 并将 Release 置为 `Ready`。Catalog 不读取 staging 或部分 materialized 目录，因此不会暴露半次
安装。

## Finalizer 与完整性

Catalog finalizer 的失败诊断持久化在 `catalog_releases`：`finalizer_error_category`、`finalizer_last_error`、
`finalizer_last_attempted_at` 和 `finalizer_next_retry_at`。确定性内容或完整性错误将 Release 置为 `Failed`；瞬时数据库、PVC 或文件系统
错误保留当前状态并在持久化的下一次时间恢复。重启遵守这个时间，不使用内存队列。

每个 immutable Challenge revision 的目录为：

```text
data_dir/challenges/<source_slug>/<challenge_revision_id>/
```

Catalog 读取会对每个 active revision 严格核对 stable ID、revision ID、title、runtime、type、tags、content revision、materialized
revision、artifact 和发布时间。引用目录缺失、路径或内容变化、可执行位变化、元数据不一致都会报告 materialized integrity error，而不会
静默过滤。未被当前 active pointer 引用的目录是历史或尚未公开内容，不能改变 Catalog 结果。

Server 启动和 `/readyz` 使用同一完整性检查；没有发布 revision 时，缺少场景根目录是合法 bootstrap 状态。配置了
`catalog.release_reference` 且首次 Release 尚未 `Ready` 时，应用层拒绝 Catalog 读取和作者生成/发布；`/readyz` 不依赖这个状态，
Runtime Worker 仍可完成安装或资源回收。

## 配置与测试

部署者在空平台打包并推送 source，获得 immutable digest，再写入 Server runtime Secret 的 `catalog_release_reference`。更新 Secret 后重启
或 rollout Server，installer 建立或恢复首次 baseline：

```bash
make catalog-package \
  CATALOG_SOURCE=/path/to/foundation-catalog \
  CATALOG_ARCHIVE=dist/foundation.oci.tar \
  CATALOG_REFERENCE=registry.example.com/breakfix/catalog/foundation:2026.08.01
```

平台验收也使用这条启动路径。`make e2e-prepare` 仅在专用、可丢弃的 Kind target 打包并发布
`test/fixtures/catalog-release/`，将 immutable digest 配置给 Server，并等待固定 Catalog 场景公开。Playwright global setup 只检查
准备好的 Server 连接；测试不会安装 release、复制文件或调用内部管理 API。完整的 target 生命周期见[测试与真实验收](../operations/testing.md)。
