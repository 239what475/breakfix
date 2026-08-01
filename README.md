# Breakfix

Breakfix 是一个提供真实、可回收运维实验环境的练习平台。学习者在隔离的
`node` 或 `k8s` 环境中完成题目，检查点自动更新进度。作者先与 Agent 讨论题意，
确认后由后台工作流生成、真实验证并发布题目。

## 架构

- **Server**：HTTP/Web UI、认证、终端代理、题库、学习记录、作者对话与 Assistant
  对话；也是 PostgreSQL 与 taxonomy 文件系统的唯一写者。
- **Controller**：只调和 `NodeEnvironment` 与 `VK8sEnvironment` CRD，供应、检查和
  回收真实环境。
- **Generate Worker**：一次领取一个 `GenerationWorkflow`，顺序执行生成、Judge、构建、
  staging artifact、真实验证、正式发布和 cleanup。
- **Taxonomy Worker**：一次领取一个 `TaxonomyWorkflow`，执行 Mapper、两位并行 reviewer
  和 taxonomy snapshot 发布。
- **PostgreSQL**：账户、学习事实、AuthoringSession、AgentRun、CandidateRevision 和两类
  Workflow 的权威存储。
- **Registry / Incus**：分别保存 K8s OCI 产物与 Node system-container image；它们不是
  浏览器 API 的一部分。

Worker 不持有 PostgreSQL 凭据。所有 lease、状态转移和阶段结果都通过 Server 的内部 API
完成；后台调度只围绕两条具体 Workflow 进行。

架构边界和数据所有权见[系统架构](docs/architecture/system-architecture.md)。

## 本地开始

前置条件和运行时配置以[`config/breakfix.example.yaml`](config/breakfix.example.yaml)为准。
本地开发需要 PostgreSQL、Kubernetes 访问、模型凭据和 OpenSandbox；运行 Node 题还需要准备好的 Incus
provider。Kind 开发使用 `deploy/overlays/kind` 提供固定 `NodePort 30443` Registry，先运行
`make dev-kind-registry`；生产部署使用运营方提供的 Registry 配置。

```bash
cp config/breakfix.example.yaml config/breakfix.yaml
# 填写 PostgreSQL、模型、OpenSandbox、Registry 与 Incus 配置。
make dev
```

`make dev` 构建并启动 Server、Controller、Generate Worker 和 Taxonomy Worker。默认界面位于
`http://localhost:9090`。本地配置含环境专属地址和密钥，不应提交。

## 验证

```bash
go test -count=1 ./...
npm run build --prefix web
make verify-generated
kubectl kustomize .
make e2e
```

真实 Kubernetes 与模型验收需要显式启用，见[测试与真实验收](docs/operations/testing.md)。

## 文档与契约

- [文档索引](docs/README.md)
- [作者生成与真实验证](docs/architecture/authoring-workflow.md)
- [Taxonomy 与 Catalog 发布](docs/architecture/taxonomy.md)
- [运行环境](docs/architecture/runtime-environments.md)
- [部署与运行](docs/operations/deployment.md)

机器可验证的契约以代码为准：HTTP 接口见 `api/http/openapi.yaml`，CRD 见
`api/v1/`，题目格式见 `internal/challenge/`，运行时配置见
`config/breakfix.example.yaml`，构建和运维命令见 `Makefile`。
