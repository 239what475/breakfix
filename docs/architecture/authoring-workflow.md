# 作者生成与真实验证

作者工作流把题意讨论、模型生成、真实验证和公开发布分开。作者不编辑题目源码，也不能上传 artifact；他们通过对话形成题意，确认后等待系统完成真实验证，只审核已经验证过的 revision。

## 生命周期

作者会话的状态由 [`internal/authoring/model.go`](../../internal/authoring/model.go) 定义：

```text
DraftConversation
  -> IntentReview
  -> GeneratingAndVerifying
  -> AwaitingVerifiedReview
  -> Publishing
  -> Published

AwaitingVerifiedReview + author feedback
  -> RevisingAndVerifying
  -> AwaitingVerifiedReview
```

`CandidateRevision` 是 Judge 已通过后保存的一份不可变候选归档。它有自己的业务状态：

```text
Building -> PublishingArtifact -> Verifying -> Verified -> PublishingChallenge -> Published
    |              |                  |
    +--------------+------------------+-> ArtifactFailed
    +--------------+------------------+-> InfrastructureFailed
```

作者始终只看到已确认的题意或上一份 `Verified` revision。未验证的源文件、失败候选和内部修复意见不会进入作者审核界面。

## 题意讨论与 Generator

作者先与 authoring agent 多轮讨论。agent 只通过受 Server 校验的领域操作修改题意约定，形成元数据、概览和自然语言检查点；它不生成源码，也不启动验证。题意必须包含合法 runtime、难度、简介、概览和至少一个检查点，作者才可以点击“生成并验证题目”。

确认后，Server 创建持久的 Generator AgentRun。Agent Worker 在 Server 管理、围栏的 OpenSandbox workspace 中生成候选，并调用 Judge。Generator 和 Judge 都使用严格 typed result；缺失、未知或非法字段不能用近似文本或默认值放行。Judge 通过且确定性 archive 校验成功时，Server 原子保存 CandidateRevision 与其执行快照，并创建 `build` WorkItem。

## 固定 Worker 流水线

真实流水线由 Server/PostgreSQL 中的通用 WorkItem 调度：

```text
CandidateRevision
  -> Build
  -> ArtifactPublish
  -> Verify
  -> 作者审核
  -> ChallengePublish
```

- **Builder Worker** 只从受信任基础产物添加 challenge bundle。K8s 产出 OCI archive；Node 从固定 `node-systemd-base` 创建停止状态的 Incus image。Builder 不执行候选脚本、不联网安装软件，也没有 Registry 写权限。
- **Publisher Worker** 将 build 输出放进真实环境可访问的 candidate staging store，并向 Server 返回不可变 OCI digest 或 Incus fingerprint。`artifact_publish` 不等于公开发布。
- **Verifier Worker** 从不可变 CandidateRevision snapshot 创建 `purpose=verification` Environment，等待 runtime-init，执行全部 `answer.sh`，再执行所有 `checks.sh`。它向 Server 提交结构化报告并删除 Environment。
- **ChallengePublish** 只在作者确认后发生。Publisher 建立正式镜像引用，Server 从已验证归档原子提升 `data_dir/challenges/<source_slug>/`，写入平台托管的 `id`、`source_slug`、`image`、`published_at`，随后创建 taxonomy mapping。

所有 Worker 经 Server 内部 API 领取、续租和提交结果；Server 是 PostgreSQL 的唯一写者。每个操作都带 `work_item_id`、attempt 和 lease owner，失去租约的旧 Worker 不能提交 artifact、报告或推进下一阶段。

## 失败与恢复

失败只有三类：

- **artifact**：候选静态校验、固定封装、runtime-init、answer 或 checkpoint 失败。当前 CandidateRevision 进入 `ArtifactFailed`，Server 用同一 Generator session 创建下一修复 Run；修复产物一定是新的 CandidateRevision。
- **infrastructure**：Server、Registry、Kubernetes、Incus、vcluster、Provider 或 Worker 故障。当前 WorkItem 在其一小时 deadline 内退避重试，不要求 Generator 修改题目；Server 在启动和固定周期中回收过期阶段，因此不依赖下一次 Worker 领取；deadline 用尽才进入 `InfrastructureFailed`。
- **cancelled**：作者取消、会话替代或输入 revision 失效。清理外部资源，但不创建修复 Run。

`artifact_cleanup` 是唯一无业务 deadline 的 WorkItem。它只回收 candidate 专属 staging 引用和临时 build 资源；正式 image 引用不会被 cleanup 删除。Server 在一个 PostgreSQL 事务内完成当前 WorkItem、保存领域结果和创建下一阶段，避免“阶段成功但下一步丢失”。

Generator AgentRun 的技术失败同样由 Server 投影到当前 AuthoringSession：会话进入 `InfrastructureFailed`、失效的 `generator_run_id` 被清除，作者可以继续通过自然语言修改方案后发起新的 revision；旧 Run 的迟到结果仍会被 lease 围栏拒绝。

## 审核与发布

只有 `Verified` revision 会进入 `AwaitingVerifiedReview`。作者可以查看只读资产、实际 metadata、检查点、diff 和验证报告；自然语言反馈会启动新的生成和验证，不会改写旧 revision。

作者确认发布后，Server 先持久化发布意图，Publisher 幂等建立由 opaque challenge ID 派生的正式 OCI/Incus 引用，Server 再从已验证归档临时生成目录并 atomic rename。文件系统与 PostgreSQL 不是伪装的单一事务：Server 启动恢复器只扫描 `PublishingChallenge` 的明确意图，目录和记录精确一致才完成数据库提交；不一致是 invariant breach。

发布题目仍需通过 taxonomy mapping 才进入公开 Catalog。taxonomy 不参与生成、构建或验证，详见[Taxonomy 与 Catalog 发布](taxonomy.md)。
