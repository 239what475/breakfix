# Breakfix

Breakfix 是一个提供真实、可回收运维实验环境的练习平台。学习者在隔离的
`node` 或 `k8s` 环境中完成题目，检查点自动更新进度。作者可以在网页中与 Authoring
Agent 讨论，也可以让 Codex 等外部 Agent 通过本机 `breakfix-mcp` 现场生成题目；两者
使用同一套 Generator 工具面，确认后经过同一质量门禁、真实验证和显式发布。

## 架构

- **Server**：HTTP/Web UI、认证、终端代理、Catalog、学习记录、Authoring、Assistant、共享 GeneratorService 与 Judge；也是
  PostgreSQL 和 immutable Challenge revision 的唯一写者。
- **`breakfix-mcp`**：用户机器上的 stdio MCP Server；经 HTTPS 与用户 Token 调用远程 Server 的 Generator application API，
  并把不可变审核包原子投影到本机可丢弃的只读目录。
- **Controller**：只调和 `NodeEnvironment` 与 `VK8sEnvironment` CRD，供应、检查和
  回收真实环境。
- **Runtime Worker**：独立运行 Action/Reaper 两条 loop，各自一次领取一个 fenced action；前者执行构建、artifact
  promotion、验证 Environment 和正式发布，后者只执行 runtime resource reaping。
- **PostgreSQL**：账户、学习事实、AuthoringSession、AgentRun、CandidateRevision、
  GenerationWorkflow、CatalogRelease、Challenge 及其 revision 的权威存储。
- **Registry / Incus**：分别保存 K8s OCI 产物与 Node system-container image；它们不是
  浏览器 API 的一部分。

Worker 不持有 PostgreSQL 凭据。所有 lease、状态转移和阶段结果都通过 Server 的内部 API
完成。

架构边界和数据所有权见[系统架构](docs/architecture/system-architecture.md)。

## 本地开始

前置条件和运行时配置以[`config/app/local.example.yaml`](config/app/local.example.yaml)为准。
本地开发需要 PostgreSQL、Kubernetes 访问、模型凭据和 OpenSandbox；运行 Node 题还需要准备好的 Incus
provider。Kind 开发使用 `deploy/overlays/kind` 提供固定 `NodePort 30443` Registry；创建好运行时 Secret 后，
`make deploy-kind` 会构建镜像、准备 Registry 并部署运行时。生产部署使用运营方提供的 Registry 配置。

```bash
cp config/app/local.example.yaml config/app/local.yaml
# 填写 PostgreSQL、模型、OpenSandbox、Registry 与 Incus 配置。
make build
./bin/breakfix-server -config config/app/local.yaml
```

`make build` 生成三个运行时二进制和嵌入式 Web UI。已部署环境的本地接管使用
`scripts/dev/telepresence.sh`；完整部署与调试步骤见运维文档。本地配置含环境专属地址和密钥，不应提交。

## 验证

```bash
make test-unit
make verify-generated
kubectl kustomize .
# 先在专用 Kind target 上执行 make e2e-prepare，再运行平台验收
```

真实 Kubernetes 与模型验收需要显式启用，见[测试与真实验收](docs/operations/testing.md)。

## 文档与契约

- [文档索引](docs/README.md)
- [代码布局](docs/architecture/code-layout.md)
- [工作流](docs/architecture/workflows.md)
- [Catalog Release](docs/architecture/catalog-release.md)
- [运行环境](docs/architecture/runtime-environments.md)
- [部署与运行](docs/operations/deployment.md)

机器可验证的契约以代码为准：HTTP 接口见 `api/http/openapi.yaml`，CRD 见
`api/v1/`，题目格式见 `internal/content/challenge/`，运行时配置见
`config/app/local.example.yaml`，构建和运维命令见 `Makefile`。
