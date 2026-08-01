# 部署与运行

Breakfix 的控制面由 Server、Controller、Generate Worker、Taxonomy Worker 和 PostgreSQL 组成。生产根
Kustomize 包不部署 Registry；Registry 由运营方提供并通过 runtime Secret 配置。Environment 由 Controller
按需创建，不是固定 Deployment。

## 前置条件

- 可访问的 PostgreSQL。
- 已安装 Breakfix CRD 的 Kubernetes 集群和能执行所需 NetworkPolicy 的 CNI。
- OpenSandbox native Kubernetes provider，用于 Server 管理 Generator workspace。
- 一个 HTTPS OCI Registry；使用私有 Registry 时，每个会拉取镜像的 node 都必须信任其 CA、解析并访问
  配置的稳定域名。
- Node runtime 还需要私网可访问的 Incus cluster 与 role-specific mTLS 证书。

配置字段以 [`config/breakfix.example.yaml`](../../config/breakfix.example.yaml) 为准。PostgreSQL 是唯一
关系数据库；Server data PVC 只保存 candidate archive、已发布 challenge 与 taxonomy snapshot，不是队列。

## Registry

生产部署要求运营方提供一个所有 Kubernetes node 都能解析、访问并信任的 HTTPS OCI Registry。
`registry_addr` 是 image reference 的 repository root，`registry_client_addr` 是 Server 和 Generate Worker
使用的 HTTPS authority；外部 Registry 通常分别填 `registry.example.com/breakfix` 和
`registry.example.com`。同时配置可选的 `registry_pull_secret`、构建推送凭据和内部 CA bundle；Breakfix 不部署或
管理 Registry，也不修改 node DNS、`/etc/hosts`、containerd 或 CA 信任库。Kubernetes CoreDNS 的 `.svc`
名称不能作为 kubelet/containerd 的最终镜像地址。

Kind 开发环境使用 `deploy/overlays/kind` 和 `make dev-kind-registry`。该开发准备步骤将 Registry 暴露为固定
`NodePort 30443`，使用 Kind control-plane 的 Docker 网络 IP 作为镜像 authority，并为该 IP 与 Registry
Service DNS 签发本地开发证书。`registry_addr` 用于 kubelet 拉取，`registry_client_addr` 用于集群内
Server/Generate Worker 访问 Service。CA 变化时开发脚本会刷新 Kind node 信任库并重启其 containerd；运行时
Secret、pull Secret 和 Registry TLS Secret 由同一步骤同步，不需要自定义 DNS、CoreDNS 或 `/etc/hosts`。
NodePort 只属于 Kind 开发环境，生产不使用它。

## 构建与部署

```bash
make verify-crd-generated
make verify-api-generated
make runtime-push TARGETOS=linux TARGETARCH=amd64 \
  RUNTIME_IMAGE_REPOSITORY=ghcr.io/acme/breakfix RUNTIME_IMAGE_TAG=dev
kubectl apply -k .
kubectl -n breakfix-system get deployments,pods
```

`make runtime-push` 只把已编译二进制打入 Server、Controller、Generate Worker 与 Taxonomy Worker 的
distroless image。不要在运行时容器中下载 Go 依赖或编译源码。

开发环境的初始 catalog 是一个完整的 `data/challenges` 与 `data/taxonomy` snapshot。先同步它，再启动
Runtime，避免 Server 在空卷上把已经审核的静态题目误判为待 mapping 的新题：

```bash
make dev-kind-reset-state
make dev-kind-catalog
make dev-kind-registry
make dev-kind-runtime
```

`make dev-kind-runtime` 使用 Kind overlay；生产部署只使用根目录清单，并在部署前准备外部 Registry。

`data/taxonomy/current` 在 Git 中是只读文本指针；Server 后续发布 taxonomy snapshot 时会原子替换为运行时
符号链接。静态 snapshot 中的 mapping 必须精确匹配每个 challenge artifact revision。

本次数据库 schema 是开发阶段的破坏性基线。检测到不匹配 schema 时 Server 会拒绝启动，必须重建开发数据库；
不提供历史数据库的兼容迁移。

## 身份和最小权限

Server 持有 `internal_workers.generate` 和 `internal_workers.taxonomy` 两个独立密钥。Generate Worker
只挂载前者，Taxonomy Worker 只挂载后者；两者都没有 PostgreSQL DSN。Generate Worker 拥有构建、Registry、
验证 Environment 与其 Incus 角色凭据；该 Incus 身份必须能够访问 Controller 为验证和学习动态创建的
NodeEnvironment project，不能只限定为 build/image 两个静态 project。Taxonomy Worker 只需要 Server、模型 API
和临时文件空间。

Controller 是唯一有权限调和 Environment CRD 的组件。Server 创建和更新 Environment `spec`，Controller
写 `status`。生产 CNI 必须真正执行 NetworkPolicy；Kind 的默认网络行为不能当作隔离验收。

## 验收

```bash
go test -count=1 ./...
npm run build --prefix frontend
make e2e
make e2e-runtime-browser
make e2e-server-recovery
```

模型驱动的端到端验收显式运行：`make e2e-agent-node`、`make e2e-agent-k8s` 和
`make e2e-agent-assistant`。Kind 验收要求 Kind overlay、固定 NodePort Registry、OpenSandbox 和
对应环境 provider；完整边界见[测试与真实验收](testing.md)。
