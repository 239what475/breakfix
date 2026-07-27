# 测试与真实验收设计

## 目标

Breakfix 的测试必须能稳定定位失败边界。不能把模型生成质量、Agent 自动修复、OpenSandbox、镜像构建、Registry、Verifier、Kubernetes 环境和浏览器交互串成一条日常 E2E；那样单次成本高、耗时不可预测，失败也无法判断是产品代码、基础设施还是模型输出的问题。

真实环境仍然必须覆盖，但应按边界拆分，并让每一层只证明一类事实。

## 分层

### 1. 确定性代码测试

`go test ./...` 覆盖不依赖真实模型、浏览器或集群生命周期的契约：

- Agent Run 的租约、围栏、取消、重试和状态转换。
- Server 对 Authoring、Generator submission、VerifyTask status 的持久化与幂等交接。
- Controller 对 CRD 的确定命名、状态投影和清理决策。
- archive 格式、challenge 元数据、检查点协议和 API 输入输出校验。

Prompt 文案不是单元测试对象。测试只能验证结构化结果、工具参数和领域状态；不能用伪造模型回复来证明自然语言 prompt “正确”。

### 2. 真实运行时组件验收

这层使用固定、已审阅的 challenge artifact，不调用模型。它分别验证：

- Registry 构建并推送镜像。
- 独立 verifier Job 创建对应的 `VerifyTask` 和真实 Environment。
- `runtime: container` 与 `runtime: vcluster` 都能完成运行时初始化、`answer.sh` 和全部检查点。
- VerifyTask 的成功、artifact 失败、infrastructure 失败及资源回收边界。

固定 artifact 不是伪造验证：它仍会使用真实 Registry、Controller、Verifier、Kubernetes 和 vcluster。它只是把不确定的模型产物从运行时能力验证中隔离出去。

### 3. 浏览器 E2E

Playwright 只验证用户可见的工作流，不负责生成题目：

- 注册、登录、Catalog、筛选和 My Space。
- 启动一个已发布的固定 container 或 vcluster 题。
- 通过浏览器键盘向终端输入命令，观察连接状态和检查点自动更新。
- 作者工作台的页面布局、会话交互、已验证资产展示与发布按钮状态。

浏览器 E2E 不创建模型生成 Run，不等待自动修复，也不把 vcluster 供应、模型 API 和 UI 组合成一个断言。需要 vcluster 的浏览器覆盖应选择一个固定的已发布题目。

### 4. Agent Live 验收

真实模型生成是发布前或人工触发的验收，不是普通 CI gate。它验证完整的 Agent Runtime：

1. 作者通过网页描述明确题意。
2. Agent Worker 在 Server 管理的 OpenSandbox workspace 中生成 artifact。
3. Judge 审核候选，Server 创建真实 VerifyTask。
4. artifact 错误时，Server 以报告创建新的 Generator Run；基础设施错误不得让 Agent 修改 artifact。
5. 验证成功后，作者审核并发布；随后可在浏览器启动该题并完成检查点。

这类验收必须使用明确、简短的题意，并单独记录每个阶段的 Run、VerifyTask 和日志。自动修复可以作为产品行为观察，但不能成为通过条件的一部分：模型修复轮次不受代码仓库控制，最多受一小时 Run deadline 约束。

## 运行原则

- 默认 `make e2e` 只能运行快速、可重复的浏览器测试；不会调用模型或创建真实验证资源。
- 真实组件验收和 Agent Live 验收都必须由显式命令或环境变量开启，且串行执行。
- 真实环境测试结束后删除它创建的 VerifyTask、Environment、OpenSandbox workspace/PVC、验证 Job 和临时 challenge，仅保留仓库中的固定题目。
- 用 Telepresence 本地接管 Server、Controller 或 Worker 时，真实验收日志应与 Playwright 输出并排观察；失败时先按阶段日志定位，不通过整条测试的最终超时猜测原因。

对应入口：

- `make e2e`：默认浏览器页面测试。
- `make e2e-runtime-verify`：固定 container/vcluster artifact 的真实 VerifyTask 验收。
- `make e2e-runtime-browser`：固定 `cleanup-logs` 的终端、检查点和学习进度浏览器验收。
- `make e2e-server-recovery`：Server/Controller 恢复验收。
- `make e2e-agent-assistant`、`make e2e-agent-container`、`make e2e-agent-vcluster`：分别运行显式 Agent Live 验收；它们不属于日常 CI。
- `make e2e-agent-soak`：在一个真实 `cleanup-logs` 环境中串行完成 20 次 Assistant Run。首轮读取真实 scrollback、检查点和解答，后续轮验证同一持久会话的模型传输与完成消息；它是发布前的模型传输 soak，不属于日常 CI。

## 当前迁移要求

Agent Runtime 迁移完成前，至少需要分别证明：

- 不再依赖 `eino-claude-code` 或 Claude Code CLI。
- Worker 只从 PostgreSQL 领取带租约的 Agent Run，不能直接取得 Kubernetes、Registry 或 OpenSandbox 生命周期凭据。
- Server 以 BYO PVC 管理 OpenSandbox workspace；Worker 只能经受围栏的 Server 内部 API 读写、执行和提交。
- VerifyTask 继续由 Controller 与独立 verifier Job 协调，不被 Agent Worker 直接操作。
- container 与 vcluster 的固定 artifact 都能完成一次真实运行时验证。

只有上述分层验收分别通过后，才可以宣称迁移完成；一次大型端到端运行不能替代这些独立证据。
