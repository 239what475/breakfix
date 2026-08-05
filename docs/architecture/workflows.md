# 工作流

当前有两种持久化执行边界：作者题目的 `GenerationWorkflow`，以及 Server
启动时恢复的 `CatalogRelease`。两者都由 PostgreSQL 保存状态、lease 和阶段结果；它们不是按字符串
`kind` 分发的通用任务。

## Generation Workflow

作者先在 `AuthoringSession` 中和 Agent 讨论题意、运行时、检查点与教学内容。作者确认 Plan revision
后，Server 创建一个 `GenerationWorkflow`；它是从 candidate 生成到正式发布的唯一持久身份。

```text
Server Agent Runtime                         Runtime Worker
--------------------                         --------------
Generating -> Judging -> Building -> ArtifactPublishing -> Verifying
                                                        |
                                                        v
NeedsAuthorReview <- verified candidate       candidate repair -> Generating
  |
  | author confirms content
  v
Classifying -> NeedsClassificationReview
                    |
                    | author confirms classification
                    v
             ChallengePublishing -> promotion result
                                      |
                                      v
                         Server finalizer: materialize + RoadmapRevision -> Published
```

实际状态枚举以 `internal/domain/generation/` 为准；公开接口字段以
`api/http/openapi.yaml` 为准。

| State | 执行者 | 持久化结果 |
| --- | --- | --- |
| `Generating` | Server 的 Generator Agent | immutable candidate archive。 |
| `Judging` | Server 的 Judge Agent | 审核结果或返回 Generator 的反馈。 |
| `Building` | Runtime Worker | Node 的 Incus build image reference，或 K8s 的 build-scoped immutable OCI reference。 |
| `ArtifactPublishing` | Runtime Worker | 真实验证可访问的 staging artifact。 |
| `Verifying` | Runtime Worker | `purpose=verification` Environment identity 与结构化验证报告。 |
| `NeedsAuthorReview` | 作者 | 已验证 candidate 的内容确认或修复反馈。 |
| `Classifying` | Server 的 Classifier Agent | 私有 ClassificationProposal。 |
| `NeedsClassificationReview` | 作者 | Topic/Tag proposal 的确认或调整。 |
| `ChallengePublishing` | Runtime Worker | 最终 immutable artifact 的 durable promotion result。 |
| `Published` | Server finalizer | 幂等 materialize 的 challenge source 与新的 RoadmapRevision。 |

Generator、Judge 和 Classifier 的已知技术错误属于各自 `AgentRun`，一个逻辑 Run 最多五次；它们不会产生
新的 CandidateRevision。Runtime state 的基础设施错误则由 Server 管理当前 state 的 `runtime_attempt`：进入
state 时为一，最多五次。接管同一 state 时 external identity 保持
`workflow_id + candidate_revision_id + state + state_version`，不包含 `runtime_attempt`。确定性 candidate
错误从 Building、ArtifactPublishing 或 Verifying 返回 `Generating`；第五次 Runtime 基础设施失败进入
`Failed`。

验证 Environment 在 Worker 成功记录其 identity 后不由该 action 同步删除。验证报告持久化后，Runtime
Worker 的异步 reaper 只删除该 Environment；删除失败只重试删除，绝不重新验证。ChallengePublishing
也先持久化 promotion result，Server 重启时只重试 materialization/finalization，绝不重复已经成功的
Worker promotion。

## Catalog Release

Catalog Release 不是 `GenerationWorkflow` 的 source variant，也不会创建 Generator Run、Authoring Session
或 Agent 调用。Server 根据 `catalog.release_reference` 选择一个 immutable OCI digest，并使用独立的 durable
Release/Entry/Commit 状态恢复安装：

```text
Pending -> Installing -> Committing -> Ready | Failed
```

每个 Entry 独立执行 Build、artifact publish 与真实 Verify。全部 Entry 成功后，Server 才在 release commit
中 materialize 题目并原子公开一个 `RoadmapRevision`。同一 digest 的重启或 lease 接管只恢复未完成阶段，不能
重复分配 challenge identity。

Catalog 成功提交时同时建立已处理 Roadmap baseline，因此不会触发关系维护工作。RoadmapRevision 是公开
Catalog 的唯一课程读模型；portable release 只作为 immutable import source。完整契约见
[Catalog Release](catalog-release.md)。
