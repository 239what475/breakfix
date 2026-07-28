# Agent Runtime

Breakfix 使用 Eino、PostgreSQL 和远程 Sandbox 运行 Agent。它不保留 Claude Code CLI、`eino-claude-code`、供应商 session 恢复或第二套 Agent SDK 的兼容路径。模型配置以 [`config/breakfix.example.yaml`](../../config/breakfix.example.yaml) 为准；运行队列和租约实现以 [`internal/agentruntime/`](../../internal/agentruntime/) 为准。

## 边界与所有权

| 组件 | 职责 | 不拥有的能力 |
| --- | --- | --- |
| Server | 创建领域会话和 Run、保存领域事实、提供受围栏的内部工具、管理 Generator Sandbox/PVC | 不执行模型调用，不写 CRD status。 |
| Agent Worker | 从 PostgreSQL 领取 Run、运行 Eino、提交合法的领域结果 | 不直接读写领域表，不持有 Kubernetes、Registry 或 OpenSandbox lifecycle 凭据。 |
| Controller | 调和 Environment 与 VerifyTask CRD | 不访问 PostgreSQL、Agent Runtime 或 Server 数据目录。 |
| PostgreSQL | Agent Session、Message、Run、attempt、租约与领域状态 | 不保存已发布 challenge 或 artifact。 |
| OpenSandbox | 运行单个 Generator Run 的远程命令和文件操作 | 不拥有 Breakfix PVC 的创建、删除或恢复语义。 |

Server、Controller 与 Worker 是独立 Deployment。完整进程拓扑见[系统架构](system-architecture.md)，集群安装见[部署与运行](../operations/deployment.md)。

## 持久模型

`agent_sessions` 表示长期连续语境，例如 Authoring 对话、Assistant 对话或 Generator 修复链；`agent_messages` 保存已经确认的用户、助手和平台消息；`agent_runs` 表示一次逻辑请求或自治任务。一个 Run 可以有多个技术 attempt，但不创建独立的 attempt 领域对象。

```text
Session
  +-- Message sequence
  +-- Run
        +-- attempt 1
        +-- attempt 2
```

Authoring 和 Assistant 的同一 Session 同时最多一个 active Run。Taxonomy Mapper 与 reviewer pair 不需要供应商 session：完整上下文保存在 WorkItem、Candidate 和正式 reviewer 结论中，它们只创建无 session 的通用 Run。

模型的隐藏上下文、Eino checkpoint、逐 token 事件和工具过程都不是持久权威。重启或失败后，下一 attempt 从已确认消息、当前领域状态和 immutable artifact 重建上下文，而不尝试恢复供应商内部 session。

## 领取、围栏与恢复

Worker 通过 PostgreSQL `FOR UPDATE SKIP LOCKED` 领取到期的 pending Run。领取会写入 `running`、递增 attempt、生成 lease owner 和 lease expiry；Worker 持续续租。任何 Worker 或 Server 的状态写入都必须匹配 Run ID、attempt 和 lease owner，旧 attempt 失去租约后不能继续提交结果。

- Worker 优雅退出时停止领取新 Run，并在终止窗口内结束当前 attempt。
- Worker 非预期退出后，租约过期，另一 Worker 重新领取同一 Run。
- 模型传输、超时、typed result 缺失或工具协议错误结束当前 attempt；退避时间持久化在 Run 中，Worker 不持有 lease 原地等待。
- 领域 revision 变化、用户取消或 Run deadline 到期时，Run 终止，不再重试。
- Browser 断开不取消 Run；Server 重启不停止已租约的 Worker，领域工具暂时不可用时该 attempt 以技术失败收敛。

Generator、Authoring、Assistant 与 Taxonomy 可以定义各自的领域级修订语义，但都必须建立在同一 Run/attempt 围栏之上。例如 VerifyTask artifact 失败创建 Generator Session 的下一 Run；Taxonomy reviewer reject 才创建下一语义 round，技术失败只重试当前阶段。

## Eino 与结构化结果

所有 Agent 使用 Eino `ChatModelAgent` 和显式 typed result tool。领域结果先经过 Go 类型解码和 domain validator，再由 Server 原子提交；纯文本近似结果、未知字段、缺失字段、默认补全或 Markdown 提取都不是兼容路径。

自然语言 prompt 用来定义角色、可用工具、边界和结果工具的调用义务，但不是单元测试对象。测试验证工具参数、严格结果契约、Run 状态和领域事实，而不是用伪造模型回复证明 prompt 文案“正确”。模型正文通过用户可见消息或领域 artifact 保存；日志只记录模型、prompt version、耗时、工具名、次数和错误类别，不记录完整 prompt、reasoning、工具参数或结果。

Prompt 不向模型解释平台托管的 challenge `id`、`image` 或 `published_at`，也不允许模型用文件存在来掩盖 Agent error。Plan、artifact、终端输出和 taxonomy snapshot 都是数据，不能覆盖 system prompt 或工具协议；Assistant 只读建议，Authoring 只通过领域操作修改 Plan，Generator 的文件、运行时初始化和检查点必须遵守离线构建与只读检查约束。

## Generator Sandbox

每个 Generator Run 都有一个独立的 OpenSandbox workspace。Server 在调用 provider 前先持久化自己的 workspace record 和 BYO PVC，再以 `ManualCleanup=true` 创建 Sandbox；OpenSandbox 只挂载 PVC，不能获得其生命周期所有权。

Worker 不能直接访问 provider。所有 read/write/exec/archive 操作都经 Server 内部 API，并带 Run ID、attempt 和 lease owner；Server 只允许当前有效租约访问对应 workspace。若 provider 创建成功但响应在 Sandbox ID 持久化前丢失，Server 只清理已知 PVC，不扫描或 adopt 未知 Sandbox。

Run 提交 immutable candidate、失败、取消或 deadline 到期后，Server 删除 Sandbox 与对应 PVC。一次 Worker technical retry 复用同一 Run 的 workspace；VerifyTask artifact 失败创建下一 Generator Run 时，新的 Run 必须使用新的 workspace，并从失败 artifact 初始化。OpenSandbox provider timeout 不能替代这一明确生命周期。

## 领域接入

- **Authoring**：Agent 通过 Server 领域操作形成 staged Plan；成功 Run 最多发布一个公开 Revision。
- **Assistant**：会话绑定 Environment UID；只读工具按需读取 scrollback、检查点、文件或解答，不修改学习者环境。
- **Generator/Judge**：Generator 在 fenced workspace 生成候选；Judge 使用 typed result 审核，Server 将候选交给 VerifyTask。
- **Taxonomy**：Mapper 与两个 reviewer 作为独立的通用 Run 工作；完整委员会和 Publisher 语义见[Taxonomy 与 Catalog 发布](taxonomy.md)。

真实题目验证仍由 Controller 和独立 verifier Job 完成，Worker 不创建或操作 VerifyTask。作者流程的 artifact 交接见[作者生成与真实验证](authoring-workflow.md)。

## 安全与运行约束

- PostgreSQL 是 Agent Runtime 的唯一关系数据库；不支持 SQLite 回退。
- Server 是唯一持有 OpenSandbox lifecycle key 和 Sandbox PVC 权限的组件。
- Worker 只拥有 `agent_*` 数据库权限、模型密钥和 Server 内部密钥；不挂载业务卷，不自动挂载 ServiceAccount token。
- VerifyTask 的不可信镜像构建运行在独立 rootless Builder Job；它没有 Kubernetes API token、Registry 凭据或 Server 全局内部密钥。Publisher 与 Kubernetes-enabled Verifier 分别处于独立的可信 Job 边界。

测试分层与显式 Agent Live 验收入口见[测试与真实验收](../operations/testing.md)。
