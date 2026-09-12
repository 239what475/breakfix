# 测试与真实验收

Breakfix 按依赖和失败边界分层测试。日常测试不启动模型、不创建真实学习环境；平台验收只在专用、可丢弃的 Kind target 运行。
失败现场保留，使用显式 reset 丢弃整套目标。

## 测试分层

| 层级 | 证明的内容 | 入口 |
| --- | --- | --- |
| 快速测试 | Go 领域逻辑、API、前端纯逻辑和 Controller 决策 | `make test-unit`、前端测试 |
| 集成测试 | PostgreSQL、OCI/Catalog、Incus 和外部适配器契约 | 按依赖显式运行 |
| 平台验收 | Kind、Registry、Server、Controller、Runtime Worker、Incus 的少量真实主路径 | `make test-e2e`、`make test-e2e-node`、`make test-e2e-recovery` |
| Live Agent 验收 | 真实模型、OpenSandbox、网页 Node/K8s 与 MCP Authoring 全链路 | `make test-acceptance-node`、`make test-acceptance-mcp` 或手工 K8s 入口 |

平台验收不替代快速测试；Controller 的 `envtest` 也不替代真实 Kind。发布冲突、finalizer、revision 并发和数据库边界由 Go
单元/集成测试覆盖，不塞进浏览器场景。

## 可丢弃的 Kind 目标

E2E 只接受明确的 Kind context。默认目标是 `kind-breakfix-e2e`，可通过 `BREAKFIX_E2E_KIND_CLUSTER` 指定另一个专用目标；
当前 kubeconfig context 必须精确等于 `kind-<目标名>`。它使用带所有权标记的 Incus project
`breakfix-e2e-build`、`breakfix-e2e-images` 和 `e2e` 实例前缀，不修改共享的 build/image project、网络或 OpenSandbox 安装。

首次使用前，集群中需要正常运行时 Secret、Registry/Incus 凭据、共享 Incus base image 和当前工作树可构建的本地工具。脚本不会创建
Kind 集群或凭据：

```bash
kind create cluster --name breakfix-e2e
kubectl config use-context kind-breakfix-e2e
make e2e-prepare
```

`e2e-prepare` 依次执行只读 preflight、构建运行时镜像、准备专用 Incus project、部署干净目标、打包并推送
`test/fixtures/catalog-release/` 的 immutable OCI digest、配置该 digest 并等待固定 Catalog 场景公开。它不调用模型、不复制文件到
PVC、不直接写数据库，也不自动运行 Playwright。fixture 是稳定的 Node 运行时场景，prepare 检查它的标题、runtime、类型和直接标签。

准备完成后可分别运行：

```bash
make test-e2e           # 轻量浏览器 UI
make test-e2e-node      # Node 学习主路径
make test-e2e-recovery  # Server 与 Controller restart
```

`e2e-prepare` 选择动态 `127.0.0.1` 端口，把它记录在 `.local/e2e/<target>/ui-origin-port` 并写入 Server 配置。每个测试入口由
`scripts/kind/run-e2e.sh` 独占该端口的 Server port-forward；测试代码不能自行启动 port-forward。

## 平台场景

- UI：未登录用户浏览固定 Catalog、搜索/空结果，登录用户查看 My Space 响应式导航；不创建 Environment 或调用模型。
- Node 学习主路径：独立用户启动固定 Node fixture，执行 `answer.sh`，确认 checkpoint 完成、Environment 为 `Completed`，并在学习
  历史看到完成记录。
- 平台恢复：Server restart 后已有 NodeEnvironment 保持 identity 并可完成 checkpoint；Controller restart 后仍能继续调和并提供终端。

发布验证与学习环境的运行顺序不同：发布 Verifier 在 runtime init 后先执行 `reproduce.sh`，确认每个目标现象证据都存在；只有这样才执行
`answer.sh`，再执行 `checks.sh` 验证参考修复。复现证据缺失、脚本协议错误或执行失败时，发布失败且不会运行参考修复。学习环境仅执行
初始化脚本，始终不自动运行这些验证或参考修复脚本。

失败时 Playwright 保留 trace、截图和 video。`run-e2e.sh` 在 `.local/e2e/<target>/<suite>-failure-<timestamp>/` 保存
Deployment/Pod/PVC/Environment、事件、组件日志、PostgreSQL 日志和 Incus 实例列表。确认现场后执行 `make e2e-reset`；它只接受
同一个已标记 Kind target，先停止写入组件、回收 workspace 和专用 Incus project，再恢复 prepare 保存的 Secret 快照。

## Live Agent 验收

真实 Authoring 是显式人工验收，不属于日常门禁。网页 Authoring Agent 与本机 `breakfix-mcp` 共用同一个 GeneratorService：作者确认
Plan、提交 candidate，经历真实 `Build -> ArtifactPublish -> Verify`、内容审核和显式发布。打回后只有作者请求的修复会写入 workspace。
MCP 验收额外断言审核包校验并原子投影到临时目录，重复同步幂等、删除投影可恢复，目录没有用户 Token 或底层环境凭据，其他用户 Token
不能读取 workflow。

```bash
RUN_AGENT_LIVE_E2E=1 make test-acceptance-node
RUN_AGENT_LIVE_E2E=1 make test-acceptance-mcp
RUN_AGENT_LIVE_E2E=1 ./scripts/kind/run-e2e.sh acceptance-k8s
```

这些入口只断言持久化状态、公开场景、Environment、checkpoint 结果和 MCP 本地审核投影，不断言模型措辞、prompt、工具调用次数或
Markdown 渲染。失败时保留 AuthoringSession、GenerationWorkflow、AgentRun、CandidateRevision、Environment、Worker 日志和
Playwright 附件，之后用 `make e2e-reset` 丢弃目标。

## 日常验证

```bash
make test-unit
make lint
make build
make verify-generated
kubectl kustomize .
kubectl kustomize deploy/overlays/kind
make test-vk8s-network
```

`test-vk8s-network` 创建并销毁自己的临时 Kind 集群，不属于上述 E2E target；可用
`BREAKFIX_KEEP_VK8S_NETWORK_CLUSTER=1` 保留其失败现场。
