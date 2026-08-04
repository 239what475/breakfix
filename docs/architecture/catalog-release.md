# Catalog Release

Catalog Release 是平台基线的 immutable、追加式输入。它由 Server 的
`catalog.release_reference` 配置指定，必须是 OCI digest reference；Server 启动后自行恢复安装。没有管理员安装 HTTP API、用户入口或 Git 工作区回写。

## Portable Source

portable source 是 Git 管理的目录，可在独立内容仓库维护：

```text
release.yaml
challenges/<domain>/<readable-source>/
roadmap/
  domains/
  topics/
  tags/
  challenge-bindings/
  topic-edges.yaml
  challenge-edges.yaml
```

`release.yaml` 固定每个 challenge source 和整份 Roadmap source 的内容 revision：

```yaml
apiVersion: breakfix.dev/catalog/v1
kind: CatalogRelease
metadata:
  name: foundation
  version: 2026.08.01
entries:
  - path: challenges/linux/service-recovery
    contentRevision: sha256:...
roadmap:
  contentRevision: sha256:...
```

`challenge.yaml` 仍是 portable candidate 语义，不能携带平台生成的 challenge ID、目录 slug、runtime artifact、发布时间或已发布 content revision。`source_ref` 与 title 位于 Roadmap source：Domain 全局唯一，Topic 使用 `domain/topic`，Tag 全局唯一，Challenge 使用 `domain/topic/challenge`。运行时 opaque challenge ID 只在最终 commit 时分配。

Catalog bundle 使用 OCI Image Spec artifact：一个 immutable manifest、空 config 与 portable source layer。它是 OCI Registry 中的内容对象，不是可运行镜像。

## 安装与可见性

```text
CatalogRelease: Pending -> Installing -> Committing -> Ready | Failed
CatalogEntry:   Pending -> Building -> ArtifactPublishing -> Verifying -> ReadyToCommit | Failed
Commit:         Prepared -> ArtifactPublished -> Materialized -> Committed
```

Server 为每个 bundle digest 创建确定性 Release 和 Entry identity，并把展开后的 source 持久化到 Server data directory。Entry 独立执行真实 Build、artifact publish 与 Verify；它们不创建 `GenerationWorkflow`、Generator Run、Authoring Session 或 Roadmap task。已完成阶段和外部资源身份都被持久化，重启或 lease 接管只恢复尚未完成的阶段。

只有全部 Entry 到达 `ReadyToCommit` 后，Release 才进入 `Committing`。Server 先持久化每题的 commit intent、发布最终 runtime artifact 并 materialize challenge source；最后在同一数据库事务中公开新的 immutable `RoadmapRevision`、标记所有 commit 为 `Committed`、将 Release 置为 `Ready`，并建立 Roadmap 已处理基线。Catalog 读取只依赖当前 RoadmapRevision，因此 materialization 早于最终事务也不会暴露部分题库。

source、Build 或 Verify 的确定性错误会使整份 Release 进入 `Failed`，不会发布部分内容，也不会启动 Agent 修复。Registry、Kubernetes、Incus 或本地存储的基础设施错误在 release deadline 内仅重试当前阶段。相同 digest 可幂等恢复；后续 release 只能添加新的 `source_ref`，不能原地修改或删除已安装内容。

## 配置与测试

部署者先打包并推送 source，获得 immutable digest，然后将它写入 Server runtime Secret 的 `catalog_release_reference`。更新 Secret 后重启或 rollout Server，安装器会从该配置恢复。

```bash
make catalog-package \
  CATALOG_SOURCE=/path/to/foundation-catalog \
  CATALOG_ARCHIVE=dist/foundation.oci.tar \
  CATALOG_REFERENCE=registry.example.com/breakfix/catalog/foundation:2026.08.01
# 输出 registry.example.com/breakfix/catalog/foundation@sha256:...
```

浏览器 E2E 也使用这条启动路径。将 `test/fixtures/catalog-release/` 打包、推送并作为测试 Server 的 `catalog_release_reference` 配置后，global setup 只轮询公开 Catalog，等待 fixture 出现；测试不会安装 release、复制文件或调用内部管理 API。
