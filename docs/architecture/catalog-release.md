# Catalog Release

Catalog Release 是空平台建立题库基线时的一次性 immutable bootstrap 输入。它由 Server 的
`catalog.release_reference` 配置指定，必须是 OCI digest reference；Server 启动后自行恢复首次安装。没有管理员安装 HTTP API、用户入口、Git 工作区回写或后续 release 更新入口。

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

`challenge.yaml` 仍是 portable candidate 语义，不能携带平台生成的 challenge ID、目录 slug、runtime artifact、发布时间或已发布 content revision。`source_ref` 与 title 位于 Roadmap source：Domain 全局唯一，Topic 使用 `domain/topic`，Tag 全局唯一，Challenge 使用 `domain/topic/challenge`。Release 作者显式维护 ASCII `source_ref`；Authoring 创建的实体从显示标题导出稳定 ASCII 引用，非 ASCII 标题使用确定性摘要段。运行时 opaque challenge ID 只在最终 commit 时分配。

## Challenge 生命周期

平台为每道题分配一个稳定的 `Challenge.id`，并为每次经过完整
`Generate -> Build -> ArtifactPublish -> Verify` 链路的发布结果分配一个不可变的
`ChallengeRevision.id`。Challenge 只保存当前 `active_revision_id` 指针；切换指针不会改写旧目录、artifact
或学习记录。作者修订沿用稳定 Challenge ID 和 `source_slug`，但必须以新的 revision 重新验证后才原子切换指针。

作者可以弃用自己的 Challenge。弃用会从当前 Roadmap 删除 binding 和关系边，将 Challenge 标记为
`deprecated`，但保留所有 revision、materialized source、artifact、Environment 和学习记录；弃用题目不再出现在
公开 Catalog，也不能创建新的学习 Environment。Catalog Release 创建的 Challenge 是 immutable 基线，不能通过作者入口修订或弃用。

Environment、checkpoint evidence 和学习 attempt 都保存创建时的 `challenge_revision_id`。读取这些历史记录时必须按
`Challenge.id + challenge_revision_id` 解析 durable revision，不能跟随当前 active pointer；因此旧 Environment 在作者发布新
revision 或弃用 Challenge 后仍然使用原来的题目内容。当前 Catalog 读取则只使用当前 Roadmap 精确绑定的 active revision。

Catalog bundle 使用 OCI Image Spec artifact：一个 immutable manifest、空 config 与 portable source layer。它是 OCI Registry 中的内容对象，不是可运行镜像。

## 安装与可见性

```text
CatalogRelease: Pending -> Installing -> Committing -> Ready | Failed
CatalogEntry:   Building -> ArtifactPublishing -> Verifying -> ReadyToCommit | Failed
Commit:         Prepared -> ArtifactPublished -> Materialized -> Committed
```

Server 为每个 bundle digest 创建确定性 Release 和 Entry identity，并把展开后的 source 持久化到 Server data directory。Server 只负责 source staging、commit intent、source materialization 和最终原子公开；Runtime Worker 独立执行 Entry 的真实 Build、artifact publish、Verify 和 Commit 的最终 artifact promotion。它们不创建 `GenerationWorkflow`、Generator AgentRun、Authoring Session 或 Roadmap task。已完成阶段和外部资源身份都被持久化，重启或 lease 接管只恢复尚未完成的阶段。

启动边界只允许以下情况：空平台安装首次 digest；首次安装中断后以同一 digest 恢复；`Ready` baseline 以同一 digest 重启；以及没有配置 release 的空平台。已有 `Ready` baseline 时配置另一个 digest、已有 Authoring 发布内容但没有 baseline 时再配置 release，都会作为配置冲突拒绝启动。失败且没有公开任何 Challenge 的首次安装可以改用新 digest，但 Server 会先删除旧 staged source，并等待 Runtime Worker 完成旧 release 的 provider resource reap，之后才创建新 Release。

只有全部 Entry 到达 `ReadyToCommit` 后，Release 才进入 `Committing`。Server 先持久化每题的 commit intent；Runtime Worker 发布最终 runtime artifact；Server 再以 hash 校验和幂等 materialization 写入 source。最后在同一数据库事务中公开新的 immutable `RoadmapRevision`、标记所有 commit 为 `Committed`、将 Release 置为 `Ready`，并建立 Roadmap 已处理基线。Worker promotion 成功后 Server 崩溃时只恢复 materialization/finalization，绝不重复 promotion。Catalog 读取只依赖当前 RoadmapRevision，因此 materialization 早于最终事务也不会暴露部分题库。

### Finalizer 诊断与恢复

`Installing` 或 `Committing` 期间由 Server 执行的 source 检查、题目物化和最终提交，属于 Catalog finalizer。
每次 finalizer 失败都会写入 `catalog_releases` 的持久诊断：`finalizer_error_category`、清理后的
`finalizer_last_error`、`finalizer_last_attempted_at` 和 `finalizer_next_retry_at`。空类别和空字段表示没有未完成的
诊断；这组字段不是日志的替代显示，而是重启后继续工作的唯一状态依据。

确定性内容或完整性错误（例如 source digest 改变、commit 与 source 不一致、物化结果不符合 intent）将 Release
置为现有 `Failed`，保留错误摘要，不再反复安装同一损坏提交。瞬时数据库、Server data PVC 或文件系统 I/O
错误保留当前 `Installing`/`Committing` 状态，并把 `next_run_at` 与 `finalizer_next_retry_at` 设为持久化的下一次
尝试时间；安装器在该时间之前直接返回，不进行提前轮询。进程重启后仍遵守数据库中的时间，成功恢复后在完成
Roadmap 原子提交的同一事务中清空诊断字段。

物化过程是幂等的：已经存在且与 commit 完全匹配的目录会被复用，冲突目录会被视为确定性失败；未被当前
Roadmap 引用的中间目录不会进入公开 Catalog。Server 启动时和运行期间会从 PostgreSQL 重新派生物化保留集合：
所有已发布 Challenge revision，以及仍处于 `Committing` 的 commit intent 都会保留；终态 `Failed` release
独占的目录和崩溃遗留 staging 会被 Server data PVC reconciler 回收。它不保存额外 worklist，也不把 Server PVC
交给 Runtime Worker。

## 物化完整性

运行时 `RoadmapRevision` 的每个 `ChallengeRef` 除 `content_revision` 外还保存
`source_slug` 和 `materialized_revision`。`content_revision` 是 portable source 的身份；
`materialized_revision` 是 Server data volume 上最终题目目录的身份，按规范化排序后的相对路径、
文件内容和可执行位计算。它不属于 `PortableChallengeRef`，不会进入 Catalog Release。

每个 immutable Challenge revision 的物化目录都是独立路径：`data_dir/challenges/<source_slug>/<challenge_revision_id>/`。
旧 revision 目录不得被覆盖或复用；目录保留是历史 Environment 可恢复读取的前提。

Catalog 读取以当前 Roadmap binding 为准，逐项找到对应的 materialized directory，并严格核对
`id`、`title`、`content_revision`、`source_slug`、目录路径和 `materialized_revision`。目录缺失、
路径改变、文件内容改变、可执行位改变或元数据不一致都会返回明确的 materialized integrity error，
不会把题目静默过滤掉。未被当前 Roadmap 引用的额外目录允许存在，以覆盖物化先于 Roadmap 原子提交的
正常窗口；它们不成为公开题目，也不参与完整性投影。没有任何发布 revision 或非终态 intent 引用的规范目录
最终会由 Server 回收；当前、superseded 和 deprecated revision 始终作为历史事实保留。

Server 启动和 `/readyz` 使用同一完整性检查；`/readyz` 使用短超时并绕过 configured release
availability gate。没有当前 Roadmap 时，缺失的题目根目录是合法的 bootstrap 状态，避免首次 Catalog
安装时 Server 与 Runtime Worker 互相等待。

source、Entry/Commit runtime 的确定性错误会使整份 Release 进入 `Failed`，不会发布部分内容，也不会启动 Agent 修复。source staging 和每个 Runtime state 各自最多五次基础设施 attempt；lease 过期也消耗当前 state 的同一预算。重试不改变 `release_id + entry_or_commit_id + state + state_version` 的外部 identity，`runtime_attempt` 不参与命名。相同 digest 只负责首次安装的幂等恢复；baseline 建立后，所有新增题目、题目 revision、Topic 和 Tag 都通过 Authoring、Generation、Classification 与 Roadmap 流程发布。

配置了 `catalog.release_reference` 且首次 Release 未达到 `Ready` 时，Server 在应用层拒绝题库读取以及作者的生成、分类和发布请求；`/readyz` 不依赖这个状态，Runtime Worker 可以继续通过内部 API 完成安装或失败 release 的资源回收，避免启动死锁。已有 baseline 不会等待另一个配置 digest，因为这种配置会在启动时直接拒绝。

## 调试导出

Server 在显式启用 `debug.enabled` 并配置独立 `breakfix-debug` Secret 后，提供一个不属于 OpenAPI 或浏览器 UI 的只读调试接口：

```text
GET /internal/debug/roadmap-revisions/{revision_id}/export
```

调用必须携带 `X-Breakfix-Debug-Key`。默认配置不注册该路由；用户 JWT、Runtime Worker identity 和其他业务凭据
不能替代 debug credential。Roadmap maintenance 请求使用同一调试边界，接口为：

```text
POST /internal/debug/roadmap-maintenance
```

它按指定 immutable `RoadmapRevision` 流式返回
`breakfix-roadmap-r{revision_id}.tar.gz`。归档根目录是完整的 portable Catalog Release：
`release.yaml`、所有 `roadmap/` 定义、Challenge binding、Topic/Challenge 两张关系图，以及该 revision
中每一道题的 portable source。Server 在开始写响应前读取并核对每个 materialized Challenge 的 title、source slug、
content revision 和 materialized revision；发布 manifest 中的 `id`、`source_slug`、`image`、`content_revision` 与
`published_at` 会被移除。

导出不包含数据库或运行时 ID、OCI/Incus artifact、构建中间产物、验证报告、临时文件或宿主机路径。
文件路径按字典序写入，文件 mode、owner、tar mtime 和 gzip mtime 固定，因此同一 revision 的重复导出
字节稳定。实现只使用 Go 标准库 `archive/tar` 与 `compress/gzip`，不创建临时目录或回写 Catalog 工作区。

## 配置与测试

部署者只在空平台初始化时打包并推送 source，获得 immutable digest，然后将它写入 Server runtime Secret 的 `catalog_release_reference`。更新 Secret 后重启或 rollout Server，安装器会从该配置建立或恢复首次 baseline。

```bash
make catalog-package \
  CATALOG_SOURCE=/path/to/foundation-catalog \
  CATALOG_ARCHIVE=dist/foundation.oci.tar \
  CATALOG_REFERENCE=registry.example.com/breakfix/catalog/foundation:2026.08.01
# 输出 registry.example.com/breakfix/catalog/foundation@sha256:...
```

平台验收也使用这条启动路径。`make e2e-prepare` 在专用、可丢弃的 Kind target 上将
`test/fixtures/catalog-release/` 打包、推送并作为 Server 的 `catalog_release_reference` 配置，然后有界地等待公开 Catalog
出现 fixture。Playwright global setup 只检查准备好的 Server 连接；测试不会安装 release、复制文件或调用内部管理 API。完整的
target 生命周期和运行入口见[测试与真实验收](../operations/testing.md)。
