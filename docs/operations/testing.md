# 测试与真实验收

Breakfix 的测试必须隔离失败边界。日常测试不能把模型质量、Agent 修复、OpenSandbox、构建、Registry、Incus、vcluster、真实验证和浏览器交互串成一条长链；真实环境仍必须验收，但每层只证明明确的能力。

## 分层

### 确定性代码测试

`go test ./...` 覆盖不依赖真实模型、浏览器或集群生命周期的契约：

- WorkItem 的并发领取、唯一约束、lease、围栏、deadline、接管和阶段事务。
- Server 对 Authoring、CandidateRevision、Build/ArtifactPublish/Verify/ChallengePublish 的持久化与幂等交接。
- Controller 对 NodeEnvironment、VK8sEnvironment 的状态、清理和检查点决策。
- archive、challenge manifest、运行时快照、检查点 JSON、Provider request 与 API 输入输出校验。

Prompt 文案不是单元测试对象。测试验证结构化结果、工具参数和领域状态，不能用伪造模型回复证明自然语言 prompt “正确”。

### 真实运行时组件验收

`make e2e-runtime-workflow` 使用固定、人工审阅的 challenge artifact，不调用模型，验证真实 `Build -> ArtifactPublish -> Verify`：

- Builder 从受信任 base 形成 Node Incus image 或 K8s OCI artifact，且不执行 `generate.sh`。
- Publisher 返回真实 staging OCI digest 或 Incus fingerprint。
- Verifier 创建真实 `purpose=verification` Environment，等待 runtime-init，运行 `answer.sh` 和全部 checkpoint。
- `runtime: node` 与 `runtime: k8s` 都经过同一阶段图；测试使用实际 Registry、Incus、Kubernetes、vcluster 和固定 Worker Deployment，不使用 fake artifact 或 fake Environment。
- 验证结束后精确清理 Environment、candidate staging 引用和本次测试归档，不按宽泛前缀扫描资源。

`make e2e-runtime-browser` 只覆盖固定已发布题目的用户终端、周期检查点、完成投影与停止。`make e2e-server-recovery` 证明 Server 或 Controller 重启后 Environment 生命周期仍可收敛。

### 浏览器 E2E

默认 `make e2e` 只验证用户可见的确定性流程：注册、登录、Catalog、筛选、My Space、固定题目的页面和窄视口行为。它不调用模型，也不创建真实 Build/Publish/Verify candidate。

CI 应运行 PostgreSQL 下的 `go test -count=1 ./...`、CRD/OpenAPI 生成物校验、前端构建与浏览器套件。Kind 默认 CNI 不执行 NetworkPolicy，因此网络隔离需在能够执行相应策略的真实 CNI 环境单独验收，不能由“清单已创建”替代。

### Agent Live 验收

真实模型生成是人工或发布前验收，不是日常 CI gate。它验证：作者题意、Server-owned OpenSandbox workspace、Generator、Judge、CandidateRevision、真实固定 Worker 流水线、artifact failure 的下一 Generator Run、验证成功后的作者审核与 ChallengePublish，以及发布题目的学习环境。

Agent Live 使用明确、简短的题意，并串行运行。deadline 到期、WorkItem 失败或基础设施异常时应立即输出相关 WorkItem、Run、CandidateRevision、Environment 与 Worker 日志；不得自动第二次运行掩盖问题。artifact failure 的自动修复是产品行为，不以模型轮次作为通过条件。

### Taxonomy Live 验收

taxonomy 有独立模型闭环，不与其他 Agent Live 验收并发。它在隔离 data directory 中只放入已发布 challenge，验证 Mapping、Mapper、reviewer pair、immutable snapshot 与 Catalog 投影。它不需要也不应重复触发 Build/ArtifactPublish/Verify。

## 运行原则与入口

- 默认 `make e2e` 只运行快速、可重复的浏览器测试。
- 真实运行时、恢复和模型验收必须显式启用并串行执行。
- 未设置 `BREAKFIX_E2E_BASE_URL` 时，浏览器套件会临时将本机 `9090` 转发到集群内的 `breakfix-server` Service；显式提供该变量时，调用方负责目标地址的可达性。
- 运行 `make e2e-server-recovery` 时，浏览器地址必须与 Server 的 `ui_origin` 一致，否则终端 WebSocket 会被 Origin 校验拒绝；默认开发配置两者均为 `http://localhost:9090`。
- Telepresence 接管 Server、Controller 或任一 Worker 时，将本地日志与 Playwright 输出并排观察；失败时按 `work_item_id`、kind、subject、attempt 和 Environment UID 定位。
- 真实测试只删除自身精确登记的资源，仓库固定题目和其他测试资源不能被清理逻辑接管。

| 命令 | 证明的边界 |
| --- | --- |
| `make e2e` | 默认浏览器页面测试。 |
| `make e2e-runtime-workflow` | 固定 Node/K8s candidate 的真实 Build、ArtifactPublish、Verify。 |
| `make e2e-runtime-browser` | 固定 `cleanup-logs` 的 Node terminal、检查点和学习进度。 |
| `make e2e-server-recovery` | Server/Controller 重启后的环境回收和检查点。 |
| `make e2e-agent-assistant` | Assistant 的真实模型验收。 |
| `make e2e-agent-node` / `make e2e-agent-k8s` | 生成、修复、真实验证与发布的模型验收。 |
| `make e2e-agent-soak` | 同一真实环境中 Assistant 会话与模型传输的串行 soak。 |
| `make e2e-taxonomy` | 隔离 taxonomy committee 的单次真实模型验收。 |

运行时边界见[系统架构](../architecture/system-architecture.md)、[运行环境](../architecture/runtime-environments.md)和[作者生成与真实验证](../architecture/authoring-workflow.md)。
