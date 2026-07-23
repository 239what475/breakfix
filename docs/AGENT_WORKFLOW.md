# Agent 题目生成工作流

用户先提供粗略题意。review agent 将其扩展成边界清晰的 draft；用户可编辑 draft 后启动生成任务。

生成 agent 在持久 session 中生成题目资产：`challenge.yaml`、Dockerfile、`generate.sh`、`problem.md`、`solution.md`、提示、`checks/checkpoints.sh` 和 `answer.sh`。它可以用 `lab_*` 工具创建临时环境、运行 `generate.sh`、`answer.sh` 与 `lab_checkpoints` 进行实验，但这不是发布验证。

judge agent 独立审查题目目标、初始故障、检查点、提示、Solution 和元数据是否自洽，特别检查：

- 没有 `verify.sh` 或只在某个用户动作时运行的隐藏规则。
- 检查器只验证当前环境结果，不要求指定命令或特定编辑路径。
- `answer.sh` 能让全部检查点通过。
- Solution 和提示能解释每个检查点的解法与原理。

judge 通过后系统自动将 artifact 交给 Gateway。Gateway 创建 `VerifyTask`，在真实 runtime 打包、启动题目环境、执行答案并聚合检查点。失败信息返回 judge，再由 judge 指导 generate agent 修正；成功后才发布题目。

因此 agent 的自测、judge 审核和平台真实验证是三个不同层次，最终事实来源始终是 `VerifyTask` 中的公开检查点。
