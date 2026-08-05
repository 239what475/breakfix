# 测试与真实验收

日常测试必须短、可重复，并只证明一个明确边界。模型、OpenSandbox、Registry、Incus、vcluster、浏览器和真实 Environment 的完整链路仍要验收，但不能伪装成稳定的单条单元测试。

## 确定性验证

```bash
make test-unit
make lint
make build
make verify-generated
kubectl kustomize .
kubectl kustomize deploy/overlays/kind
make test-e2e
```

Go 测试覆盖 archive、portable challenge/roadmap source、运行时快照、检查点 JSON、Provider 请求、API 输入输出、Catalog Release 的阶段恢复与原子提交，以及 Controller 的状态和清理决策。Prompt 文案不是单元测试对象；测试验证 typed result、工具参数和领域状态，不伪造模型输出来证明自然语言 prompt。

默认 `make test-e2e` 只覆盖快速浏览器页面流程，不调用模型，也不人为写入数据库伪造后台流程。测试 Server 必须在启动前通过 `catalog.release_reference` 配置 immutable fixture release。global setup 只等待公开 Catalog 出现 fixture；它不调用管理员 API，不复制 challenge 或 Roadmap 文件到 Server data directory。

先生成本地 fixture archive：

```bash
make catalog-package \
  CATALOG_SOURCE=test/fixtures/catalog-release \
  CATALOG_ARCHIVE=dist/e2e-catalog.oci.tar
```

将 archive 推送到测试 Registry 后，把得到的 immutable digest 写入测试环境 `breakfix-runtime` Secret 的 `catalog_release_reference`，然后重启 Server。`make test-e2e` 会复用 `test/node_modules`；只有测试锁文件变化或依赖缺失时才重新执行 `npm ci`。

## 已部署运行时验收

```bash
RUN_RUNTIME_E2E=1 npm run test:runtime:browser --prefix test
BREAKFIX_E2E_BASE_URL=http://localhost:9090 RUN_SERVER_RECOVERY_E2E=1 npm run test:recovery --prefix test
```

`e2e-runtime-browser` 与 `e2e-server-recovery` 只在专用测试平台运行。该平台也必须预先配置
`test/fixtures/catalog-release/` 对应的 immutable release；测试从 Catalog 动态读取 fixture 的发布 ID，不依赖生产题目的身份。前者验证终端、自动检查点、完成投影与停止，后者验证 Server 或 Controller 重启后环境生命周期仍可收敛。

## 模型驱动验收

```bash
RUN_AGENT_LIVE_E2E=1 npm run test:agent-live:node --prefix test
RUN_AGENT_LIVE_E2E=1 npm run test:agent-live:k8s --prefix test
RUN_AGENT_LIVE_E2E=1 npm run test:agent-live:assistant --prefix test
RUN_AGENT_SOAK_E2E=1 npm run test:agent-live:soak --prefix test
```

这些入口需要显式环境变量和已部署的当前架构。Node/K8s 作者验收从自然语言题意开始，经过 Server 内的 Authoring、Generator、Judge 和 Classifier Agent Runtime，再由 Runtime Worker 完成构建、真实验证与正式发布，最后启动学习环境运行答案。它们是完整流程验收，必须串行运行并在失败时保留 Workflow、AgentRun、CandidateRevision、Environment 和 Worker 日志用于定位，不能自动重跑掩盖问题。

Assistant 验收验证真实终端上下文、工具调用和 Markdown 渲染；soak 验收验证同一持久对话的连续调用。它们不替代题目生成验证。

## 原则

- 实际 E2E 只删除自身精确登记的 Environment 和临时资源，不能按宽泛前缀清理。
- 未部署当前 `breakfix-runtime-worker` 时，不能将旧 Deployment 的结果视为本次架构验收。
- Telepresence 接管时，将本地日志与测试输出并排观察；以 Workflow ID、state、attempt 和 Environment UID 定位，而不是使用旧 `kind` 路由术语。
