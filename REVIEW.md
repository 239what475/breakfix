# Breakfix 代码审计

审计时间：2026-07-28
审计基线：`be64d7f feat(verification): isolate untrusted challenge builds`，以及当前
工作区尚未提交的 P1/P2 交付保障改动。

## 结论

当前仓库已经完成了最重要的一次架构收敛：Server、Controller、Agent
Worker、Builder、Publisher、Verifier 和 PostgreSQL 的职责已能在代码、CRD 和
部署清单中辨认；
Eino Runtime、租约、围栏、OpenSandbox BYO PVC、文件系统题库和 taxonomy
snapshot 也没有再保留 Claude Code 或 SQLite 的兼容路径。这里不需要为了
“文件更小”再把 `internal/server`、`internal/controller`、`internal/db` 或 Vue
feature 目录拆碎。

此前阻止开放作者功能的 P0 已关闭：不可信候选只在 rootless、无
ServiceAccount、无 Registry 凭据的 Builder Job 中构建；Publisher 和 Verifier
被拆到独立的可信 Job；最终镜像只能由 Server 以已验证的 staging digest 提升。
真实 Kind 验收已覆盖 container/vcluster 成功路径、构建失败、检查点失败后的
清理，以及 Builder 的 NetworkPolicy 出站边界。

本轮关闭了所有 P1/P2 项：CI/release 现在覆盖持久化测试、完整运行产物和真实
浏览器 smoke；生产进程不会回退到开发默认配置；终端 bearer token 不再出现在 URL；
RBAC/status 所有权、遗留 proxy/CA、PTY 取消和题库严格 readiness 均已收敛。剩余
工作是 P3 的容量目标与非关键仓库体验改善，不应阻塞后续产品方向。

## 审计范围与证据

已阅读：进程入口、Server/Controller/Worker/Verifier、CRD、PostgreSQL
schema、Agent Runtime、challenge/taxonomy、前端终端与认证、Kustomize、
RBAC、Dockerfile、Makefile、CI/release 和现有文档。

在此工作区执行并通过：

- 设置临时 PostgreSQL `BREAKFIX_TEST_DATABASE_URL` 后执行
  `go test -count=1 ./...`。
- `npm run build --prefix frontend`、`make verify-crd-generated`、
  `make verify-api-generated` 与 `kubectl kustomize .`。
- 使用 `test/fixtures/catalog/` 的 immutable taxonomy snapshot 启动真实 Server 后执行
  `npm run test:e2e --prefix test -- catalog.spec.ts --workers=1`，两个 Catalog smoke
  用例通过。
- 以六个占位 digest 渲染 release manifest，确认 Deployment 和 ConfigMap 中的全部
  runtime image 都被替换，且没有遗留 `ghcr.io/breakfix/...:dev`。
- `npm run test:runtime:builder-boundary --prefix test -- --workers=1`：专用
  Kind+Cilium 集群确认 Builder 可访问 Server、不可访问 Registry 和
  `kubernetes.default:443`，且没有 ServiceAccount token、image pull secret 或
  Registry/internal API 凭据。
- `npm run test:runtime:verify --prefix test -- --workers=1`：四个真实
  VerifyTask 场景在 6.9 分钟内通过，包含固定 container/vcluster artifact、真实
  BuildKit `RUN exit 1` 构建失败，以及 Publisher 后的 checkpoint 失败和 staging
  manifest/Environment 清理。
- `git diff --check`。

本轮未重新运行真实模型或 OpenSandbox live 验收；它们仍是独立的人工/发布前边界，
不能由固定 browser smoke 代替。Builder 边界测试为验证 NetworkPolicy 使用专用
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

## 已完成：P1 交付与运行保障

### 1. CI、release 与真实运行时

`.github/workflows/ci.yml` 现在以 PostgreSQL service 运行
`go test -count=1 ./...`，校验 CRD/OpenAPI 生成物，并编译 Server、Controller、Agent
Worker、Builder、Publisher、Verifier 六个入口。browser-smoke 在 Kind 中安装 CRD，启动
真实 Server，并以 `test/fixtures/catalog/` 中的完整 immutable snapshot 执行 Playwright
Catalog 断言；它不依赖被忽略的本地 `data/taxonomy` 或模型调用。

`.github/workflows/release.yml` 通过 `make release-manifest` 构建并推送全部六个 OCI
image，解析每个 Registry digest，渲染 Kustomize 并上传 digest-pinned 的
`breakfix-<version>.yaml`。该 target 同时替换 Deployment 与 ConfigMap 内的 Job image
引用，并拒绝保留源仓库的 `:dev` 引用。

### 2. 配置、Sandbox 与 readiness

`config.Load` 只读取明确提供的 YAML，不再注入任何开发默认值；缺失文件直接失败。
`ValidateServer`、`ValidateController` 和 `ValidateAgentWorker` 分别检查真实依赖，三个
进程入口均在创建客户端前调用对应校验。OpenSandbox 或 workspace manager 初始化错误会
中止 Server 建立；`/healthz` 只报告进程存活，`/readyz` 与启动过程都会严格校验整个
challenge catalog。Deployment probe 已改用这两个端点。

### 3. 终端认证与 Origin

终端 JWT 已从 WebSocket query 删除。Server 经受 JWT 保护的 HTTP 接口签发一分钟、一次性、
绑定 user/environment/challenge/window 的随机 ticket；数据库仅保存 SHA-256 hash，并通过
单条 `UPDATE ... RETURNING` 原子消费。前端仅将短 ticket 放入 WebSocket query。WebSocket
在消费 ticket 前及 Gorilla upgrader 中都严格比较配置的 `ui_origin`，不同 Origin 不会烧掉
有效 ticket。OpenAPI、Go 和前端生成类型已同步。

### 4. RBAC 与 VerifyTask status 协议

Server ClusterRole 已移除所有 Environment 和 VerifyTask `*/status` 写权限。架构文档明确
Controller 只拥有调度阶段和 verifier 未给出终态时的 infrastructure failure；Verifier Job
独占 `Verifying -> Succeeded/Failed` 的 answer/checkpoint 结论；Server 只读 status 并投影
领域状态。

## 已完成：P2 遗留清理

### 5. 废弃代理、CA 与配置键

`internal/proxy`、`internal/ca`、`proxy_port`、`k8s_base_image`、未使用的 image/cooldown
accessor 与 Handler `serverHost` 均已删除；示例和 in-cluster YAML 不再声明这些不存在的
运行时能力。

### 6. PTY 取消传播

`k8s.ExecPTY` 现在接收终端 session context，并以 `StreamWithContext` 执行 SPDY stream。
WebSocket 输入结束或 handler 返回时会取消该 context；tmux session 仍保留在 Pod 内。`TestStreamPTYPropagatesCancellation` 证明取消会结束底层 stream。

### 7. 可追溯部署 artifact

release manifest 是唯一的发布部署输入：所有运行 image 都固定为 Registry 解析出的 digest，
并作为 GitHub Release asset 发布。基础 Kustomize 中的 `:dev` 只服务本地开发，运维文档已
明确区分两条路径。

### 8. 文件系统题库坏目录策略

选择严格模式。任一不合法 challenge 目录会使 Server 启动或 `/readyz` 失败；
`internal/server/readiness.go` 和路由测试覆盖该行为，因此不会静默隐藏坏目录或让不同
Catalog 请求出现不一致结果。

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

1. 为 checkpoint 轮询、Controller 队列和 Agent Worker 建立容量目标、指标与压测基线。
2. 再补根 README 与少量历史命名整理；它们不应与运行时安全或产品功能改动混在一起。
