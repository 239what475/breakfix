# 部署与运行

本项目运行 Server、Controller、Agent Worker、PostgreSQL 与 OCI Registry 五个核心组件。前三者是独立 Deployment，PostgreSQL 使用 `StatefulSet`，Registry 使用单副本 `Deployment` 和独立 PVC；运行时使用 PostgreSQL，不支持 SQLite 回退。配置键以 [`config/breakfix.example.yaml`](../../config/breakfix.example.yaml) 为准；构建、开发和集群部署命令以 [`Makefile`](../../Makefile) 为准。

## 本地开发

前置条件：Docker、Kind、kubectl、Go、Node.js、PostgreSQL，以及可用的 `vcluster` CLI。需要生成题目时，还需要已安装的 OpenSandbox native Kubernetes provider 和其 lifecycle key。Breakfix 使用 OpenSandbox 的 `ManualCleanup` Sandbox：每个 Generator Run 由 Server 显式删除 Sandbox 及其 BYO PVC，不依赖 provider 的工作区超时。验证 Registry 必须支持 Docker Registry V2 的 manifest DELETE；失败的 `VerifyTask` 必须删除临时镜像，不能将该清理错误忽略为成功。默认 Kind 集群名为 `breakfix-dev`。

```bash
cp config/breakfix.example.yaml config/breakfix.yaml
make dev
```

每个进程都必须显式读取一个配置文件；缺失配置文件或缺失该进程需要的配置会直接退出，绝不会补入开发默认值。复制示例后，为 `database_url` 和 `agent_database_url` 提供可访问的 PostgreSQL DSN，并设置模型和 OpenSandbox 所需密钥。`ui_origin` 必须是浏览器实际访问 UI 的单个完整 `http`/`https` origin，例如 `http://localhost:9090`；终端 WebSocket 只接受这个 Origin。

`make dev` 会准备本地匿名 Registry、空的 Docker pull Secret、数据目录、CRD、RBAC、题目镜像和验证 Job 镜像，构建三个常驻二进制并依次启动 Controller、Server 与 Agent Worker。Web UI 位于 `http://localhost:9090`；Server 的 `/readyz` 同时校验整个题库，`/healthz` 只用于存活探测；Controller 健康检查位于 `http://localhost:8081/healthz`。

常用迭代命令：

```bash
make dev-server       # 构建并重启 Server
make dev-controller   # 构建并重启 Controller
make dev-agent-worker # 构建并重启 Agent Worker
make dev-down         # 停止三个进程和本地 registry
make dev-reset        # 清理本地运行数据，保留受版本控制题目
make docker-challenge NAME=<directory>
```

`make dev-registry` 创建的本地 `registry:2` 会设置 `REGISTRY_STORAGE_DELETE_ENABLED=true`。若已有旧 registry 未启用该选项，命令会保留其数据卷并重建容器；不要以关闭 manifest DELETE 的 registry 运行 verifier。

不要提交 `config/breakfix.yaml` 或 `config/breakfix.local.yaml`。它们包含环境地址、密钥和本地路径；仓库只跟踪示例配置。

需要以本地代码接管已部署集群的 Server、Controller 或 Agent Worker 时，使用
[Telepresence 本地调试](telepresence.md)，不要混用 `make dev-*` 与同一集群运行时。

## 集群准备

Controller 需要 CRD、RBAC 和能够创建 namespace、Pod、Job、Secret 与 vcluster 资源的 Kubernetes 凭据。开发环境可运行：

```bash
make generate-crd
make dev-crd
make dev-rbac
```

生产环境应从 [`deploy/crd/`](../../deploy/crd/) 和 [`deploy/rbac/verifier.yaml`](../../deploy/rbac/verifier.yaml) 应用同样的资源。`deploy/crd/` 是由 Go 类型生成并受 CI 校验的部署契约，不能手改。

## 集群部署

Server 需要自己的 RWO PVC，保存发布题目、作者 artifact 和提交归档。PostgreSQL 保存账户、领域状态与 durable Agent Runtime 记录。Controller 没有 PostgreSQL 凭据；Agent Worker 只有 `agent_*` 数据库角色、模型 key 和 Server 内部密钥。Server 是唯一持有 OpenSandbox lifecycle key 的组件。

Builder 节点必须满足官方 rootless BuildKit 的 user namespace、`fuse-overlayfs`/overlayfs 和 AppArmor 前置条件。不能满足时，Build Job 应失败并报告基础设施错误；部署不提供 rootful 或 `privileged` 构建回退。

完整安装步骤、Registry TLS/认证 Secret、OpenSandbox 前置条件和 Kustomize 入口见 [`deploy/runtime/README.md`](../../deploy/runtime/README.md)。控制面与验证 Job 镜像使用 `builder_image`、`publisher_image` 和 `verifier_image` 配置；Builder 镜像必须由 kubelet 无凭据拉取。开发镜像可以使用 `:dev`：

```bash
make runtime-push TARGETOS=linux TARGETARCH=amd64 \
  RUNTIME_IMAGE_REPOSITORY=ghcr.io/acme/breakfix RUNTIME_IMAGE_TAG=dev
kubectl apply -k .
```

发布时不要手改基础 Kustomize 清单。release workflow 会构建并推送 Server、Controller、Agent Worker、Builder、Publisher 和 Verifier 六个 OCI image，解析每个 digest，并上传 `breakfix-<version>.yaml`。该 artifact 已将 Deployment 和 ConfigMap 中的所有运行镜像固定为 digest；部署发布版本时直接应用它：

```bash
kubectl apply -f breakfix-vX.Y.Z.yaml
```

Controller 为每个 `VerifyTask` 创建依次执行的 Build、Publisher 和 Verifier Job。Server 从 `registry_addr` 读取固定基础镜像并通过一次性 grant 交给无凭据 Builder；Publisher 才拥有 Registry 写 Secret，Controller 从其 staging tag 读取最终 digest 后启动 Verifier。作者确认发布时，Server 再将该 digest 复制到正式 challenge image 并重新解析 digest；Builder 从不直接访问 Registry。Server 对外暴露 HTTP/WebSocket；Controller 只暴露 health/ready 端口，不作为公网入口；Agent Worker 不暴露端口。

## 运行检查

```bash
kubectl -n breakfix-system get deploy,statefulset,pods
kubectl -n breakfix-system logs deploy/breakfix-server -f
kubectl -n breakfix-system logs deploy/breakfix-controller -f
```

部署后至少运行 `make e2e` 验证页面与认证流程。真实运行时、恢复和 Agent 验收分开显式执行：`make e2e-runtime-verify` 验证固定 container/vcluster artifact 的完整 VerifyTask，`make e2e-runtime-browser` 验证固定题目的终端和检查点，`make e2e-server-recovery` 验证恢复行为。模型相关的 `make e2e-agent-assistant`、`make e2e-agent-container` 和 `make e2e-agent-vcluster` 只用于人工或发布前验收，不是日常 CI。完整策略见[测试与真实验收](testing.md)。

## 生成与发布前检查

```bash
make verify-crd-generated
make verify-api-generated
go test ./...
npm run build --prefix frontend
npm run test:e2e --prefix test -- --list
```

这些检查分别覆盖 CRD 生成物、OpenAPI 的 Go/前端生成物、Go 包、前端构建和 Playwright 发现。真实环境 E2E 需要显式环境变量，避免普通浏览器套件意外创建集群资源。
