# 工作流

Breakfix 只有两类可领取的后台工作流：`GenerationWorkflow` 与 `TaxonomyWorkflow`。它们都是 PostgreSQL 中的
持久状态机，不是通用 work-item，也不通过 `kind` 再分发为隐式子任务。Server 是状态、lease 和阶段结果的唯一写者；
Worker 只通过受认证的内部 API 领取、续租、读取上下文和报告 typed 结果。

## Generation Workflow

作者先在 `AuthoringSession` 中和 Agent 讨论题意、运行时、检查点与教学内容。作者确认后，Server 创建一个
`GenerationWorkflow`；它是从 candidate 生成到正式发布的唯一持久身份。

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

### 阶段职责

1. **Generating**：Generate Worker 使用同一个 Generator session 在 Server 管理的 workspace 中生成候选 archive。
2. **Judging**：Judge 审核候选、题意、metadata 和检查点是否自洽。
3. **Building**：Node 候选形成 Incus image；K8s 候选形成 OCI archive。
4. **ArtifactPublishing**：将不可变 candidate artifact 放入真实验证可访问的位置。
5. **Verifying**：创建 `purpose=verification` Environment，等待运行时初始化，运行答案与全部检查点。
6. **NeedsAuthorReview**：验证通过后暂停 deadline 并释放 Worker lease；作者只能确认发布或用自然语言提出修改。
7. **ChallengePublishing**：Generate Worker 发布正式 runtime artifact，Server materialize challenge 目录。
8. **CleaningUp**：回收 candidate 专属构建产物和临时资源，再按不可变 cleanup intent 进入终态。

### 失败与恢复

候选内容、脚本、答案或 checkpoint 的确定性失败属于 artifact failure。Server 保存可用于修复的
结构化反馈，将 Workflow 返回 `Generating`，并使用同一个 Generator session 产生新的
CandidateRevision。作者不会先看到一个未通过真实验证的候选。

Server、Registry、Kubernetes、Incus、OpenSandbox 或网络故障属于 infrastructure failure。Server 保持
当前 state、递增 `state_attempt`、释放 lease 并在总 deadline 内退避重试；deadline 用尽时转入
`CleaningUp`，清理完成后才是 `Failed`。

作者审核期间不消耗 deadline。恢复生成或开始正式发布时，Server 将审核暂停时间加回 deadline。明确
取消同样先进入 `CleaningUp`，只有清理完成后才进入 `Cancelled`。

### 发布与 Catalog

正式发布由 Server 以候选 archive 与正式 artifact 为输入完成文件系统 materialization。随后 taxonomy
maintenance 为该 challenge revision 创建或补回 `TaxonomyWorkflow`。题目只有被 taxonomy snapshot 映射后
才出现在公开 Catalog。

`source.kind=release` 复用同一 Workflow 记录和 Worker 阶段协议，但 Catalog entry 已经是审查过的 source：它从
`Building` 开始，跳过 Generator、Judge、作者审核和逐题最终发布。所有 entry 通过真实验证后，由 Release 级原子提交
统一公开，详见 [Catalog Release](catalog-release.md)。

## Taxonomy Workflow

每个已发布 `(challenge ID, content revision)` 最多有一个 `TaxonomyWorkflow`：

```text
Queued -> Mapping -> Reviewing -> Publishing -> Completed
                    | reject
                    v
                 Mapping (next semantic round)
```

Mapper 提出 candidate mapping，Curriculum 与 SRE reviewer 作为同一语义 round 并行审查；拒绝后 Mapper 使用两份意见
开始下一 round。模型调用、传输和 typed result 校验错误是当前 state 的技术重试，不增加 semantic round；连续十次失败后
Worker 释放 lease，保留状态与候选，下一副本在退避后接管。

多个 taxonomy Workflow 可以并行分析和审查；snapshot 构造、校验与 `current` 指针替换由 Server 短暂串行化。完整 mapping、
snapshot 和 Catalog 准入规则见 [Taxonomy](taxonomy.md)。
