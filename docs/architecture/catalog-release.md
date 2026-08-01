# Catalog Release

Catalog Release 是空平台的管理员初始化机制。它安装一个经 OCI digest 固定的、Git 管理的 portable source；它不是持续
同步器、用户可见的提交入口，也不是 Kubernetes CRD。

## Portable Source

```text
catalog/
  release.yaml
  challenges/<topic>/<source>/
  taxonomy/
```

`release.yaml` 只保存 source 路径和确定性的 `contentRevision`：

```yaml
apiVersion: breakfix.dev/catalog/v1
kind: CatalogRelease
metadata:
  name: foundation
  version: 2026.08.01
entries:
  - path: challenges/linux/cleanup-logs
    contentRevision: sha256:...
taxonomy:
  contentRevision: sha256:...
```

portable source 中的 `challenge.yaml` 是 candidate 语义，禁止包含平台生成的 challenge ID、目录 slug、运行时 image、
发布时间和已发布 content revision。`contentRevision` 覆盖相对路径、文件字节和可执行位；它与 Node Incus fingerprint
或 K8s OCI digest 是不同概念。后两者只在目标平台真实构建后作为 runtime artifact 保存。

Catalog Release bundle 使用 OCI Image Spec 1.1 的 artifact 表达：根对象是 OCI image manifest，包含
`artifactType`、`application/vnd.oci.empty.v1+json` config 和 portable source layer。这样可被标准 OCI Registry
存储，而不会伪装成可运行容器镜像。

## 安装流程

```text
Pending -> Installing -> Committing -> Ready
                  |          |
                  +----> CleaningUp -> Failed
```

管理员通过 Server API 提交不可变 OCI digest。Server 下载、展开并校验完整 source，再在一个事务中创建 Release、每个 Entry、
CandidateRevision 和对应的 `GenerationWorkflow`。Release entry 的 source 是 `release/<entry-id>`；它从 `Building` 开始，
只执行真实 build、artifact staging、verify 与 cleanup，不运行 Generator、Judge、作者审核或 Taxonomy Agent。

每个 entry 成功后进入 `ReadyToCommit`。只有所有 entry 都成功，Server 才进入 `Committing`：为每题保留一次 opaque
challenge ID 与可读 slug，materialize 到 staging，保存 runtime artifact 和已编译的 taxonomy mapping，最后在一个数据库
事务中将 Release 置为 `Ready`。崩溃恢复复用同一运行时身份并核对目标文件树，不重新分配题目身份。

内容、构建或真实验证失败会停止领取新 entry，转入 `CleaningUp`；已领取 Worker 在当前阶段结束后自行清理。基础设施失败仅在
Release deadline 内重试。两类失败都不会触发 Agent 修复，也不会公开部分题库；`Failed` 保留为不可变历史，再次安装会创建新的
Release attempt。

## 初始化与测试

Server 可以在空 data PVC 上启动，但正常产品初始化必须安装一个 `Ready` Catalog Release。`data_dir/challenges` 和
`data_dir/taxonomy` 只保存成功 materialize 的运行时视图，不能由开发脚本或 Git 目录直接复制。

浏览器 E2E 使用同一入口。`test/fixtures/catalog-release/` 是最小 portable source；global setup 仅在目标平台 Catalog
为空时，经管理员 API 安装其 immutable OCI bundle。测试 fixture 不会直接写入 Server data directory。
