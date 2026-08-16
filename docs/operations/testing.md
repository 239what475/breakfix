# 测试与真实验收

Breakfix 将测试按依赖和失败边界分层。日常测试不启动模型、不创建真实学习环境；平台验收只在一个专用、可丢弃的 Kind 目标上运行。失败时保留现场，使用显式的 reset 丢弃整套目标。

## 测试分层

| 层级 | 证明的内容 | 入口 |
| --- | --- | --- |
| 快速测试 | Go 领域逻辑、API、前端纯逻辑和 Controller 决策 | `make test-unit`、前端测试 |
| 集成测试 | PostgreSQL、OCI/Catalog、Incus 和外部适配器契约 | 按依赖显式运行 |
| 平台验收 | Kind、Registry、Server、Controller、Runtime Worker、Incus 的少量真实主路径 | `make test-e2e`、`make test-e2e-node`、`make test-e2e-recovery` |
| Live Agent 验收 | 真实模型、OpenSandbox、网页 Node/K8s 与 MCP 三条 Authoring 全链路 | `make test-acceptance-node`、`make test-acceptance-mcp` 或手工 K8s Live 入口 |

平台验收不替代快速测试；Controller 使用 `envtest` 的测试也不替代真实 Kind。发布冲突、finalizer、revision 并发和数据库边界由 Go 单元/集成测试覆盖，不塞进浏览器场景。

## 可丢弃的 Kind 目标

E2E 只接受明确的 Kind context。默认目标是 `kind-breakfix-e2e`，即 Kind 集群名 `breakfix-e2e`；可以通过 `BREAKFIX_E2E_KIND_CLUSTER` 显式指定另一个专用目标。当前 kubeconfig context 必须精确等于 `kind-<目标名>`，不能在普通开发或生产集群上运行这些脚本。

目标还使用两个带所有权标记的 Incus project：`breakfix-e2e-build` 和 `breakfix-e2e-images`，以及 `e2e` 实例名前缀。它们从共享的基础镜像和 bootstrap profile 复制内容，但不修改共享的 `breakfix-build`、`breakfix-images`、`bf` 网络或 OpenSandbox 安装。Server、Controller 和 Runtime Worker 通过同一个 `breakfix-runtime` Secret 读取这组 E2E Incus identity。

首次使用前，集群中需要已有正常运行时 Secret、Registry/Incus 凭据、共享 Incus base image 和当前工作树可构建的本地工具。脚本不会创建 Kind 集群，也不会替用户生成凭据。

```bash
kind create cluster --name breakfix-e2e
kubectl config use-context kind-breakfix-e2e

make e2e-prepare
```

`e2e-prepare` 的顺序是：只读 preflight、构建当前工作树的运行时镜像、标记目标、准备专用 Incus project、将 target 的 Catalog 配置显式置空后部署当前工作树、清理该目标、重新标记以创建本轮的恢复快照，再次部署干净目标，通过 Kind Registry 发布 `test/fixtures/catalog-release/` 的 immutable OCI digest，写入该 digest 并等待公开 Catalog projection。镜像构建发生在 target preflight 之后、任何 target 状态改变之前。prepare 不调用模型、不把文件复制到 PVC、不直接写数据库，也不自动运行 Playwright。

这份 Catalog fixture 是验收自己的分类靶场，而不是 `catalog/` 的替身。它只发布一个稳定的 Node runtime 基线题，用于普通平台和 MCP 场景；其 Roadmap 还提供两个没有绑定基线题目的分类目标：Linux 日志归档，以及 Kubernetes 工作负载与 Service。这样网页 Node 场景可以生成真实的日志归档题，网页 K8s 场景可以生成 Deployment/Service 题，而不必把测试题意扭成运行时标记题。prepare 同时检查公开基线题的 runtime、Domain、Topic 和 Tag，所有检查成功后才写入 prepared marker。

准备完成后分别运行测试：

```bash
make test-e2e           # 轻量浏览器 UI
make test-e2e-node      # Node 学习主路径
make test-e2e-recovery  # Server restart 与 Controller restart
```

`e2e-prepare` 会为这个 target 选择一个动态的 `127.0.0.1` 端口，将它记录在 `.local/e2e/<target>/ui-origin-port`，并把同一精确 Origin 写入 Server 配置。这样终端 WebSocket 仍保持严格 Origin 校验，而不依赖开发者遗留的 `localhost:9090`。每个测试入口由 `scripts/kind/run-e2e.sh` 独占该端口的 Server port-forward；Server rollout 重启导致连接断开时，运行器会在同一端口重新建立转发。测试代码不能自行启动 port-forward。

## 三套平台场景

### UI

`make test-e2e` 只验证未登录用户浏览固定 Catalog、搜索/空结果和已登录用户的 My Space 响应式导航。它不创建 Environment，不检查 Kubernetes/Incus 状态，不调用模型。

### Node 学习主路径

`make test-e2e-node` 使用独立用户启动固定 Node fixture，等待终端连接，执行 fixture 的 `answer.sh`，确认唯一 checkpoint 完成、Environment 进入 `Completed`，并在 Learning history 中看到 `Completed`。只有场景成功后才删除该场景自己登记的 Environment；失败时保留 Environment identity 和 Playwright 附件，整套目标由 `e2e-reset` 清理。

### 平台恢复

`make test-e2e-recovery` 包含两个独立场景：

- Server restart 后，已有 NodeEnvironment 保持同一 identity，终端重新连接并完成 checkpoint。
- Controller restart 后，已有 NodeEnvironment 继续调和，终端可用并完成 checkpoint。

恢复测试只执行真实 Deployment rollout，不 patch CRD 的 TTL、不写数据库、不创建测试专用 Service 或调度器。它们与 UI、Node 主路径分开运行，不能依赖其他测试创建的用户或 Environment。

## 失败现场与清理

Playwright 配置在失败时保留 trace、截图和 video。`run-e2e.sh` 还会在 `.local/e2e/<target>/<suite>-failure-<timestamp>/` 保存 Deployment/Pod/PVC/Environment、事件、各组件日志、PostgreSQL 日志和 Incus 目标 project 实例列表。失败路径不会删除 workflow、Environment 或 provider 资源，便于定位真实 identity。

确认现场已记录后执行：

```bash
make e2e-reset
```

reset 只接受同一个已标记 Kind target，先停止会写入数据的 Server、Runtime Worker 和 Registry，让 Controller 完成 Environment finalizer，再按 PostgreSQL 中的 workspace identity 通过 OpenSandbox Lifecycle API 删除 sandbox（没有持久化 ID 时按 workspace metadata 唯一查询），确认其进入终态后删除对应 PVC，再清理专用 Incus project。最后用 prepare 创建的受管 Secret 快照恢复运行时 Secret 的全部 key 数据，并删除快照和 E2E marker。共享 Incus project、base image、网络和外部 Registry 不在清理范围内。清理失败时保留资源标识并返回失败，不用删除数据库状态掩盖泄漏；没有完整快照时不会继续破坏性清理。

## Live Agent 验收

真实 Authoring 是显式的人工验收，不属于日常门禁。三条入口共用同一个 `GeneratorService` 和后续状态机：

- **网页 Authoring Agent**：先与作者讨论并持久化 Plan，作者在对话中明确确认某个 revision 后，Agent 在同一个回合调用
  `confirm_generation`、操作远程 workspace 并提交 candidate；后续内容审核、分类审核与发布也由作者在对话中确认后经版本绑定
  工具推进。页面只有只读审核 Tab 与正常聊天输入，没有承担授权职责的 lifecycle 按钮。
- **本机 `breakfix-mcp`**：外部 Agent 通过 stdio MCP connector 调用同一组工具。验收额外断言审核包被校验、摘要匹配后原子
  投影到系统临时目录，重复同步幂等、删除投影后可重新同步，目录内没有用户 Token 或底层环境凭据，且其他用户 Token 不能读取
  同一 workflow。
- **网页 K8s Authoring**：网页 Agent 生成一个 Deployment 与 ClusterIP Service 题，经历相同的审核、分类、发布和 VK8s
  验证路径。它通过 `scripts/kind/run-e2e.sh acceptance-k8s` 显式运行，保持与 Node/MCP 验收独立。

三条验收都覆盖 candidate 提交、被打回后的修复与重新提交、真实 `Build -> ArtifactPublish -> Verify`、内容审核、分类审核和
显式发布。Node 和 MCP Make 入口会先重新 prepare；需要在同一 prepared target 上运行 K8s 场景时使用直接入口：

```bash
RUN_AGENT_LIVE_E2E=1 make test-acceptance-node
RUN_AGENT_LIVE_E2E=1 make test-acceptance-mcp
RUN_AGENT_LIVE_E2E=1 ./scripts/kind/run-e2e.sh acceptance-k8s
```

这些入口只断言持久化状态、公开 challenge、Environment、checkpoint 结果与 MCP 本地审核投影，不断言模型措辞、prompt、工具调用
次数或 Markdown 渲染。失败时保留 AuthoringSession、GenerationWorkflow、AgentRun、CandidateRevision、Environment、Worker
日志和 Playwright 附件，之后用 `make e2e-reset` 丢弃目标。

其他真实验收保持独立，必须先手工准备目标并显式启用对应开关：

```bash
RUN_AGENT_LIVE_E2E=1 ./scripts/kind/run-e2e.sh agent-assistant
RUN_AGENT_SOAK_E2E=1 ./scripts/kind/run-e2e.sh agent-soak
```

这些场景需要相应的真实模型、OpenSandbox 或 VK8s 能力；本 P0 不把它们纳入日常 E2E 完成门槛。

## 其他验证

```bash
make test-unit
make lint
make build
make verify-generated
kubectl kustomize .
kubectl kustomize deploy/overlays/kind
make test-vk8s-network
```

`test-vk8s-network` 是独立的网络隔离验收，会创建并销毁自己的临时 Kind 集群，不属于上述 E2E target。它的失败现场由脚本的 `BREAKFIX_KEEP_VK8S_NETWORK_CLUSTER=1` 选项保留。
