# 测试与真实验收

Breakfix 按依赖和失败边界分层测试。日常测试不启动模型、不创建真实学习环境；平台验收只在专用、可丢弃的 Kind target 运行。
失败现场保留，使用显式 reset 丢弃整套目标。

## 测试分层

| 层级 | 证明的内容 | 入口 |
| --- | --- | --- |
| 快速测试 | Go 领域逻辑、API、前端纯逻辑和 Controller 决策 | `make test-unit`、前端测试 |
| 集成测试 | PostgreSQL、OCI/Catalog、Incus 和外部适配器契约 | 按依赖显式运行 |
| 平台验收 | Kind、Registry、Server、Controller、Runtime Worker、Incus 的少量真实主路径 | `make test-e2e-regression`（编排，或其中的 ui/k8s/node/recovery 单套件） |
| Live Agent 验收 | 真实模型、OpenSandbox、网页 Node/K8s 与 MCP Authoring 全链路，以及真实模型驱动的文档实践流水线与管理台 | `make test-e2e-documentation`、`make test-e2e-admin`、`make test-acceptance-node`、`make test-acceptance-mcp` 或手工 K8s 入口 |

平台验收不替代快速测试；Controller 的 `envtest` 也不替代真实 Kind。发布冲突、finalizer、revision 并发和数据库边界由 Go
单元/集成测试覆盖，不塞进浏览器场景。

## E2E 剖面：core 与 full

平台验收链以 `BREAKFIX_E2E_PROFILE` 选择剖面，默认 `full`（与既有本地工作流一致）：

| 剖面 | 平台依赖 | 可跑套件 |
| --- | --- | --- |
| `core` | Kind + in-cluster Registry/PostgreSQL；无 Incus CLI、无 Incus Secret | ui、k8s（acceptance-k8s 的运行时也是 k8s，live 门禁另由 `RUN_AGENT_LIVE_E2E` 把守） |
| `full` | 同上 + 外部 Incus remote（预置 project、基础镜像、profile） | 全部套件 |

core 剖面的前置全部可由脚本生成：在空 Kind 集群上执行 `make e2e-bootstrap-core`，它会创建 runtime
Secret（随机数据库/JWT/Registry 凭据，prepare 链拥有的字段留空或占位）与 Registry htpasswd Secret，随后
`BREAKFIX_E2E_PROFILE=core make e2e-prepare` 从零直达 prepared。preflight 会拒绝带残留 `incus_endpoint`
的目标。部署清单对 Incus Secret 的引用全部可选，Server/Controller/Runtime Worker 以 Node-less 模式启动：Node
场景的置备、物化与终端在第一时间报 "provider is not configured"，K8s 场景不受影响。`node`、`recovery`、
`acceptance-node`、`acceptance-mcp`、`acceptance-interruption`、`agent-assistant`、`agent-soak` 套件在 core
剖面下启动即报需要 full 剖面；documentation 与 admin 套件只依赖 k8s 运行时、
不限剖面，但与其他 agent 套件一样要求 `RUN_AGENT_LIVE_E2E=1`（真实模型）；`recovery` 断言 incus PTY 终端重连与 node answer 完成恢复，刻意保留 Node
fixture。full 剖面的 preflight 额外要求 runtime Secret 提供 `incus_endpoint`，且 Incus 证书与 remote 属于
运维提供的外部资源，不由 bootstrap 生成。

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

`e2e-prepare` 依次执行只读 preflight、构建运行时镜像、（full 剖面）准备专用 Incus project、部署干净目标、打包并推送
`test/fixtures/catalog-release/` 的 immutable OCI digest、配置该 digest 并等待固定 Catalog 场景公开。它不调用模型、不复制文件到
PVC、不直接写数据库，也不自动运行 Playwright。fixture 是稳定的 Node 运行时场景，prepare 检查它的标题、runtime、类型和直接标签。

准备完成后可分别运行：

```bash
make test-e2e           # 轻量浏览器 UI
make test-e2e-node      # Node 学习主路径
make test-e2e-recovery  # Server 与 Controller restart
```

单套件入口各自完整执行一次 prepare，保证从任何状态都正确；一次全量回归改用编排入口，只为整条链付一次
prepare：

```bash
make test-e2e-regression
```

它执行一次 documentation prepare，随后按 admin →（数据库级 reset）→ documentation → k8s → ui 串行运行；
full 剖面再追加 node 与 recovery。数据库级 reset 只重建数据库并让 Server 从 runtime Secret 记录的
digest 重装 fixture Catalog，部署、文档库挂载、Registry 与 prepared 标记全部保持原样。

`e2e-prepare` 选择动态 `127.0.0.1` 端口，把它记录在 `.local/e2e/<target>/ui-origin-port` 并写入 Server 配置。每个测试入口由
`scripts/kind/run-e2e.sh` 独占该端口的 Server port-forward；测试代码不能自行启动 port-forward。

## 平台场景

- UI：未登录用户浏览固定 Catalog、搜索/空结果，登录用户查看 My Space 响应式导航；不创建 Environment 或调用模型。
- Node 学习主路径：独立用户启动固定 Node fixture，执行 `answer.sh`，确认 checkpoint 完成、Environment 为 `Completed`，并在学习
  历史看到完成记录。
- 平台恢复：Server restart 后已有 RuntimeEnvironment 保持 identity 并可完成验证；Controller restart 后仍能继续调和并提供终端。

发布验证与学习环境的运行顺序不同：发布 Verifier 在 runtime init 后先执行 `reproduce.sh`，确认每个目标现象证据都存在；只有这样才执行
`answer.sh`，再执行 `checks.sh` 验证参考修复。复现证据缺失、脚本协议错误或执行失败时，发布失败且不会运行参考修复。学习环境仅执行
初始化脚本，始终不自动运行这些验证或参考修复脚本。

失败时 Playwright 保留 trace、截图和 video。`run-e2e.sh` 在 `.local/e2e/<target>/<suite>-failure-<timestamp>/` 保存
Deployment/Pod/PVC/Environment、事件、组件日志、PostgreSQL 日志和 Incus 实例列表。确认现场后执行 `make e2e-reset`；它只接受
同一个已标记 Kind target，先停止写入组件、回收 workspace 和专用 Incus project，再恢复 prepare 保存的 Secret 快照。

## Live Agent 验收

真实 Authoring 是显式人工验收，不属于日常门禁。网页 Authoring Agent 与本机 `breakfix-mcp` 共用同一个 GeneratorService：作者确认
Plan、提交 candidate，经历真实 `MaterializeArtifact -> Verify`、内容审核和显式发布。打回后只有作者请求的修复会写入 workspace。
MCP 验收额外断言审核包校验并原子投影到临时目录，重复同步幂等、删除投影可恢复，目录没有用户 Token 或底层环境凭据，其他用户 Token
不能读取 workflow。

```bash
RUN_AGENT_LIVE_E2E=1 make test-e2e-documentation   # 文档实践链（真实模型）
RUN_AGENT_LIVE_E2E=1 make test-e2e-admin           # 批次调度/看门狗/救援（真实模型）
RUN_AGENT_LIVE_E2E=1 make test-acceptance-node
RUN_AGENT_LIVE_E2E=1 make test-acceptance-mcp
RUN_AGENT_LIVE_E2E=1 ./scripts/kind/run-e2e.sh acceptance-k8s
```

documentation prepare 会拒绝占位或空的 `deepseek_api_key`：这两组套件消费真实模型配额，
断言的是持久化的流水线状态（批次、门禁、digest、发布索引），不断言模型措辞。

这些入口只断言持久化状态、公开场景、Environment、checkpoint 结果和 MCP 本地审核投影，不断言模型措辞、prompt、工具调用次数或
Markdown 渲染。失败时保留 AuthoringSession、GenerationWorkflow、AgentRun、CandidateRevision、Environment、Worker 日志和
Playwright 附件，之后用 `make e2e-reset` 丢弃目标。

## CI 与本地的分工

CI 只运行可以在托管 runner 上复现的层级：

- **push 快车道**（`ci.yml`，每次 push 到 main）：`make test-unit`（带 PostgreSQL service）、
  `make web-test-unit`、`make verify-generated` + `make build` + kustomize 渲染 + golangci-lint。
  单人直推 main，它是干净环境兜底而非合并门禁；
- **按需 nightly**（`nightly.yml`，每晚 03:00 本地时间）：gate 比较 HEAD 与 nightly 上一次
  成功运行的 sha——相同则整夜跳过，失败未修复则自动重试，周日无条件跑一次兜底外部漂移；
  手动 `workflow_dispatch` 遵循同一 gate，`force` 输入可强制全跑。重 job 为 core 剖面全量
  回归（空集群 `make e2e-bootstrap-core` + `BREAKFIX_E2E_PROFILE=core make
  test-e2e-regression`，失败留存 `.local/e2e` 诊断 artifact）与 `make test-vk8s-network`；
- **永不进 CI**：node/recovery 套件（外部 Incus）、live agent 验收（真实模型，含
  documentation/admin 套件）——它们属于本地专用 target 与显式人工验收。

本地日常只跑与变更相关的层级，全量回归交给 nightly 与阶段收尾：

| 变更区域 | 本地最相关 |
| --- | --- |
| `web/` | `make web-test-unit`；动到浏览路径再跑 ui 套件 |
| handler / transport / admin、documentation API | `make test-unit`；按需 live 的 admin/documentation 套件（真实模型） |
| runtime（k8s/vcluster）/ controller | `make test-unit`；按需 k8s 套件 |
| runtime（node）/ Incus 适配 | `make test-unit`；full target 上按需 node 套件 |
| `scripts/`、`deploy/`、Makefile | 受影响套件；改 prepare/reset 链后跑一次回归编排 |
| `docs-site/`、文档适配 | `make docs-check`；必要时 docs-smoke |
| 阶段收尾或大改后 | `make test-e2e-regression`（full target） |

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
