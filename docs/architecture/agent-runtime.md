# Agent Runtime

Breakfix 使用 Eino、PostgreSQL 和远程 Sandbox 运行 Agent。系统不保留 Claude Code CLI、`eino-claude-code`、供应商 session 恢复或第二套 Agent SDK 的兼容路径。模型配置以 [`config/breakfix.example.yaml`](../../config/breakfix.example.yaml) 为准；Run、WorkItem 和租约实现以 [`internal/agentruntime/`](../../internal/agentruntime/) 和 [`internal/worklist/`](../../internal/worklist/) 为准。

## 边界与所有权

| 组件 | 职责 | 不拥有的能力 |
| --- | --- | --- |
| Server | 领域状态、AgentRun、CandidateRevision、WorkItem、内部工具、Generator Sandbox/PVC | 不执行模型调用，不写 Environment status。 |
| Agent Worker | 领取 `agent` WorkItem，运行 Eino，提交严格合法的领域结果 | 不直接写 PostgreSQL，不持有 Kubernetes、Registry、Incus 或 OpenSandbox lifecycle 凭据。 |
| Builder / Publisher / Verifier Worker | 分别执行固定的构建、staging 发布和真实验证阶段 | 不调用模型，不直接写 PostgreSQL，也不推进无关阶段。 |
| Controller | 调和 NodeEnvironment 与 VK8sEnvironment 的 spec、status 与 finalizer | 不访问 PostgreSQL、候选归档或 Agent Runtime。 |
| PostgreSQL | 领域记录、WorkItem、租约和恢复权威 | 不保存已发布题目内容或大 artifact。 |
| OpenSandbox | 运行单个 Generator Run 的受限文件和命令操作 | 不拥有 Breakfix PVC 的创建、删除或恢复语义。 |

所有固定 Worker 通过 Server 内部 API 工作。它们不能持有 PostgreSQL 凭据；Server 是唯一数据库写者。不同阶段有独立 Deployment、ServiceAccount、Secret、NetworkPolicy 和进程内并发 1，因此副本数就是相应阶段的全局并发上限。

## 持久模型与领取

`AgentRun` 表示一次 Authoring、Generator、Judge、Assistant 或 Taxonomy 模型调用，保存 session、输入、typed result、deadline 和消息。`WorkItem` 是唯一调度信封，保存 kind、subject、状态、attempt、lease、next run 和最小错误摘要；它不复制 prompt、候选文件或完整报告。

Worker 用 PostgreSQL `FOR UPDATE SKIP LOCKED` 领取最早可运行的任务。领取会进入 `running`、递增 attempt、写入随机 lease owner 和 expiry。续租、工具访问、artifact handoff、完成、失败和取消都必须匹配 `work_item_id + attempt + lease_owner`。续租失败时 Worker 取消当前 context，旧 attempt 不能在恢复后写入任何结果。

技术失败将同一个 WorkItem 退避后放回 Pending；lease 过期可被另一副本以新 attempt 接管。Server 在启动和固定周期中回收已到 deadline 的 AgentRun 和 Candidate stage，即使没有 Worker 再次领取也会终结其 WorkItem 与领域状态。模型传输、超时、typed result 缺失或工具协议错误属于技术失败，而不是新的领域 revision。领域输入变更或用户取消会取消 subject 与 WorkItem，迟到结果一律拒绝。

## Eino 与结构化结果

所有 Agent 使用 Eino `ChatModelAgent` 和显式 typed-result tool。领域结果先经过 Go 类型解码和 domain validator，再由 Server 原子提交；纯文本近似结果、未知字段、缺失字段、默认补全或 Markdown 提取都不是兼容路径。

自然语言 prompt 只定义角色、可用工具、边界和结果工具的调用义务，不是单元测试对象。测试验证工具参数、严格结果契约、Run 状态和领域事实，而不是以伪造模型回复验证 prompt 文案。日志只记录模型、prompt version、耗时、工具名、次数和错误类别，不记录完整 prompt、reasoning、工具参数或结果。

Generator prompt 不向模型解释平台托管的 challenge `id`、`image` 或 `published_at`。Plan、候选 artifact、终端输出和 taxonomy snapshot 都是数据，不能覆盖 system prompt 或工具协议。

## Generator Sandbox

每个 Generator Run 有独立的 OpenSandbox workspace。Server 在调用 provider 前持久化自己的 workspace record 与 BYO PVC，再以 `ManualCleanup=true` 创建 Sandbox；OpenSandbox 只挂载 PVC，不能取得生命周期所有权。

Agent Worker 只能经受围栏内部 API 执行 read/write/exec/archive/finalize。一次技术 retry 复用同一 Run 的 workspace；artifact failure 创建下一 Generator Run 时使用新的 workspace，并从失败 CandidateRevision 的 immutable archive 初始化。Run 成功、失败、取消或 deadline 到期后，Server 删除该 Sandbox 与 PVC。

## 领域接入

- **Authoring**：Agent 只通过领域函数修改私有 Plan，成功 Run 原子公开一个 authoring revision。
- **Assistant**：会话绑定 Environment UID；只读工具按需读取 terminal scrollback、检查点、文件或参考解答，不修改学习者环境。
- **Generator/Judge**：Generator 在 fenced workspace 生成候选；Judge 通过后 Server 创建 CandidateRevision，并由固定 Builder、Publisher、Verifier Worker 完成真实流水线。
- **Taxonomy**：Mapper 与 reviewer pair 都是 AgentRun；TaxonomyMapping 是领域聚合，attempt 和 lease 仍只属于通用 WorkItem。

真实验证的 artifact、infrastructure、cancelled 路径和作者审核边界见[作者生成与真实验证](authoring-workflow.md)。

## 安全与运行约束

- PostgreSQL 是 Agent Runtime 的唯一关系数据库；不支持 SQLite 回退。
- Server 是唯一持有 OpenSandbox lifecycle key 和 Sandbox PVC 权限的组件。
- Agent Worker 只拥有模型密钥和 Server 内部密钥；不挂载业务卷，不自动挂载 ServiceAccount token。
- Builder 只持有 task-bound Server handoff 和 Node build Project 的 Incus 证书；Publisher 只持有 Registry 写凭据及 build/image Project 证书；Verifier 只持有 Environment CRD、exec 与独立 Provider 身份。

测试分层和显式真实 Agent 验收入口见[测试与真实验收](../operations/testing.md)。
