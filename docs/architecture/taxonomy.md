# Taxonomy 与 Catalog 发布

Taxonomy 将已发布 challenge 映射到可读的 Skill、Tag 与关系图。challenge artifact 与 taxonomy 定义是
两套独立内容：先发布题目，再由 `TaxonomyWorkflow` 建立稳定的 mapping。Server 是 taxonomy 文件系统的
唯一写者。

## 单题 Workflow

每个 `(challenge_id, challenge_revision)` 最多有一个 `TaxonomyWorkflow`：

```text
Queued -> Mapping -> Reviewing -> Publishing -> Completed
                    | reject
                    v
                 Mapping (next semantic round, lease released)
```

- **Mapping**：Mapper 基于 immutable challenge artifact、只读可复用定义目录与上一轮正式意见，提交新定义和目标 mapping；Worker 再确定性转换为内部 ChangeSet。
- **Reviewing**：Curriculum Reviewer 与 SRE Reviewer 并行执行；两份合法结论才构成一轮审查。
- **Publishing**：Server 串行预览、原子写入 `revisions/<sha256>` 并替换 `current` 指针。

当前 ChangeSet 只能复用或创建 Skill/Tag、创建目标 challenge mapping，并为本轮新建 Skill 声明
`requires`。修改已有定义、已有关系或其他 challenge mapping 属于后续全局 maintenance workflow，
不能混入单题流程。

Mapper 的结果工具是独立于内部 ChangeSet 的协议：已有定义只按 ID 引用，新定义只有本轮本地键、标题、
定义与 mapping guidance。模型不会生成 opaque ID、`kind`、引用 title 或 ChangeSet operation；Worker 基于
固定 challenge 和 taxonomy snapshot 生成 ID、补全精确引用并构造 ChangeSet。这样既避免模型改写已有对象，
也让发布层始终拥有唯一的内部变更表示。结果协议将唯一 primary outcome 单独建模，次要 outcome 使用独立
数组，因此不把“恰有一个 primary”的计数约束留给模型自行遵守。Reviewer 结果同样使用 approval 或
rejection（包含 feedback）的互斥结构，而不是由模型组合 `decision` 和可选文本。

提示词按角色分层：稳定的角色、判断标准和工具义务放入 system；每轮 taxonomy、artifact 和历史审核意见
作为带边界的只读用户数据。Mapper 不接收上一轮内部 ChangeSet，只接收两位 reviewer 的意见；Reviewer 也不
接收 ChangeSet，而是接收 Server 从该 ChangeSet 导出的可读候选视图，其中只有新定义、目标题目的关系和新
Skill 的前置关系。内部 operation、opaque ID、存储字段不会进入 reviewer 上下文。

## 重试与并发

模型调用、传输、超时或 typed result 校验失败是技术失败，不增加 semantic round。当前 state 连续十次
技术失败后，Worker 在当前调用结束后释放 lease、保留 state 和候选，并写入退避时间；下一副本从同一
state 接管。artifact 消失、revision 改变或管理员中止才会进入 `Cancelled`；不可恢复存储不变量错误才会
进入 `Failed`。

多个 Workflow 可以并行 Mapping 和 Reviewing。最终 snapshot 构造、校验与 `current` 指针替换由 Server
短暂串行化；这不是另一套队列。若 taxonomy head 变化，纯目标 mapping 的 ChangeSet 可以在最新 snapshot
上确定性重验；涉及新 Skill、Tag 或 `requires` 的 ChangeSet 会清空候选并进入下一 semantic round。

## Catalog 准入

Server 定期扫描已发布 challenge 目录，补回 crash 后遗漏的 TaxonomyWorkflow，也会取消 artifact 已消失或
revision 已变化的活动 Workflow。Catalog 只投影 current snapshot 中与当前 challenge revision 匹配的 mapping，
因此不会展示未完成 taxonomy 的新题。
