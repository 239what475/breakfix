# 作者生成与真实验证

作者先在 `AuthoringSession` 中和 Agent 讨论题意、运行时、检查点与教学内容。作者确认后，Server
创建一个 `GenerationWorkflow`；该 Workflow 是从候选生成到正式发布的唯一持久身份。

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

实际状态枚举和接口字段以 `internal/generation/` 与 `api/http/openapi.yaml` 为准。

## 阶段职责

1. **Generating**：Generate Worker 使用同一个 Generator session 在 Server 管理的 workspace 中生成候选 archive。
2. **Judging**：Judge 审核候选、题意、metadata 和检查点是否自洽。
3. **Building**：Node 候选形成 Incus image；K8s 候选形成 OCI archive。
4. **ArtifactPublishing**：将不可变 candidate artifact 放入真实验证可访问的位置。
5. **Verifying**：创建 `purpose=verification` Environment，等待运行时初始化，运行答案与全部检查点。
6. **NeedsAuthorReview**：验证通过后暂停 deadline 并释放 Worker lease；作者只能确认发布或用自然语言提出修改。
7. **ChallengePublishing**：Generate Worker 发布正式 runtime artifact，Server materialize challenge 目录。
8. **CleaningUp**：回收 candidate 专属构建产物和临时资源，再按不可变 cleanup intent 进入终态。

## 失败与恢复

候选内容、脚本、答案或 checkpoint 的确定性失败属于 artifact failure。Server 保存可用于修复的
结构化反馈，将 Workflow 返回 `Generating`，并使用同一个 Generator session 产生新的
CandidateRevision。作者不会先看到一个未通过真实验证的候选。

Server、Registry、Kubernetes、Incus、OpenSandbox 或网络故障属于 infrastructure failure。Server 保持
当前 state、递增 `state_attempt`、释放 lease 并在总 deadline 内退避重试；deadline 用尽时转入
`CleaningUp`，清理完成后才是 `Failed`。

作者审核期间不消耗 deadline。恢复生成或开始正式发布时，Server 将审核暂停时间加回 deadline。明确
取消同样先进入 `CleaningUp`，只有清理完成后才进入 `Cancelled`。

## 发布与 Catalog

正式发布由 Server 以候选 archive 与正式 artifact 为输入完成文件系统 materialization。随后 taxonomy
maintenance 为该 challenge revision 创建或补回 `TaxonomyWorkflow`。题目只有被 taxonomy snapshot 映射后
才出现在公开 Catalog。
