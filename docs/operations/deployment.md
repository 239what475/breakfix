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

配置字段以 [`config/app/local.example.yaml`](../../config/app/local.example.yaml) 和
[`config/app/in-cluster.yaml`](../../config/app/in-cluster.yaml) 为准。PostgreSQL 是唯一
关系数据库；Server data PVC 只保存 candidate archive、已发布 challenge 与 taxonomy snapshot，不是队列。

## Registry

生产部署要求运营方提供一个所有 Kubernetes node 与平台 Pod 都能解析、访问并信任的 HTTPS OCI
Registry。运行时 Secret 的 `registry_repository` 是 image reference 的 repository root，例如
`registry.example.com/breakfix`；其 authority 由 kubelet、Server 和 Generate Worker 原样共享。同步配置可选的
`registry_pull_secret`、构建推送凭据和内部 CA bundle；Breakfix 不部署或管理 Registry，也不修改 node DNS、
`/etc/hosts`、containerd 或 CA 信任库。Kubernetes CoreDNS 的 `.svc` 名称不能作为
kubelet/containerd 的最终镜像 authority。

Kind Registry 的 NodePort、开发 CA 和镜像加载流程属于[本地开发](development.md)，生产不使用它。

## 构建与部署

```bash
make verify-generated
make images TARGETOS=linux TARGETARCH=amd64 \
  RUNTIME_IMAGE_REPOSITORY=ghcr.io/acme/breakfix RUNTIME_IMAGE_TAG=dev
for component in server controller generate-worker taxonomy-worker; do
  docker push "ghcr.io/acme/breakfix-${component}:dev"
done
kubectl apply -k .
kubectl -n breakfix-system get deployments,pods
```

`make images` 只把已编译二进制打入 Server、Controller、Generate Worker 与 Taxonomy Worker 的
distroless image，并构建 Kind 使用的 K8s base image。推送仍由部署者显式执行；不要在运行时容器中下载 Go
依赖或编译源码。

Catalog Release source 是 Git 管理的 portable source，不是 Server data directory。代码仓库不附带样例题库；真实基础题库
应在内容仓库或随其发布的 `catalog/` 目录中维护。平台基线就绪后，管理员显式指定 source，将其打包为 OCI artifact，并通过
Server 安装一个 digest 固定的 Catalog Release：

```bash
make catalog-package \
  CATALOG_SOURCE=/path/to/foundation-catalog \
  CATALOG_ARCHIVE=dist/foundation.oci.tar \
  CATALOG_REFERENCE=registry.example.com/breakfix/catalog/foundation:2026.08.01
# 输出 registry.example.com/breakfix/catalog/foundation@sha256:...

make catalog-install \
  CATALOG_SERVER_URL=https://breakfix.example.com \
  CATALOG_BUNDLE=registry.example.com/breakfix/catalog/foundation@sha256:... \
  CATALOG_ADMIN_TOKEN="$BREAKFIX_CATALOG_ADMIN_TOKEN"
```

`make catalog-package` 只打包和显式推送 OCI artifact，绝不访问 Server data、Incus 或 Kubernetes。`make
catalog-install` 只调用 Server 管理员 API；它绝不复制 data PVC 或直接发布 image。安装状态、失败语义与原子可见性见
[Catalog Release](../architecture/catalog-release.md)。Kind 开发时可为 `catalog-package` 提供
`CATALOG_TRUST_BUNDLE_FILE=.local/kind-registry/ca.crt`。

`data_dir/challenges` 与 `data_dir/taxonomy` 仅保存安装成功后的运行时 materialization 和 taxonomy snapshot；
它们不再由 Git 或开发脚本复制。

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

## Incus

Node runtime 的基础镜像和 role-specific mTLS 身份由 `scripts/incus/bootstrap.sh` 准备。脚本将证书写入被忽略的
`.local/incus/<role>/`，并输出 `base_image_fingerprint`；管理员再将 `server`、`controller` 和 `generate` 三套证书创建为
`breakfix-incus-*` Secret，并把该 fingerprint 写入 `breakfix-runtime` 的 `incus_base_image_fingerprint`。

## 验收

```bash
make test-unit
make lint
make build
make test-e2e
RUN_RUNTIME_E2E=1 npm run test:runtime:browser --prefix test
BREAKFIX_E2E_BASE_URL=http://localhost:9090 RUN_SERVER_RECOVERY_E2E=1 npm run test:recovery --prefix test
```

模型驱动的端到端验收显式运行：`RUN_AGENT_LIVE_E2E=1 npm run test:agent-live:node --prefix test`、
`RUN_AGENT_LIVE_E2E=1 npm run test:agent-live:k8s --prefix test` 和
`RUN_AGENT_LIVE_E2E=1 npm run test:agent-live:assistant --prefix test`。Kind 验收要求 Kind overlay、固定 NodePort Registry、OpenSandbox 和
对应环境 provider；完整边界见[测试与真实验收](testing.md)。
