# Breakfix Runtime Deployment

`deploy/runtime` 安装 PostgreSQL、Server、Controller、Generate Worker 和 Taxonomy Worker。仓库根目录
只安装这些控制面资源，生产环境必须通过 runtime Secret 配置运营方提供的 Harbor、云厂商 Registry 或其他
HTTPS OCI Registry。Kind 开发环境额外使用 `deploy/overlays/kind`，其中包含固定 NodePort Registry。

后台流程只使用两个 PostgreSQL 聚合：

```text
GenerationWorkflow: generate -> judge -> build -> artifact publish -> verify
                    -> author review -> challenge publish -> cleanup

TaxonomyWorkflow:   map -> reviewer pair -> taxonomy snapshot publish
```

Controller 只调和 `NodeEnvironment` 和 `VK8sEnvironment`，不拥有 CandidateRevision 或 Worker lease。

## 构建运行时镜像

先在容器外编译，再将对应二进制放入 distroless image。Docker build context 不包含 Go 源码、模块缓存、
Node 依赖或宿主网络回退逻辑。

```bash
make runtime-push TARGETOS=linux TARGETARCH=amd64 \
  RUNTIME_IMAGE_REPOSITORY=ghcr.io/breakfix RUNTIME_IMAGE_TAG=dev
```

该命令构建并推送 Server、Controller、Generate Worker 与 Taxonomy Worker。K8s management terminal
base image 由 `make k8s-base-image` 推送到配置的 OCI Registry；Node system-container base image 由
`make dev-incus` 在 Incus image project 中准备。

## Secret 与 Worker 身份

在应用清单前创建 runtime Secret。示例文件必须复制到仓库外后填写真实值：

```bash
cp deploy/runtime/runtime-secret.example.env /secure/path/breakfix-runtime.env
kubectl apply -f deploy/runtime/namespace.yaml
kubectl -n breakfix-system create secret generic breakfix-runtime \
  --from-env-file=/secure/path/breakfix-runtime.env
```

为两个 Worker 建立独立 identity Secret。Server 挂载两个密钥以认证内部 API；每个 Worker 只挂载自己的
密钥。

```bash
for role in generate-worker taxonomy-worker; do
  kubectl -n breakfix-system create secret generic "breakfix-${role}-identity" \
    --from-env-file=/secure/path/"${role}"-identity.env
done
```

identity 文件仅包含 `worker_api_key=<long-random-value>`。两个值必须不同，且不应与 JWT 或 Registry
密码复用。

## Registry

生产环境不部署 Breakfix 自带 Registry。运营方负责 Registry 的存储、TLS、认证、可用性和节点可达性。
`registry_addr` 是写入 immutable OCI image reference 的 repository root，必须是每个 Kubernetes node 都能
解析和访问的 HTTPS endpoint；`registry_client_addr` 是 Server 与 Generate Worker 进行 OCI HTTP 调用的
HTTPS authority。生产环境通常将后者设为前者的 authority，例如分别为
`registry.example.com/breakfix` 与 `registry.example.com`。Registry 必须支持 immutable digest pull、push 和
candidate manifest delete。

私有 Registry 的 CA 必须在集群初始化时安装到每个 node 的镜像运行时；Breakfix 不修改 node DNS、
`/etc/hosts`、containerd 或 CA 信任库。`breakfix-registry-pull` 是标准 Kubernetes image-pull Secret，
只在 Registry 需要认证时提供。

```bash
kubectl -n breakfix-system create secret docker-registry breakfix-registry-pull \
  --docker-server=registry.example.com \
  --docker-username=breakfix \
  --docker-password=replace-with-registry-password
```

Kind 开发环境才应用以下 overlay：

```bash
kubectl apply -k deploy/overlays/kind
```

该 overlay 的 Registry Service 明确使用固定 `NodePort 30443`。Kind runtime Secret 中的
`registry_addr` 写成 `<Kind 节点可达地址>:30443/breakfix`，供 kubelet 拉取镜像；
`registry_client_addr` 由开发脚本写成 `breakfix-registry.breakfix-system.svc.cluster.local`，供集群内
Server 和 Generate Worker 访问同一 Registry Service。开发证书同时覆盖 NodePort IP 和该 Service DNS，
Docker config Secret 的 auth key 仍必须匹配 NodePort authority。脚本不会修改 CoreDNS、`/etc/hosts` 或
containerd；它只显式把开发 CA 安装到 disposable Kind node 的系统信任库。这不是生产部署方式。

## Incus

Node runtime 需要可访问的 Incus cluster。运行时应用使用 role-specific mTLS 文件，绝不挂载开发者 CLI
证书或 `~/.config/incus`：

```bash
make dev-incus
make dev-incus-secrets
```

第二个命令创建 Server、Controller 与 Generate Worker 的 Incus Secret，并将 base image fingerprint 写入
`breakfix-runtime`。Taxonomy Worker 不访问 Incus。Bootstrap 只建立 provider baseline；运行时的 Incus
操作使用 Go SDK。

## Apply 与检查

```bash
make verify-crd-generated
make verify-api-generated
kubectl kustomize .
kubectl apply -k .
kubectl -n breakfix-system get deployments,pods
```

这是开发阶段的破坏性 schema 基线：不会导入旧数据库或旧 data layout。要重建 disposable Kind 基线，
先运行 `make dev-kind-reset-state`，再运行 `make dev-kind-catalog` 和 `make dev-kind-runtime`。catalog 同步先创建
并填充 Server data PVC，确保 Server 启动时同时看到 challenge 与精确绑定的 taxonomy snapshot。重置脚本不会
偷偷安装 insecure registry、node DNS/hosts、containerd 配置或镜像 preload fallback；`make dev-kind-registry`
会显式安装其开发 CA。

Server data 和 PostgreSQL 各自使用独立 PVC。Kind overlay 的 Registry 另有独立 PVC。Server data PVC 为
RWO，因此 Server 使用单副本 `Recreate` 策略。Generate Worker 的副本数就是同时运行生成、构建或真实验证的最大任务数；
Taxonomy Worker 可独立扩缩容。

## 运行观察

`/readyz` 只表示进程能服务。动态依赖作为 capability endpoint 暴露：Generate Worker 检查 Registry、
Kubernetes API 和 Node provider；Server 持有 OpenSandbox 与 catalog 能力；Taxonomy Worker 只依赖 Server
和模型配置。一个 provider 暂时不可用不会让无关组件误报不就绪。

排障时按 `GenerationWorkflow` 或 `TaxonomyWorkflow` ID、state、state attempt、AgentRun、
CandidateRevision 与 Environment UID 关联日志。生成、构建、artifact 发布、验证与 cleanup 都由
Generate Worker 完成。
