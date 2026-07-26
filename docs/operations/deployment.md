# 部署与运行

本项目运行三个独立二进制：`breakfix-server`、`breakfix-controller` 和 `breakfix-agent-worker`。它们职责和运行过程独立；运行时使用 PostgreSQL，不支持 SQLite 回退。配置键与默认值以 [`config/breakfix.example.yaml`](../../config/breakfix.example.yaml) 为准；构建、开发和远程部署命令以 [`Makefile`](../../Makefile) 为准。

## 本地开发

前置条件：Docker、Kind、kubectl、Go、Node.js、PostgreSQL，以及可用的 `vcluster` CLI。需要生成题目时，还需要已安装的 OpenSandbox native Kubernetes provider 和其 lifecycle key。默认 Kind 集群名为 `breakfix-dev`。

```bash
cp config/breakfix.example.yaml config/breakfix.yaml
make dev
```

在运行 `make dev` 前，为配置中的 `database_url` 和 `agent_database_url` 提供可访问的 PostgreSQL DSN。`make dev` 会准备本地 registry、数据目录、CRD、RBAC 和题目镜像，构建三个二进制并依次启动 Controller、Server 与 Agent Worker。Web UI 位于 `http://localhost:9090`；Controller 健康检查位于 `http://localhost:8081/healthz`。

常用迭代命令：

```bash
make dev-server       # 构建并重启 Server
make dev-controller   # 构建并重启 Controller
make dev-agent-worker # 构建并重启 Agent Worker
make dev-down         # 停止三个进程和本地 registry
make dev-reset        # 清理本地运行数据，保留受版本控制题目
make docker-challenge NAME=<directory>
```

不要提交 `config/breakfix.yaml` 或 `config/breakfix.local.yaml`。它们包含环境地址、密钥和本地路径；仓库只跟踪示例配置。

## 集群准备

Controller 需要 CRD、RBAC 和能够创建 namespace、Pod、Job、Secret 与 vcluster 资源的 Kubernetes 凭据。开发环境可运行：

```bash
make generate-crd
make dev-crd
make dev-rbac
```

生产环境应从 [`deploy/crd/`](../../deploy/crd/) 和 [`deploy/rbac/controller.yaml`](../../deploy/rbac/controller.yaml) 应用同样的资源。`deploy/crd/` 是由 Go 类型生成并受 CI 校验的部署契约，不能手改。

## 主机进程

Server 需要可写的 `data_dir`，其中保存证书材料、发布题目、作者 artifact 和提交归档。PostgreSQL 保存账户、领域状态与 durable Agent Runtime 记录。Controller 只需读取配置和 kubeconfig，不需要也不应拥有 Server 数据目录或 PostgreSQL 凭据；Agent Worker 只获得 `agent_*` 数据库角色、模型 key 和 Server 内部密钥。

在 systemd 中分别运行两个服务，并为它们传递同一个运行时配置路径：

```ini
# breakfix-server.service
ExecStart=/usr/local/bin/breakfix-server -config /var/lib/breakfix/breakfix.yaml

# breakfix-controller.service
ExecStart=/usr/local/bin/breakfix-controller -config /var/lib/breakfix/breakfix.yaml

# breakfix-agent-worker.service
ExecStart=/usr/local/bin/breakfix-agent-worker -config /var/lib/breakfix/breakfix.yaml
```

为三个服务设置重启策略，并确保只有 Controller 运行用户可以读取 kubeconfig。Server 对外暴露 HTTP/WebSocket；Controller 只暴露配置的 health/ready 端口，不应作为公网入口；Agent Worker 不暴露公网端口。

## 构建与远程更新

`make build` 输出 Linux Server 和 Controller 二进制到 `dist/`。Agent Worker 的容器镜像由 [`deploy/images/agent-worker/Dockerfile`](../../deploy/images/agent-worker/Dockerfile) 构建；完整集群安装使用 [`deploy/runtime/`](../../deploy/runtime/)。远程更新依赖本地 `.breakfix-server` 或 `SERVER=<host>`：

```bash
make deploy-server
make deploy-controller
make deploy-catalog
make deploy-images
make deploy
```

`deploy-catalog` 将本地 `data/challenges` 原子替换到远端 `data_dir/challenges`。`deploy-images` 推送基础镜像和发布题镜像；registry 地址和是否使用不安全 registry 从配置读取。更新题目目录和镜像时应保持二者版本一致。

## 运行检查

```bash
make status
make logs
curl -fsS http://localhost:9090/api/openapi.json
curl -fsS http://localhost:8081/healthz
```

部署后至少验证注册、登录、container 题终端、vcluster 题环境和检查点自动完成。真实恢复覆盖可通过 `make e2e-server-recovery` 运行；完整命令和开关位于 [`test/`](../../test/) 项目中。

## 生成与发布前检查

```bash
make verify-crd-generated
make verify-api-generated
go test ./...
npm run build --prefix frontend
npm run test:e2e --prefix test -- --list
```

这些检查分别覆盖 CRD 生成物、OpenAPI 前端类型、Go 包、前端构建和 Playwright 发现。真实环境 E2E 需要显式环境变量，避免普通浏览器套件意外创建集群资源。
