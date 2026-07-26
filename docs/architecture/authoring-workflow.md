# 作者生成与真实验证

作者工作流将自然语言题意、agent 生成和真实运行时验证分开。作者不直接编辑题目源码，也不能上传任意 artifact；他们只通过对话形成题意、确认生成并审核已经验证的 revision。

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

在生成或修订验证期间，真实验证失败会留在同一内部循环中：artifact 类失败会创建同一 Generator Session 的下一 Run，并将结构化反馈提供给它修复。基础设施失败不会要求 Agent 修改题目。作者只继续看到上一个已验证 revision，或在首次生成时看到已确认的题意；未通过验证的 artifact 不进入作者审核界面。

## 题意讨论

作者创建会话后与 review agent 多轮讨论。agent 只能通过受 Server 校验的领域操作修改题意约定，形成带元数据、概览和自然语言检查点的 revision。题意约定必须包含有效 runtime、难度、概览和至少一个检查点，才能进入 `IntentReview` 并允许作者点击“生成并验证题目”。

检查点在此阶段是作者可读的学习目标，不是固定的 `expected_user_edits` 模板。生成 agent 负责把它们落为实际题目资产和可执行检查。

## 生成与 judge

确认生成后，Server 创建持久 Generator Agent Run。Agent Worker 以数据库租约领取该 Run，在 Server 管理并围栏的 OpenSandbox 工作区中写入 challenge 文件。Worker 没有 Kubernetes、Registry 或 OpenSandbox 生命周期凭据；它只能经内部 API 对当前租约的工作区读写、执行命令、归档和提交候选。

Generator 先进行确定性候选校验，judge agent 再以严格 typed result 审核题意、元数据、题面、解答和检查点是否自洽。任何缺失、未知或非法字段都会使当前 Run 失败，不能按近似文本放行。生成资产的规范与静态约束在 [`internal/generator/`](../../internal/generator/) 中实现。

## VerifyTask

Generator 通过内部 HTTP 向 Server 提交归档。Server 保存归档并创建 `VerifyTask`；Controller 启动真实验证 Job：

1. 从 Server 下载 artifact 并构建镜像。
2. 创建与该题 runtime 相同的临时 Environment。
3. 等待运行时 `generate.sh` 初始化完成。
4. 执行 `answer.sh`。
5. 执行全部公开检查点。

成功时，VerifyTask status 保存构建、答案和检查点维度的报告及临时镜像引用。失败时，Server 的作者会话调和器按 `report.class` 区分 artifact 与基础设施错误；只有前者会启动下一 Generator Run。它不会把失败 artifact 暴露给作者。

## 审核与发布

只有已通过 VerifyTask 的 artifact 才进入 `AwaitingVerifiedReview`。作者可以查看题目元数据、检查点、只读资产、diff 和成功验证摘要；对已验证 revision 的自然语言修改会自动启动新一轮生成与验证，旧 revision 保持可见。

作者显式点击发布后，Server 从 VerifyTask 读取已验证镜像，将 artifact 原子提升为 `data_dir/challenges/<opaque-id>/`，写入平台管理的 `id`、`image` 与 `published_at`，再记录发布完成。发布不重新生成也不重新验证。

## 安全与边界

- Generator、VerifyTask 和 Server 的 artifact 交接使用内部 API key；没有公开 artifact 上传入口。
- 题目目录是已发布 catalog 的唯一权威来源；作者会话和临时归档不是 catalog。
- VerifyTask 只验证，不修改 artifact 或发布目录。
- 运行环境和检查点使用与学习者一致的 runtime，详见[运行环境](runtime-environments.md)。
