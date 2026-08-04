# 工作流

当前有两种持久化执行边界：作者题目的 `GenerationWorkflow`，以及 Server 启动时恢复的 `CatalogRelease`。两者都由 PostgreSQL 保存状态、lease 和阶段结果；它们不是按字符串 `kind` 分发的通用任务。

## Generation Workflow

作者先在 `AuthoringSession` 中和 Agent 讨论题意、运行时、检查点与教学内容。作者确认后，Server 创建一个 `GenerationWorkflow`；它是从 candidate 生成到正式发布的唯一持久身份。

```text
AuthoringSession
  -> Queued -> Generating -> Judging -> Building -> ArtifactPublishing -> Verifying
                                                              |
                                                              v
NeedsAuthorReview <- verified candidate                 candidate repair -> Generating
  |                        |
  | author confirms        | author feedback
  v                        v
ChallengePublishing ----> CleaningUp -> Completed
```

实际状态枚举以 `internal/domain/generation/` 为准；公开接口字段以 `api/http/openapi.yaml` 为准。

1. **Generating**：Generate Worker 使用同一个 Generator session 在 Server 管理的 workspace 中生成 candidate archive。
2. **Judging**：Judge 审核 candidate、题意、metadata 和检查点是否自洽。
3. **Building**：Node candidate 形成 Incus image；K8s candidate 形成 OCI archive。
4. **ArtifactPublishing**：将不可变 candidate artifact 放入真实验证可访问的位置。
5. **Verifying**：创建 `purpose=verification` Environment，等待运行时初始化，运行答案与全部检查点。
6. **NeedsAuthorReview**：验证通过后暂停 deadline 并释放 Worker lease；作者可以确认发布或提出修改。
7. **ChallengePublishing**：Generate Worker 发布正式 runtime artifact，Server materialize challenge 目录。
8. **CleaningUp**：当前实现回收 candidate 专属构建产物和临时资源后进入终态。

候选内容、脚本、答案或 checkpoint 的确定性失败会回到生成修复路径。Server、Registry、Kubernetes、Incus、OpenSandbox 或网络故障保持当前 state，在总 deadline 内退避重试。作者审核期间不消耗 deadline。

## Catalog Release

Catalog Release 不是 `GenerationWorkflow` 的 source variant，也不会创建 Generator Run、Authoring Session 或 Agent 调用。Server 根据 `catalog.release_reference` 选择一个 immutable OCI digest，并使用独立的 durable Release/Entry/Commit 状态恢复安装：

```text
Pending -> Installing -> Committing -> Ready | Failed
```

每个 Entry 独立执行 Build、artifact publish 与真实 Verify。全部 Entry 成功后，Server 才在 release commit 中 materialize 题目并原子公开一个 `RoadmapRevision`。同一 digest 的重启或 lease 接管只恢复未完成阶段，不能重复分配 challenge identity。

Catalog 成功提交时同时建立已处理 Roadmap baseline，因此不会触发关系维护工作。RoadmapRevision 是公开 Catalog 的唯一课程读模型；portable release 只作为 immutable import source。完整契约见 [Catalog Release](catalog-release.md)。
