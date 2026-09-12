# 部署与运行

Breakfix 的固定控制面是 Server、Controller、Runtime Worker 和 PostgreSQL。Catalog 安装器在 Server 进程内运行；Environment 由 Controller 按需创建，不是固定 Deployment。生产根 Kustomize 包不部署 Registry，Registry 由运营方提供并通过 runtime Secret 配置。

## 前置条件

- 可访问的 PostgreSQL。
- 已安装 Breakfix CRD 的 Kubernetes 集群和能执行所需 NetworkPolicy 的 CNI。`runtime.k8s.network.protected_cidrs`
  必须显式覆盖该集群的 Service/Pod CIDR、平台私网和 metadata/link-local 地址。
- OpenSandbox native Kubernetes provider，用于 Server 管理 Generator workspace。
- 一个 HTTPS OCI Registry；使用私有 Registry 时，每个会拉取镜像的 node 都必须信任其 CA、解析并访问配置的稳定域名。
- Node runtime 还需要私网可访问的 Incus cluster 与 role-specific mTLS 证书。

配置字段以 [`config/app/local.example.yaml`](../../config/app/local.example.yaml) 和 [`config/app/in-cluster.yaml`](../../config/app/in-cluster.yaml) 为准。PostgreSQL 是唯一关系数据库；Server data PVC 保存持久化 Catalog source、已 materialize scenario 与可恢复的 artifact 文件，不是队列。

## Registry

生产部署要求运营方提供一个所有 Kubernetes node 与平台 Pod 都能解析、访问并信任的 HTTPS OCI Registry。运行时 Secret 的 `registry_repository` 是 image reference 的 repository root，例如 `registry.example.com/breakfix`；其 authority 由 kubelet、Server 和 Runtime Worker 原样共享。同步配置可选的 `registry_pull_secret`、构建推送凭据和内部 CA bundle；Breakfix 不部署或管理 Registry，也不修改 node DNS、`/etc/hosts`、containerd 或 CA 信任库。Kubernetes CoreDNS 的 `.svc` 名称不能作为 kubelet/containerd 的最终镜像 authority。

Kind Registry 的 NodePort、开发 CA 和镜像加载流程属于[本地开发](development.md)，生产不使用它。

## 构建与部署

```bash
make verify-generated
make images TARGETOS=linux TARGETARCH=amd64 \
  RUNTIME_IMAGE_REPOSITORY=ghcr.io/acme/breakfix RUNTIME_IMAGE_TAG=dev
for component in server controller runtime-worker; do
  docker push "ghcr.io/acme/breakfix-${component}:dev"
done
kubectl apply -k .
kubectl -n breakfix-system get deployments,pods
```

`make images` 只把已编译二进制打入 Server、Controller 与 Runtime Worker 的 distroless image，并构建 Kind 使用的 K8s base image。推送仍由部署者显式执行；不要在运行时容器中下载 Go 依赖或编译源码。

## Catalog 基线

Catalog Release source 是 Git 管理的 portable source，不是 Server data directory。先打包并推送内容，得到 immutable digest：

```bash
make catalog-package \
  CATALOG_SOURCE=/path/to/foundation-catalog \
  CATALOG_ARCHIVE=dist/foundation.oci.tar \
  CATALOG_REFERENCE=registry.example.com/breakfix/catalog/foundation:2026.08.01
# 输出 registry.example.com/breakfix/catalog/foundation@sha256:...
```

将这个 digest 写入 runtime Secret 的 `catalog_release_reference`，应用 Secret 并 rollout Server。Server 在启动时创建或恢复首次 CatalogRelease；没有管理员安装 API，也不允许从 Server data PVC 或 Git 工作区直接复制运维场景。尚未建立 Catalog 时，空字符串表示刻意启动空 Catalog。

同一 digest 可幂等恢复。Ready baseline 不接受另一个 release；后续内容通过作者工作流发布。失败的首次 release 保留诊断且不会公开部分 Catalog；切换 digest 前必须完成旧 release 的外部资源回收。完整安装状态和原子可见性见 [Catalog Release](../architecture/catalog-release.md)。Kind 开发时可为 `catalog-package` 提供 `CATALOG_TRUST_BUNDLE_FILE=.local/kind-registry/ca.crt`。

本次数据库 schema 是开发阶段的破坏性基线。检测到不匹配 schema 时 Server 会拒绝启动，必须重建开发数据库；不提供历史数据库的兼容迁移。

## 身份和最小权限

Server 持有 `internal_workers.runtime` 密钥；Runtime Worker 只挂载该密钥，不持有 PostgreSQL DSN 或模型凭据。Runtime Worker 拥有构建、Registry、验证 Environment 与其 Incus 角色凭据；该 Incus 身份必须能够访问 Controller 为验证和学习动态创建的 NodeEnvironment project，不能只限定为 build/image 两个静态 project。

Controller 是唯一有权限调和 Environment CRD 的组件。Server 创建和更新 Environment `spec`，Controller 写 `status`。生产 CNI 必须真正执行 NetworkPolicy；Kind 的默认网络行为不能当作隔离验收。

Runtime Worker 的 `/healthz` 与 `/readyz` 只表示进程和 action loop 可用，不能因一个 provider 故障而让它停止领取其他 runtime 的 action。Registry、Kubernetes API 和 Incus 分别通过 `/capabilities/registry`、`/capabilities/kubernetes-api`、`/capabilities/node-provider` 暴露独立探针，并同时写入 capability metrics，供部署者告警和排障。

## Agent-native authoring 客户端

网页 Authoring Agent 不需要额外部署：它随 Server 运行，并直接调用共享的 `GeneratorService` function tools。外部 Agent 使用
部署在用户机器上的 `breakfix-mcp`，它同时是标准 stdio MCP Server 和远程 Breakfix 的认证客户端：

```yaml
server_url: https://breakfix.example.com/api
token_env: BREAKFIX_MCP_TOKEN
```

连接器从本地配置读取 Server URL 与用户 Token 环境变量，通过 HTTPS 调用与网页相同的 Generator application API；远程连接必须
是 HTTPS，Server 在每次 workspace、workflow、审核和发布操作中校验用户所有权。Token 只存在于进程环境，不进入 Agent 对话、
配置、日志、审核目录或 candidate。连接器把 Server 返回的不可变审核包投影到系统临时目录（默认
`/tmp/breakfix/reviews/<workflow-id>/`），该目录是可丢弃的只读缓存：删除或重启后可从 Server 重新同步，用户在其中编辑文件
不会改变 Server 状态。远程 Server 永远不写调用机器的临时目录；本地审核目录也不会上传回 Server。

## Incus

Node runtime 的基础镜像和 role-specific mTLS 身份由 `scripts/incus/bootstrap.sh` 准备。脚本将证书写入被忽略的 `.local/incus/<role>/`，并输出 `base_image_fingerprint`；部署者再将 `server`、`controller` 和 `runtime` 三套证书创建为 `breakfix-incus-*` Secret，并把该 fingerprint 写入 `breakfix-runtime` 的 `incus_base_image_fingerprint`。

## 验收

```bash
make test-unit
make lint
make build
make verify-generated
kubectl kustomize .
kubectl kustomize deploy/overlays/kind
```

Kind 平台验收必须先在专用 `kind-breakfix-e2e` 目标执行 `make e2e-prepare`，再分别执行 `make test-e2e`、`make test-e2e-node` 和 `make test-e2e-recovery`；完成后用 `make e2e-reset` 丢弃目标。它们使用动态 port-forward，不使用固定的 `localhost:9090`。真实模型验收通过 `RUN_AGENT_LIVE_E2E=1 make test-acceptance-node`、`RUN_AGENT_LIVE_E2E=1 make test-acceptance-mcp` 或测试文档中的其他独立 Live 入口运行。完整边界见[测试与真实验收](testing.md)。
