# 工作流

当前有三种持久化执行边界：作者题目的 `GenerationWorkflow`、Server 启动时恢复的
`CatalogRelease`，以及 Server 内的 `RoadmapWorkflow`。它们都由 PostgreSQL 保存状态、lease 和阶段结果；不是按字符串
`kind` 分发的通用任务。

## Generation Workflow

作者先在 `AuthoringSession` 中和 Agent 讨论题意、运行时、检查点与教学内容。作者确认 Plan revision
后，每次 generate 请求创建一条独立的 `GenerationWorkflow`；它是这一项生成任务从 candidate 生成到正式发布的唯一持久身份。
`generation_workflows` 同时是这些独立任务的数据库 worklist，不存在一个集中处理所有作者任务的统一 workflow 或额外 worklist 表。

Server 的 Agent Runtime 从数据库逐条领取当前可执行的 workflow。领取成功后立即异步推进该 workflow 的当前阶段，随后继续领取其他 workflow；因此不同作者的 workflow 可以同时运行。一个阶段完成并持久化状态转移后，当前执行结束，后续阶段由下一次领取自然推进。数据库 lease 和状态版本只负责防止重复领取和拒绝过期结果，不构成固定并发上限。

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

`Published` 的结果不是覆盖旧题目。首次发布会创建稳定 `Challenge` identity 和 revision；对已有 author-owned
Challenge，`ChallengePublishing` 创建新的 immutable revision，要求其 `BaseActiveRevisionID` 仍等于作者开始修订时的
active pointer。Server 在同一事务中保存新 revision、将旧 revision 标为 `superseded`、切换 active pointer、更新
Roadmap binding，并把 workflow 标为 `Published`。如果 fence 已变化，workflow 进入 `Failed`，旧 active revision 不受影响。

弃用不是 Generation state。作者通过独立生命周期 API 请求后，Server 在锁定 Challenge 和当前 Roadmap 的事务中删除
该题 binding/关系边并将 Challenge 标为 `deprecated`；历史 revision、artifact、Environment 和学习记录不删除。正常
Catalog 和新 Environment 只看 active Roadmap，因此 deprecated Challenge 不会重新进入学习入口。

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

学习 Environment 和学习记录保存创建时的 `challenge_revision_id`。任何历史读取（包括 Progress、Assistant、Terminal
上下文和 My Space 学习记录）都按该 revision 读取；它们不能因为 active pointer 改变而显示新题目。启动新 Environment
只解析当前 Catalog 的 active revision，不能复用旧 revision 的 Environment。

每个 Runtime Worker 进程独立运行 Action Loop 与 Reaper Loop：前者只领取 Build、artifact publish、Verify
和 challenge publish，后者只领取已有的幂等资源清理。两条 loop 各自一次只处理一个 lease-fenced action，进程
退出时随同一取消信号停止。Server 对 action 和 cleanup 分别在 Generation 与 Catalog 之间轮换首选 claim；首选侧
没有到期工作时立即尝试另一侧，避免任一侧的持续积压饿死另一侧。

## Catalog Release

Catalog Release 不是 `GenerationWorkflow` 的 source variant，也不会创建 Generator AgentRun、Authoring Session
或 Agent 调用。Server 根据 `catalog.release_reference` 选择一个 immutable OCI digest，并使用独立的 durable
Release/Entry/Commit 状态恢复安装：

```text
Pending -> Installing -> Committing -> Ready | Failed
```

Server stage immutable source 后，Runtime Worker 独立执行每个 Entry 的 Build、artifact publish 与真实 Verify；全部
Entry 成功后 Server 写入 release commit intent，Runtime Worker 执行最终 artifact promotion，Server 再 materialize
题目并原子公开一个 `RoadmapRevision`。同一 digest 的重启或 lease 接管只恢复未完成阶段，不能重复分配 challenge
identity 或重复已成功的 promotion。source staging 与每个 Entry/Commit Runtime state 都有各自最多五次的基础设施
attempt，lease 过期消耗同一 state 的预算。

Catalog 成功提交时同时建立已处理 Roadmap baseline，因此不会触发关系维护工作。RoadmapRevision 是公开
Catalog 的唯一课程读模型；portable release 只作为 immutable import source。配置了 immutable release 时，未 `Ready`
的 release 会在应用层阻塞 Catalog 读取以及作者生成、分类和发布，但不会使 `/readyz` 失败。完整契约见
[Catalog Release](catalog-release.md)。

## Roadmap Maintenance

Roadmap maintenance 是 Server 内的异步 Planner/双 Reviewer 流程，不创建独立 Deployment 或 Runtime Worker action。
启动和运行期间保持 idle barrier：任何 Generation execution state 或 Catalog commit 都不能与其并行。一个 task 的一个
Planner 规划或一个 Reviewer 审查是一次 semantic role call，并且恰好对应一个 `AgentRun`。该 Run 内的模型传输、工具调用
或 typed-result 校验技术错误最多重试五次，只递增 `AgentRun.attempt`；task 的 role-call counter 只统计 semantic call。
五次 attempt 或 Run deadline 耗尽后，该 Run 和 task 作为当前 workflow 的 `Failed` 结果完成，源 entry 保持未处理，自然进入
下一次由新增题目阈值或手工调试入口触发的 maintenance workflow。Reviewer reject 是成功持久化的语义结果：两份 review 完成后
才开启同一 task 的下一 semantic round，并创建新的 Planner/Reviewer Run。最终只有 Server 合并已接受的 ChangeSet 并发布新的
RoadmapRevision。

Server 启动和 task lease 接管都会先将旧的未完成 Roadmap Run 标记为 `Interrupted`。任务 lease 被释放后，系统依据已持久化的
ChangeSet 和 review 创建替代 Run：缺少 ChangeSet 时只替代 Planner；已有 ChangeSet 时只替代尚未有 review 的角色。替代 Run
不增加 semantic round 或 role-call counter，也不恢复未完成的模型上下文或工具执行。
