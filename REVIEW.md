# Breakfix 代码审计

审计时间：2026-07-28
审计基线：`026620c docs: consolidate architecture and test documentation`，以及当前
工作区尚未提交的 P0 构建/发布/验证隔离改动。

## 结论

当前仓库已经完成了最重要的一次架构收敛：Server、Controller、Agent
Worker、Builder、Publisher、Verifier 和 PostgreSQL 的职责已能在代码、CRD 和
部署清单中辨认；
Eino Runtime、租约、围栏、OpenSandbox BYO PVC、文件系统题库和 taxonomy
snapshot 也没有再保留 Claude Code 或 SQLite 的兼容路径。这里不需要为了
“文件更小”再把 `internal/server`、`internal/controller`、`internal/db` 或 Vue
feature 目录拆碎。

此前阻止开放作者功能的 P0 已在当前工作区关闭：不可信候选只在 rootless、无
ServiceAccount、无 Registry 凭据的 Builder Job 中构建；Publisher 和 Verifier
被拆到独立的可信 Job；最终镜像只能由 Server 以已验证的 staging digest 提升。
真实 Kind 验收已覆盖 container/vcluster 成功路径、构建失败、检查点失败后的
清理，以及 Builder 的 NetworkPolicy 出站边界。

这不代表仓库已经达到生产级保障。当前主要缺口转为 P1：CI/release 没有覆盖
完整持久化与运行产物，生产配置仍可静默退回开发默认值，终端 bearer token 的
传输和 Server RBAC/status 所有权仍需收敛。应先处理这些问题，再进入技能图或
题库规模化工作。

## 审计范围与证据

已阅读：进程入口、Server/Controller/Worker/Verifier、CRD、PostgreSQL
schema、Agent Runtime、challenge/taxonomy、前端终端与认证、Kustomize、
RBAC、Dockerfile、Makefile、CI/release 和现有文档。

在此工作区执行并通过：

- 设置临时 PostgreSQL `BREAKFIX_TEST_DATABASE_URL` 后执行
  `go test -count=1 ./...`。
- `make verify-crd-generated` 与 `make verify-api-generated`。
- `npm run test:runtime:builder-boundary --prefix test -- --workers=1`：专用
  Kind+Cilium 集群确认 Builder 可访问 Server、不可访问 Registry 和
  `kubernetes.default:443`，且没有 ServiceAccount token、image pull secret 或
  Registry/internal API 凭据。
- `npm run test:runtime:verify --prefix test -- --workers=1`：四个真实
  VerifyTask 场景在 6.9 分钟内通过，包含固定 container/vcluster artifact、真实
  BuildKit `RUN exit 1` 构建失败，以及 Publisher 后的 checkpoint 失败和 staging
  manifest/Environment 清理。
- `git diff --check`。

本轮没有重新运行前端构建、浏览器 smoke、真实模型或 OpenSandbox 验收；不能把
它们视为已由本次 P0 测试证明。Builder 边界测试为验证 NetworkPolicy 使用专用
Cilium Kind 集群；Cilium 不是运行时捆绑依赖，但生产集群必须使用会实际执行
NetworkPolicy 的 CNI。

## 已完成：P0 作者构建与验证隔离

此前单一 verifier Pod 同时运行候选 BuildKit、持有 Registry 写凭据并调用
Kubernetes API。当前 `VerifyTask` 仍是唯一的业务 CRD，但 Controller 已将其拆为
三个确定性 Job，职责和信任边界分别固定在
`internal/controller/verifytask.go:26-305`：

- **不可信 Builder**：以非 root、非 privileged 方式运行，关闭
  `automountServiceAccountToken`，不注入 Registry Secret、`imagePullSecrets` 或
  Server internal key：`internal/controller/verifytask.go:121-165`。它仅用带
  `taskID`、操作和过期时间的 HMAC grant 下载本次 submission/平台 base，并上传
  OCI archive；grant 不能调用一般内部 API：`internal/verification/grant.go:13-74`。
  Builder 通过任务限定的 OCI layout 取得 base，不访问 Registry：
  `internal/builder/builder.go:110-148`。其 egress policy 只允许 Server 和 DNS：
  `deploy/runtime/build-network-policy.yaml:1-29`。
- **可信 Publisher**：独立于 Builder，才接收 Registry write Secret；它先校验
  Builder 传来的 OCI archive，再只推送 Controller 派生的 staging image：
  `internal/controller/verifytask.go:200-246`、`internal/publisher/publisher.go:35-57`。
  Controller 使用自身 Registry 凭据重新解析该 tag 的 manifest digest，不信任
  Publisher 自报内容：`internal/controller/verifytask.go:249-268`。
- **可信 Verifier**：不再运行 BuildKit 或持有 Registry write Secret，而是拿到
  已解析 digest 后创建真实 ContainerEnvironment/VClusterEnvironment；其专用
  ServiceAccount 只有验证环境和 VerifyTask 所需最小权限：
  `internal/controller/verifytask.go:271-305`、`deploy/rbac/verifier.yaml:1-46`。
- **正式发布与失败清理**：失败任务会删除 staging image 与临时 Environment：
  `internal/controller/verifytask.go:441-455`。作者审核发布时，Server 仅将成功
  VerifyTask 的 immutable staging digest 提升到 opaque challenge ID 派生的正式
  镜像，再落盘 challenge 目录：`internal/server/authoring.go:241-274`。

这个 P0 已有单元、PostgreSQL 集成和真实 Kubernetes 验收，不再保留 rootful /
privileged BuildKit 回退路径。后续变更必须保持此三段边界，不能为构建便利向
Builder 注入 Kubernetes token、Registry 凭据或全局 Server key。

## P1：下一轮应处理

### 1. CI、release 与真实运行时的保障链断裂

**证据**

- CI 只构建前端、列出 Playwright 测试、检查生成物并运行 lint；没有
  `go test ./...`：`.github/workflows/ci.yml:24-38`。
- Playwright 在 CI 中使用 `--list`，没有启动 Server 或执行浏览器断言：
  `.github/workflows/ci.yml:28-31`。
- 依赖 PostgreSQL 的 DB、Server、Worker、Assistant 和 taxonomy 测试会在没有
  `BREAKFIX_TEST_DATABASE_URL` 时跳过；调用点遍布
  `internal/db/*_test.go`、`internal/server/*_test.go` 和
  `internal/agentworker/worker_integration_test.go`。
- CI build job 只编译 Server 和 Controller：`.github/workflows/ci.yml:58-61`；
  tag release 也只发布这两个二进制：`.github/workflows/release.yml:44-47`。
  但运行时还依赖 Agent Worker、Builder、Publisher 和 Verifier；本地生产构建也
  已分别声明这些入口：`Makefile:349-370`。

**影响**

当前绿色 CI 不能证明 schema migration、Run lease、作者流程、Worker 或
Verifier 可用；发布 tag 也不能重现 `deploy/runtime/` 所声明的完整运行时。
这会让下一次 Agent Runtime 或 CRD 变更在合并/发布后才暴露问题。

**建议**

1. 为 CI 提供 disposable PostgreSQL service，并设置
   `BREAKFIX_TEST_DATABASE_URL`，强制执行 `go test ./... -count=1`。测试若
   因缺少该变量跳过，应在 CI 中视为配置错误而非绿色结果。
2. 保留当前分层 E2E 原则，不要恢复一条庞大的模型 E2E；新增一个固定题目、
   无模型的浏览器 smoke lane，真正启动 Server 并执行现有 Catalog/My Space
   断言。
3. CI 至少编译全部六个运行入口；release 应明确二选一：发布全部二进制，或只
   发布 OCI 镜像并在 release 中构建、推送和记录全部运行 image digest。不能继续
   一边部署多个组件，一边只发布两个。

### 2. 配置错误会退回开发默认值，Generator Sandbox 失败会静默失效

**证据**

- `config.Load` 找不到配置文件时直接返回 defaults：
  `internal/config/config.go:141-148`。defaults 含有开发 JWT/internal key 和
  insecure registry：`internal/config/config.go:70-99`。
- `NewHandler` 仅在条件满足时创建 OpenSandbox/Workspace manager，并吞掉
  `opensandbox.New` 与 `workspace.NewManager` 的错误：
  `internal/server/handlers.go:69-79`。
- 后续 Generator 内部接口才以 “runtime is unavailable” 失败：
  `internal/server/generator_workspace.go:226-228`；HTTP readiness 仍只探测
  `/api/openapi.json`：`deploy/runtime/server.yaml:103-113`。

**影响**

配置路径拼错或缺失时，进程可能以不安全的默认密钥启动；OpenSandbox key、
PVC 权限、endpoint 或 storage 配置错误时，Server 能变为 Ready，但用户直到
开始生成才看到失败。这既不利于调试，也削弱了部署配置作为契约的意义。

**建议**

1. 将“可加载默认值”只保留给显式测试构造；三个生产入口必须要求实际配置
   文件存在。
2. 提供按进程划分的校验，例如 `ValidateServer`、`ValidateController`、
   `ValidateAgentWorker`。Server 必须验证 DB、JWT/internal key、OpenSandbox 和
   workspace 参数；Worker 必须验证 agent DB、模型 key 与内部 URL；Controller
   只验证其真实依赖。
3. `NewHandler`/启动过程应返回并记录 Sandbox 初始化错误，或提供明确的
   degraded readiness；不能静默置空依赖。

### 3. 终端 JWT 出现在 URL，WebSocket 接受任意 Origin

**证据**

- 前端把完整 JWT 放进 WebSocket query：
  `frontend/src/features/workspace/useTerminalSession.ts:81-87`。
- WebSocket upgrader 的 `CheckOrigin` 无条件返回 true：
  `internal/server/terminal.go:22-26`。
- JWT 同时持久化在 `localStorage`：`frontend/src/api/client.ts:27-36`。

**影响**

query 中的 bearer token 容易被反向代理、访问日志、调试工具或事故转储记录；
任意 Origin 接受也为未来切换到 cookie 或增加第三方页面时埋下
cross-site WebSocket hijacking 风险。

**建议**

通过一个带 Authorization 的短 HTTP 请求签发一次性、极短有效期、绑定
`user/environment/window` 的终端 ticket，WebSocket 只携带该 ticket；同时将
`CheckOrigin` 限制为明确配置的 UI origin。这样不需要把整个认证体系改成
cookie，也不会给普通 HTTP API 增加额外复杂度。

### 4. Server 的 RBAC 比声明的所有权更宽，VerifyTask status 写者也未被明确建模

**证据**

- 架构文档明确说 Server 不写 Environment status：
  `docs/architecture/system-architecture.md:29`；代码搜索也没有 Server 的
  `Status().Update/Patch` 调用。
- 但 Server ClusterRole 仍可 update/patch 两种 Environment 和 VerifyTask 的
  status：`deploy/runtime/rbac.yaml:22-27`。
- Controller 写调度阶段和 infrastructure failure：
  `internal/controller/verifytask.go:47-76`、`169-179`、`379-405`；verifier Job
  写 Success 和 artifact failure：`internal/verifier/verifytask.go:122-132`、
  `299-330`。
- 文档将 Controller 概括为 VerifyTask 的 status 写者：
  `docs/architecture/system-architecture.md:33-35`，没有描述这个双写协议。

**影响**

前者不必要地扩大了对外 Server 的破坏面；后者虽然已有 terminal phase guard，
但 status 是跨 Controller 和 Job 的同步协议，缺少明确的“谁可以写哪些
transition”的契约和测试矩阵，后续增加 attempt/retry 时容易引入竞态。

**建议**

1. 立即从 Server ClusterRole 删除所有 `*/status` 的 update/patch，只保留读取
   所需权限。
2. 决定并文档化 VerifyTask 的状态机：保留 verifier 写 terminal report 时，
   明确 Controller 只写调度/基础设施失败；若希望 Controller 是唯一写者，则
   改为 verifier 上报受限结果，由 Controller 写 status。两种都可行，关键是
   一个 transition 只能有一个权威写者。
3. 为 Job Complete、Job Failed、verifier 已写 terminal 状态、Controller 重启
   四种交错顺序补 deterministic controller tests。

## P2：应在下一次整理中处理

### 5. 已删除的代理/CA 设计仍残留在代码与配置中

**证据**

- `internal/proxy/proxy.go` 没有任何生产 import；`proxy_port` 只出现在
  `internal/config/config.go` 和两份 YAML。
- `internal/ca/ca.go` 没有生产 import；`Config.CertFile`、`KeyFile` 也没有调用：
  `internal/config/config.go:203-205`。
- `k8s_base_image`、`Config.ImageURL`、`Config.CooldownDuration` 和 Handler 中的
  `serverHost` 字段都没有实际消费者；前两项仍出现在配置模板。

**影响**

配置和目录会继续暗示不存在的运行时行为，增加新成员理解成本。此前 verifier
RBAC 文件名与职责不符的问题已随 P0 迁移到 `deploy/rbac/verifier.yaml`，不再是
本项开放问题。

**建议**

做一次小而完整的删除提交：移除 proxy、ca、无用 Config 字段/方法和模板键。
不要保留“将来也许会用”的空实现。

### 6. 终端关闭没有把请求取消显式传到 Kubernetes exec

**证据**

- WebSocket 层把 `r.Context()` 用于 heartbeat 和租约，但调用 PTY 时没有传入
  context：`internal/server/terminal.go:69-111`。
- `k8s.ExecPTY` 以 `context.Background()` 调用 SPDY stream：
  `internal/k8s/exec.go:231-267`。

**影响**

浏览器关闭目前依赖 pipe/WebSocket 写失败来结束远端 exec。通常能收敛，但没有
取消保证，网络异常或写端卡住时可能留下 stream/goroutine，也使终端生命周期
难以验收。

**建议**

给 `ExecPTY` 增加 `context.Context` 参数，透传请求或显式 session context；在
WebSocket close 时取消它，并补一项 fake executor 或真实 Pod 测试验证取消会
结束 exec。tmux session 应继续保留，取消的只是代理 stream。

### 7. 发布配置不是可追溯的 deployment artifact

**证据**

- 运行清单固定使用 mutable `:dev` 控制面镜像：
  `deploy/runtime/server.yaml:44`、`controller.yaml:18`、
  `agent-worker.yaml:19`。
- `deploy/runtime/README.md:8-17` 要求操作者手动替换 Kustomize 中的开发 tag；
  根 Kustomization 没有环境 overlay 或 `images:` 入口：`kustomization.yaml:1-23`。
- tag release 既不生成部署 overlay，也不推送四个运行镜像。

**影响**

部署清单、构建参数和实际镜像之间没有机器可验证的关联，回滚、审计和重建都会
依赖人工记忆。Registry 的 verifier tag 与控制面镜像也更容易错配。

**建议**

定义一个最小 release overlay：使用不可变 digest 或发布版本 tag，通过
`kustomize images`/CI 生成并保存。release pipeline 负责产生该 overlay 和所有
运行镜像的 digest，部署只消费它，而不是让操作者编辑基础清单。

### 8. 文件系统题库缺少“单个坏目录”时的可用性策略

**证据**

- `challenge.List` 遇到任一目录校验失败就整体返回 error：
  `internal/challenge/store.go:58-84`。
- Catalog 和 My Space 会直接调用它：`internal/server/catalog.go:33-35`、
  `internal/server/my_space.go:75`。

原子 publish 会避免正常流程产生半成品，但题目目录也是版本控制和人工维护的
资产；单个错误目录、手工恢复错误或磁盘损坏仍可让整个 Catalog 请求失败。

**建议**

明确选择其一并测试：

- 严格模式：启动/ready 直接失败，并给出可观测的损坏目录；
- 降级模式：隔离坏目录、返回其余已验证题目，并记录 metric/告警。

不要静默吞掉错误，也不要让一个无关目录把所有已发布题目一起隐藏。

## P3：规模化前规划，不应现在过度重构

### 9. 检查点轮询与控制面资源尚无并发容量模型

Controller 对每个 Ready/Draining Environment 每 4 秒执行一次 Pod 内
`/checks/checkpoints.sh --json`：`internal/controller/checkpoints.go:17-40`、
`internal/controller/container_environment.go:139-175`。Server、Controller 和
Agent Worker 的 Deployment 也还没有 CPU/memory requests/limits：
`deploy/runtime/server.yaml`、`controller.yaml`、`agent-worker.yaml`。

这对当前少量并发题目是合理且简单的，但在大量同时练习的用户下会把 API server、
SPDY exec 和 Controller 工作队列变成主要负载。下一阶段只需先补指标和容量目标，
例如活跃环境数、checkpoint exec 延迟/错误率、queue depth、Worker 领取延迟；
达到目标前不需要提前引入复杂事件系统或分布式调度。

### 10. 仓库入口与少量历史命名仍可改善

根目录没有面向新贡献者的 `README.md`，而 `frontend/README.md` 已有局部说明；
此外测试辅助函数仍使用 `writeGateway...` 命名，`todo.md` 中也保留用于历史
描述的 Gateway 文字。它们不影响运行时，不应与安全和 CI 项混在同一次大改中。

建议在完成 P1 后添加一个简短根 README（项目目标、五个进程、最快启动/测试、
文档入口），并在触及相关测试时顺手改名；`todo.md` 的历史记录可保留，避免把
已完成的演进背景误删。

## 当前不建议修改的部分

- **Server/Controller/Worker 的进程拆分**：当前所有权清楚，Controller 不依赖
  PostgreSQL 和 Server data PVC，Worker 只持有 `agent_*` 权限。不要重新合并成
  单一 gateway。
- **文件系统作为 challenge/taxonomy 内容权威**：原子发布、immutable snapshot
  和单 Server RWO 假设一致。多 Server/共享存储是明确的未来边界，不要现在为了
  假设的扩容引入数据库内容副本。
- **CRD 的位置和生成方式**：`internal/k8s/apis/breakfix/v1` 已是唯一 Go 源，
  CRD/DeepCopy/OpenAPI 都有生成校验；没有外部 Go API 消费者时无需迁回 `pkg` 或
  `apis`。
- **Vue 单页状态管理**：当前不使用 vue-router 是刻意产品选择。`AppShell` 已将
  Catalog、My Space、Studio 和 Workspace 分开，尚未出现需要引入路由器的复杂度。
- **Agent Runtime 的 durable Run/lease 模型**：它与 PostgreSQL、Worker fencing
  和 Server 内部工具边界一致；本次未发现 Claude Code、供应商 session 或 SQLite
  fallback 的代码残留。

## 推荐执行顺序

1. 修复配置 fail-fast、终端 ticket/origin、Server status RBAC，并补对应测试。
2. 给 CI 加 PostgreSQL 和真实 deterministic smoke，统一全部运行产物的 release。
3. 清理 proxy/ca 与失效配置。
4. 在并发目标明确后，再处理 checkpoint 容量、resource requests/limits 和发布
   overlay。
