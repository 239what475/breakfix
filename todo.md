# P0：面向所有登录用户的题目构建与真实验证隔离

所有登录用户都可以通过作者工作流创建题目。题意、Agent 生成的
`Dockerfile`、`generate.sh`、`answer.sh` 和检查点脚本必须视为不可信输入；
不能让它们与宿主节点特权、host Kubernetes API、全局 Server 内部密钥或全局
Registry 写入凭据处于同一个执行边界。

当前 VerifyTask 的 verifier Job 同时运行 rootful、`privileged` BuildKit，并持有
Kubernetes ServiceAccount、Registry 写凭据和 Server internal API key。仅将
BuildKit 改成 rootless 不足以解决问题：rootless 只降低构建器的宿主机权限，
不会移除 Job 已有的控制面权限。官方 rootless Kubernetes 模式还可能使用
`--oci-worker-no-process-sandbox`，因此不能与任何可信 verifier 进程或凭据
共用 Pod。

## 目标架构

### 1. 不可信 Build Job

- 每个 VerifyTask 创建独立、短生命周期的 rootless BuildKit Build Job。
- Job 不使用 `privileged`，以非 root 用户运行，关闭
  `automountServiceAccountToken`，没有 Kubernetes RBAC、Server 全局 internal key
  或任何 Registry 凭据。
- Build Job 只能取得本次 submission 的一次性、任务绑定 artifact 下载能力，以及
  对同一 VerifyTask 的一次性 OCI image archive 上传能力。
- BuildKit 将镜像导出为 OCI archive 并交给 Server 的任务专属暂存区；它不直接
  访问 Registry。这比向 BuildKit 发放 repository-scoped push token 更强，也避免
  为现有 Registry 的单一 basic-auth 用户新增一个节点必须可达的 token realm。
- 网络策略只允许访问 Server artifact 入口和 DNS；设置资源限制与 Job deadline，避免
  一个构建占用共享集群资源。Build Job 不直接访问 Registry。

### 2. 可信 Publisher Job

- Publisher 是由 Controller 派生的独立短生命周期 Job，不与 BuildKit 共用 Pod。
- 它只取得同一 VerifyTask OCI archive 的一次性下载能力、固定的 staging image
  名称和 Registry 写凭据；没有 Kubernetes RBAC 或 ServiceAccount token。
- Publisher 只将 archive 推送到由 VerifyTask 派生的 staging repository/tag。
  Controller 随后从预期 tag 读取 Registry 的实际 manifest digest，绝不信任 Job
  自报的镜像名称或 digest。

### 3. 可信 Verifier Job

- Verifier 不运行 BuildKit，也不持有 Registry 写入凭据。
- 它只接收 Controller 确认过的 staging image digest，并创建真实
  `ContainerEnvironment` 或 `VClusterEnvironment`。
- 它等待运行时初始化，执行 `answer.sh` 和同一套 checkpoints，并写入受限的
  VerifyTask 验证结论。
- 验证环境中的 workspace Pod 一律关闭 host Kubernetes ServiceAccount token。
  `runtime: vcluster` 题所需的 kubeconfig 仅指向该环境自己的 vcluster，不得提供
  host cluster 凭据。

### 4. 可信发布与清理

- 只有 Server/Controller 的可信发布路径持有正式 Registry 权限。
- VerifyTask 成功并经作者确认发布后，Server 以可信 Registry 凭据将已验证 staging
  digest 提升为由平台 opaque challenge ID 派生的正式 image；失败后删除 staging image
  和临时 Environment。
- 题目文件、最终镜像引用和 taxonomy 发布仍遵循现有的 VerifyTask 成功后作者审核
  与文件系统题库流程。

## 实施顺序与验收

1. 先在目标 Kubernetes 环境完成真实 rootless BuildKit POC，构建当前
   `cleanup-logs` 与一个 `runtime: vcluster` challenge。
2. POC 必须证明 Build Job 无 `privileged`、无自动挂载 ServiceAccount token、无
   Kubernetes RBAC 和 Registry 凭据，且能产出 OCI archive。
3. 再将当前单一 verifier Job 拆为 Build Job、Publisher Job 与 Verifier Job，
   替换全局 artifact 下载 key 和全局 Registry 写 Secret 的错误注入方式。
4. 用真实 Kubernetes 验收覆盖：成功构建、Publisher 推送和真实验证、构建失败、
   验证失败、staging 镜像清理、正式镜像提升，以及 Build Job 无法访问 host
   Kubernetes API 和正式 Registry 写入路径。

这项完成前，不应把当前 verifier 的 rootful privileged BuildKit 当作公共作者
功能的安全边界。
