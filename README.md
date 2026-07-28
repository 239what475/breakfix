# Breakfix

Breakfix 是一个在真实 Kubernetes 环境中进行运维练习的平台。用户在隔离的
container 或 vcluster 环境中完成题目；检查点自动评估进度。作者通过 Agent
工作流生成题目，再由真实 `VerifyTask` 构建、启动和验证后发布。

## 架构

- **Server**：HTTP/Web UI、认证、终端代理、题库、学习记录与作者工作流。
- **Controller**：调和 Environment 和 VerifyTask CRD，管理 Pod、vcluster、构建和验证 Job。
- **Agent Worker**：从 PostgreSQL 领取带租约的 Eino Agent Run，并通过受限内部 API 工作。
- **PostgreSQL**：账户、学习记录、作者会话和 Agent Runtime 的权威存储。
- **Registry**：保存验证阶段与已发布的 challenge image。

架构边界、数据所有权和恢复行为见 [系统架构](docs/architecture/system-architecture.md)。

## 本地开始

前置条件：Docker、Kind、kubectl、Go、Node.js、PostgreSQL、`vcluster` CLI；生成题目时还需要
OpenSandbox native Kubernetes provider 和模型/API 凭据。

```bash
cp config/breakfix.example.yaml config/breakfix.yaml
# 填写 PostgreSQL DSN、模型、OpenSandbox 与 Registry 凭据。
make dev
```

`make dev` 会构建本地镜像、安装 CRD/RBAC，并启动 Server、Controller 和 Agent Worker。
界面默认位于 `http://localhost:9090`。本地配置含环境专属地址和密钥，不应提交。

接管已部署集群中的某个进程时，使用 [Telepresence 本地调试](docs/operations/telepresence.md)，
不要同时运行同一组件的本地开发进程。

## 常用验证

```bash
BREAKFIX_TEST_DATABASE_URL='postgres://...' go test -count=1 ./...
npm run build --prefix frontend
make verify-crd-generated
make verify-api-generated
make e2e
```

真实 Kubernetes、浏览器和模型验收按层拆分，不应组合成一条日常 E2E。具体入口和边界见
[测试与真实验收](docs/operations/testing.md)。

## 文档与契约

- [文档索引](docs/README.md)
- [题目内容格式](docs/content/challenge-format.md)
- [作者生成与真实验证](docs/architecture/authoring-workflow.md)
- [运行环境](docs/architecture/runtime-environments.md)
- [部署与运行](docs/operations/deployment.md)

机器可验证的契约以代码为准：HTTP 接口见 `api/openapi.yaml`，CRD 见
`internal/k8s/apis/breakfix/v1/`，题目格式见 `internal/challenge/`，运行时配置见
`config/breakfix.example.yaml`，构建和运维命令见 `Makefile`。
