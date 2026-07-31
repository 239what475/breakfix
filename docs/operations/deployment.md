# 部署与运行

Breakfix 的控制面由 Server、Controller、Agent Worker、Builder Worker、Publisher Worker、Verifier Worker 和 PostgreSQL 组成。前六者都是固定容量 Deployment；PostgreSQL 是 StatefulSet。根部署包默认额外部署单副本 OCI Registry 与独立 PVC，也可改用运营方已有的 Registry。PostgreSQL 是唯一关系数据库，不支持 SQLite 回退。

Server 是 CandidateRevision、WorkItem 和领域状态的唯一写者。所有 Worker 都通过 Server 内部 API 领取和提交带 lease fence 的 WorkItem，不持有 PostgreSQL 凭据。Controller 只调和 `NodeEnvironment` 和 `VK8sEnvironment` CRD，不参与候选工作流状态机。

配置键以 [`config/breakfix.example.yaml`](../../config/breakfix.example.yaml) 为准；构建、开发和集群部署命令以 [`Makefile`](../../Makefile) 为准。

## 本地开发

前置条件：Docker、Kind、kubectl、Go、Node.js、PostgreSQL 和 `vcluster` CLI。生成题目还需要 OpenSandbox native Kubernetes provider 及其 lifecycle key、模型 API key。默认 Kind cluster 名是 `breakfix-dev`。

```bash
cp config/breakfix.example.yaml config/breakfix.yaml
# 填写 PostgreSQL、模型、OpenSandbox 与 Registry 连接信息。
make dev
```

`make dev` 会准备题库目录、CRD/RBAC 和 K8s base image，然后构建并启动 Server、Controller 和四类固定 Worker。运行 VK8s 前，先准备配置的 HTTPS OCI Registry；若它使用内部 CA，集群节点必须已经信任该 CA。Web UI 位于 `http://localhost:9090`；`/readyz` 只反映进程的核心可服务状态，`/healthz` 只反映进程存活。运行时依赖通过 capability endpoint 单独观察，避免一个可选 provider 让无关运行时失去服务。

Node runtime 依赖独立的 Incus provider，不会由 `make dev` 隐式创建或修改。先按 [运行环境](../architecture/runtime-environments.md) 准备 Incus 7.0.1，然后显式执行：

```bash
make dev-incus
make dev-incus-catalog
make dev-incus-secrets
```

这会创建平台固定 build/image Project、受信任且不带过期时间的 `node-systemd-base`、各角色的 mTLS Secret，并把提交的 `cleanup-logs` 以候选 bundle 的正式 Build/ArtifactPublish/ChallengePublish 语义发布为 immutable Incus image。`make dev-kind-catalog` 依赖这一步，因而不会只复制题目目录而遗漏镜像。运行时二进制不会读取开发机的 Incus CLI remote 或 `~/.config/incus`。Incus 不可用时，K8s 路径与不需要 Incus 的固定 Worker 仍可运行；新的 NodeEnvironment 会报告可重试的 Provider 基础设施失败。Server 与 Node-capable Worker 的 `/capabilities/node-provider` 可用于直接检查角色 mTLS 与 Incus 预检；Publisher 还提供 `/capabilities/registry`，Verifier 提供 `/capabilities/kubernetes-api`。

Bootstrap 的固定 Builder bridge 默认使用 `10.248.25.1/24`，可通过 `BREAKFIX_INCUS_BUILD_NETWORK_CIDR` 变更；它必须与 `incus.node_network_pool` 分离。脚本不会再让 Incus 自动选择子网，并会拒绝接管地址、NAT 或 IPv6 配置不同的同名 bridge。

当前重构不迁移旧开发状态。若 Kind 的 PostgreSQL 或 Server data PVC 来自旧 schema/layout，先执行 `make dev-kind-reset-state`，再执行 `make dev-kind-runtime` 和 `make dev-kind-catalog`。该命令默认只允许 `kind-*` context，避免误清理非开发集群。

常用迭代命令：

```bash
make dev-server
make dev-controller
make dev-agent-worker
make dev-builder
make dev-publisher
make dev-verifier
make dev-down
make dev-reset
```

不要提交 `config/breakfix.yaml`、`config/breakfix.local.yaml`、`.local/` 或任何 Secret。它们包含环境地址、密钥、证书或本地路径；仓库只跟踪安全示例。

需要以本地代码接管已部署集群中的 Server、Controller 或任一 Worker 时，使用[Telepresence 本地调试](telepresence.md)。同一个角色不能同时运行本地开发进程和 Telepresence replacement。

## 集群准备

应用 CRD 与 RBAC 前先验证生成物：

```bash
make verify-crd-generated
make verify-api-generated
kubectl kustomize .
```

根目录 [`kustomization.yaml`](../../kustomization.yaml) 是集群安装入口，CRD 来自 `deploy/crd/`，不能手改。Controller 需要管理 Environment CRD、vcluster 所需的 Kubernetes 资源及 status/finalizer；Verifier 只拥有创建/读取/删除 Environment 和必要 exec 的最小 RBAC。Builder、Publisher 和 Agent Worker 不自动挂载 ServiceAccount token。

根目录 [`kustomization.yaml`](../../kustomization.yaml) 默认部署内置 Registry。运营方为它选择仅内网可解析的稳定名称，例如 `registry.breakfix.internal`，并将该名称解析到私有 LoadBalancer。所有 Kubernetes node 必须能解析并访问该地址；不能把 `*.svc` 或 ClusterIP 作为最终镜像引用，因为 kubelet/containerd 运行在节点上，不使用 Pod 的 CoreDNS。

内置 Registry 使用管理员提供的 `breakfix-registry-tls` TLS Secret 和 `breakfix-registry-auth` 认证 Secret。证书由管理员持有的内部 CA 签发，根 CA 必须在所有 Kubernetes node 的镜像运行时信任库中预装。若 `registry.trust_bundle_file` 非空，管理员还需创建包含 `ca.crt` 的 `breakfix-registry-ca` ConfigMap，Server 和 Publisher 会将它追加到自身系统信任链。Breakfix 不生成或轮换根 CA，不安装 cert-manager，不申请公网证书，也不会修改节点 DNS、`/etc/hosts` 或 containerd 配置。

也可以使用 [`deploy/overlays/external-registry`](../../deploy/overlays/external-registry/) 接入 Harbor、云厂商 Registry 或其他 HTTPS OCI Registry；该 overlay 不创建 Registry、Service 或 PVC。配置 `registry.address`、可选 `registry.pull_secret`、可选内部 CA bundle 和 Publisher 凭据即可。无论使用哪种模式，Registry 必须允许 Docker Registry V2 manifest DELETE，以便 `artifact_cleanup` 回收 candidate staging 引用；blob garbage collection 只应在 Registry 离线维护窗口运行。

Node runtime 需要独立的 Incus cluster。应用与 Incus API 位于同一私网，API 使用 mTLS；Server、Controller、Builder、Publisher 和 Verifier 分别挂载自己的证书，证书不进入候选 archive、OpenSandbox Sandbox 或用户/验证 Environment。初始实现只支持单成员 bridge 网络；多成员与 OVN 是后续单独验收的扩展。

## 集群部署

完整 Secret、Registry TLS/认证、Incus bootstrap、固定 Worker identity 和 Kustomize 步骤见 [`deploy/runtime/README.md`](../../deploy/runtime/README.md)。构建和发布运行时镜像：

```bash
make runtime-push TARGETOS=linux TARGETARCH=amd64 \
  RUNTIME_IMAGE_REPOSITORY=ghcr.io/acme/breakfix RUNTIME_IMAGE_TAG=dev
kubectl apply -k .
```

发布版本不要手改基础清单。`make release-manifest` 推送六个运行时 image、解析 digest，并生成可部署的 `breakfix-<version>.yaml`：

```bash
make release-manifest TARGETOS=linux TARGETARCH=amd64 \
  RUNTIME_IMAGE_REPOSITORY=ghcr.io/acme/breakfix RUNTIME_IMAGE_TAG=vX.Y.Z
kubectl apply -f dist/breakfix-vX.Y.Z.yaml
```

Server data PVC 保存未发布 CandidateRevision archive、已发布 challenge 目录和 taxonomy snapshot；它不是队列或数据库。PostgreSQL 保存领域记录与 WorkItem。内置 Registry PVC 保存 K8s candidate/正式 OCI image；使用外部 Registry 时其存储由运营方管理。Incus image project 保存 Node candidate/正式 image。当前 Server data PVC 是 RWO，因此 Server 仍是单副本；这不改变 PostgreSQL worklist 的跨 Worker 接管语义。

## 验收

日常测试与真实运行时验收分层，不能用浏览器 stub 或 fake provider 代替真实 Node/VK8s 验证：

```bash
go test ./...
make e2e
make e2e-runtime-browser
make e2e-runtime-workflow
make e2e-server-recovery
```

模型驱动的作者验收显式、串行运行：`make e2e-agent-node`、`make e2e-agent-k8s` 和 `make e2e-taxonomy`。失败时按 WorkItem ID、kind、attempt 和 Environment UID 查看固定 Worker Deployment 日志；不要自动重跑掩盖问题。完整边界见[测试与真实验收](testing.md)。
