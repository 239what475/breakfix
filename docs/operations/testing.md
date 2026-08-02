# 测试与真实验收

日常测试必须短、可重复，并只证明一个明确边界。模型、OpenSandbox、Registry、Incus、vcluster、浏览器和
真实 Environment 的完整链路仍要验收，但不能伪装成稳定的单条单元测试。

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

Go 测试覆盖 archive、challenge manifest、运行时快照、检查点 JSON、Provider 请求、API 输入输出、
`GenerationWorkflow` 与 `TaxonomyWorkflow` 的正向阶段推进，以及 Controller 的状态和清理决策。Prompt
文案不是单元测试对象；测试验证 typed result、工具参数和领域状态，不伪造模型输出来证明自然语言 prompt。

默认 `make test-e2e` 只覆盖快速的浏览器页面流程。它不调用模型，也不人为写入数据库伪造后台流程。
若目标平台的 Catalog 为空，Playwright global setup 要求 `BREAKFIX_E2E_CATALOG_REFERENCE` 提供由
`test/fixtures/catalog-release/` 打包并推送后的 immutable OCI digest，同时要求 `BREAKFIX_CATALOG_ADMIN_TOKEN`；它会调用
正式管理员安装 API 并等待 release 到达 `Ready`。测试不会复制 challenge 或 taxonomy 到 Server data directory。先生成
本地 archive 的命令为：

```bash
make catalog-package \
  CATALOG_SOURCE=test/fixtures/catalog-release \
  CATALOG_ARCHIVE=dist/e2e-catalog.oci.tar
```

将 archive 推送到目标 Registry 后，把得到的 immutable digest 设置为 `BREAKFIX_E2E_CATALOG_REFERENCE`。`make test-e2e`
会复用 `test/node_modules`；只有测试锁文件变化或依赖缺失时才重新执行 `npm ci`。

## 已部署运行时验收

```bash
RUN_RUNTIME_E2E=1 npm run test:runtime:browser --prefix test
BREAKFIX_E2E_BASE_URL=http://localhost:9090 RUN_SERVER_RECOVERY_E2E=1 npm run test:recovery --prefix test
```

`e2e-runtime-browser` 和 `e2e-server-recovery` 只在专用测试平台运行。global setup 在空平台安装
`test/fixtures/catalog-release/`，测试从 Catalog 动态读取 fixture 的发布 ID；它们不依赖生产 Catalog 的题目标题或身份。
前者验证终端、自动检查点、完成投影与停止，后者验证 Server 或 Controller 重启后环境生命周期仍可收敛。

## 模型驱动验收

```bash
RUN_AGENT_LIVE_E2E=1 npm run test:agent-live:node --prefix test
RUN_AGENT_LIVE_E2E=1 npm run test:agent-live:k8s --prefix test
RUN_AGENT_LIVE_E2E=1 npm run test:agent-live:assistant --prefix test
RUN_AGENT_SOAK_E2E=1 npm run test:agent-live:soak --prefix test
```

这些入口需要显式环境变量和已部署的当前架构。Node/K8s 作者验收从自然语言题意开始，经过 Server 直接
Authoring 对话、Generate Worker、真实验证、作者确认、正式发布、Taxonomy Worker 映射，再启动学习环境
运行答案。它们是完整流程验收，必须串行运行并在失败时立即保留 Workflow、AgentRun、CandidateRevision、
Environment 和 Worker 日志用于定位，不能自动重跑掩盖问题。

Assistant 验收验证真实终端上下文、工具调用和 Markdown 渲染；soak 验收验证同一持久对话的连续调用。它们
不替代题目生成验证。

## 原则

- 实际 E2E 只删除自身精确登记的 Environment 和临时资源，不能按宽泛前缀清理。
- 未部署当前 `breakfix-generate-worker` 与 `breakfix-taxonomy-worker` 时，不能将旧 Deployment 的结果
  视为本次架构验收。
- Telepresence 接管时，将本地日志与测试输出并排观察；以 Workflow ID、state、attempt 和 Environment UID
  定位，而不是使用旧 `kind` 路由术语。
