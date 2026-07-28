# 测试与真实验收

Breakfix 的测试必须能定位失败边界。不能将模型生成质量、Agent 自动修复、OpenSandbox、镜像构建、Registry、Verifier、Kubernetes 环境和浏览器交互串成一条日常 E2E：单次成本高、耗时不可预测，也无法判断失败来自产品、基础设施还是模型。

真实环境仍必须覆盖，但按边界分层，每一层只证明一类事实。

## 分层

### 确定性代码测试

`go test ./...` 覆盖不依赖真实模型、浏览器或集群生命周期的契约：

- Agent Run 的租约、围栏、取消、重试和状态转换。
- Server 对 Authoring、Generator submission、VerifyTask status 的持久化与幂等交接。
- Controller 对 CRD 的确定命名、状态投影和清理决策。
- archive 格式、challenge 元数据、检查点协议和 API 输入输出校验。

Prompt 文案不是单元测试对象。测试验证结构化结果、工具参数和领域状态，不能用伪造模型回复证明自然语言 prompt “正确”。

### 真实运行时组件验收

这一层使用固定、已审阅的 challenge artifact，不调用模型，分别验证：

- Registry 构建并推送镜像。
- 独立 verifier Job 创建对应的 VerifyTask 和真实 Environment。
- `runtime: container` 与 `runtime: vcluster` 都完成运行时初始化、`answer.sh` 和全部检查点。
- VerifyTask 的成功、artifact 失败、infrastructure 失败及资源回收边界。

固定 artifact 不是伪造验证：它仍使用真实 Registry、Controller、Verifier、Kubernetes 和 vcluster，只是将不确定的模型产物从运行时能力验证中隔离。

### 浏览器 E2E

Playwright 只验证用户可见工作流，不负责生成题目：注册、登录、Catalog、筛选、My Space、固定 container/vcluster 题目的终端和检查点，以及作者工作台的会话与已验证资产展示。浏览器 E2E 不创建模型生成 Run，不等待自动修复，也不把 vcluster、模型 API 与 UI 组合成一条断言。

### Agent Live 验收

真实模型生成是发布前或人工触发的验收，不是日常 CI gate。它验证：作者题意、Server-owned OpenSandbox workspace 中的生成、Judge、真实 VerifyTask、artifact failure 的下一 Generator Run、成功后作者审核发布，以及浏览器中真实题目的检查点。

这类验收使用明确、简短的题意，并单独记录 Run、VerifyTask 和阶段日志。自动修复是产品行为观察，不是通过条件：模型修复轮次不受仓库控制，只受 Run deadline 约束。

### Taxonomy Live 验收

taxonomy 有独立的真实模型闭环，不与其他 Agent Live 验收并发。它在隔离 data directory 中仅放入已验证 `cleanup-logs` artifact，且开始时没有 `taxonomy/current`。Server scheduler 创建 Mapping WorkItem，真实 Agent Worker 执行 Mapper 和并行 reviewer pair，Publisher 写入 immutable snapshot；浏览器随后确认 Catalog 卡片中的结构化 Tag 与主要 outcome，Catalog API 确认完整 taxonomy 投影。

整个流程只允许一个总 deadline。deadline 到期、WorkItem 进入 Failed/Cancelled 或任一 Agent Run 失败时，测试必须立即输出 WorkItem、关联 Run 和 snapshot 状态，然后清理临时 data directory、数据库 schema 与浏览器资源。它不属于日常 CI，也不重试第二次掩盖模型或基础设施问题。

## 运行原则与入口

- 默认 `make e2e` 只运行快速、可重复的浏览器测试，不调用模型或创建真实验证资源。
- 真实组件与 Agent Live 验收必须由显式命令或环境变量开启，并串行执行。
- 真实测试结束后删除其创建的 VerifyTask、Environment、OpenSandbox workspace/PVC、验证 Job 和临时 challenge，只保留仓库固定题目。
- Telepresence 接管 Server、Controller 或 Worker 时，将本地组件日志与 Playwright 输出并排观察；失败时按阶段日志定位，而不是等待整条测试超时。

| 命令 | 证明的边界 |
| --- | --- |
| `make e2e` | 默认浏览器页面测试。 |
| `make e2e-runtime-verify` | 固定 container/vcluster artifact 的真实 VerifyTask。 |
| `make e2e-runtime-browser` | 固定 `cleanup-logs` 的终端、检查点和学习进度。 |
| `make e2e-server-recovery` | Server/Controller 恢复。 |
| `make e2e-agent-assistant` | Assistant 的真实模型验收。 |
| `make e2e-agent-container` / `make e2e-agent-vcluster` | 生成、VerifyTask 和发布的真实模型验收。 |
| `make e2e-agent-soak` | 同一真实环境中 Assistant 会话与模型传输的串行 soak。 |
| `make e2e-taxonomy` | 隔离 taxonomy committee 的单次真实模型验收。 |

运行时与 Agent Runtime 的边界见[系统架构](../architecture/system-architecture.md)、[Agent Runtime](../architecture/agent-runtime.md)和[Taxonomy 与 Catalog 发布](../architecture/taxonomy.md)。
