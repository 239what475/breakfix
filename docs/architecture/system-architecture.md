# 系统架构

Breakfix 是一个用真实运行环境练习运维问题的平台。已发布题目是文件系统内容；用户和验证环境是 Kubernetes CRD；生成、构建、发布和验证是 Server/PostgreSQL 持久 worklist 上的固定 Worker 流水线。

## 权威来源

- HTTP 路由、请求和响应：[`api/openapi.yaml`](../../api/openapi.yaml)
- Environment CRD 契约：[`internal/k8s/apis/breakfix/v1/`](../../internal/k8s/apis/breakfix/v1/)
- challenge 目录、归档和发布校验：[`internal/challenge/`](../../internal/challenge/)
- 运行时与 Provider 配置：[`config/breakfix.example.yaml`](../../config/breakfix.example.yaml)

本文件记录所有权和流程，不复制这些来源的完整字段清单。

## 进程边界

```text
Browser
  | HTTP / WebSocket
  v
breakfix-server <----> PostgreSQL <----> agent / builder / publisher / verifier Workers
  | Kubernetes API                         | fenced internal HTTP
  | Incus SDK                              |
  v                                        v
breakfix-controller ----> NodeEnvironment / VK8sEnvironment status and finalizers
  |                         |
  v                         v
Incus system containers     namespaces, vclusters and management terminals
```

`breakfix-server` 负责 HTTP、内嵌 Web UI、认证、WebSocket 终端、作者会话、学习助手、文件系统题库、taxonomy snapshot、CandidateRevision 和 WorkItem。它是领域数据的唯一写者：只创建或更新 Environment `spec`、读取 status 并投影学习事实，绝不写 `status`。终端先由 JWT 保护的 HTTP 接口签发一次性 ticket，再由严格 `ui_origin` 校验的 WebSocket 消费；Server 从不接受 URL 中的 JWT。

`breakfix-controller` 只运行 controller-runtime manager。它读取 `NodeEnvironment`/`VK8sEnvironment` spec，供应和回收 Incus 或 Kubernetes/vcluster 资源，运行学习环境的检查点并写回 status。它不访问 PostgreSQL、题目目录、候选归档或 Worker 队列。

四类 Worker 是常驻 Deployment：Agent Worker 执行模型 Run；Builder Worker 构造不可变运行时产物；Publisher Worker 管理 staging、正式引用和 cleanup；Verifier Worker 创建验证 Environment 并执行答案与检查点。所有 Worker 只通过 Server 内部 API 领取、续租和提交；它们不直接写 PostgreSQL，也不会自行推进下一阶段。

## 数据所有权

| 数据 | 权威所有者 | 访问原则 |
| --- | --- | --- |
| 已发布题目 | `data_dir/challenges/` | Server 在 ChallengePublish 时原子写入；不是数据库或 CRD 副本。 |
| Skill、Tag 与 mapping | `data_dir/taxonomy/current` 不可变快照 | Server taxonomy workflow 发布；数据库只保存 Mapping 和 AgentRun 状态。 |
| 未发布 CandidateRevision 与归档 | Server data volume | Server 保存不可变归档；Worker 只走 task-bound 下载/上传 API。 |
| 账户、作者会话、学习记录、AgentRun、WorkItem | PostgreSQL | Server 是唯一写者。 |
| Generator workspace | Server-owned PVC + PostgreSQL record | Server 创建、清理和围栏；OpenSandbox 只以 BYO 模式挂载。 |
| 用户和验证环境 | NodeEnvironment / VK8sEnvironment CRD | Server 提供不可变 spec，Controller 管理资源和 status。 |
| staging 与正式运行时产物 | Registry 或 Incus image Project | Publisher 建立精确引用；Verifier 只使用不可变 digest/fingerprint。 |

Environment spec 在创建时包含不可变执行快照，因此 Controller 无须重读题目目录，验证环境也不会因作者之后的修订改变执行语义。

## 请求、阶段与恢复

用户开始挑战时，Server 从已发布题目目录读取 manifest，创建 learning Environment CRD，并等待 Controller 调和到 Ready。Node terminal 经 Server 代理到 Incus tmux；VK8s terminal 经 Server 代理到管理 Pod。终端活动是 Environment spec 输入，Controller 计算 idle lease、Draining 和回收；完成、失败和检查点结果都由 CRD status 表达。

作者确认题意后，Server 创建 Generator AgentRun。Judge 通过后，Server 在同一数据库事务中保存 CandidateRevision 并创建 `build` WorkItem；阶段顺序固定为 `build -> artifact_publish -> verify -> author review -> challenge_publish`。每个 WorkItem 的 lease owner、attempt 和 deadline 围栏所有外部副作用。详细流程见[作者生成与真实验证](authoring-workflow.md)。

Server 重启不停止 Controller reconcile。Controller 重启后从现有 CRD 继续供应、检查或清理；Server 恢复后重新投影现有 Environment status，并恢复 `PublishingChallenge` 的明确发布意图。Worker 崩溃后租约过期，由同类型固定副本接管；旧 attempt 的结果被 Server 拒绝。

## 当前部署假设

运行时包含 Server、Controller、Agent Worker、Builder Worker、Publisher Worker、Verifier Worker 和 PostgreSQL；根部署包默认附带 OCI Registry，也可用外部 Registry 替换。Server 的 RWO data PVC 保存 catalog 与 candidate artifact，因此当前 Server 使用单副本 `Recreate` 策略。Node provider 是集群外、mTLS 访问的 Incus cluster；VK8s 保留在 Kubernetes 集群内。

K8s OCI Registry 是私有平台基础设施，不是 `*.svc` 形式的 Pod 内部服务。内置模式使用管理员提供 TLS Secret 的私有 LoadBalancer，且所有节点用正常 DNS 解析稳定内网名称，例如 `registry.breakfix.internal`。内部 CA 根证书由管理员在节点镜像运行时中预装；Server 和 Publisher 通过可选的 `breakfix-registry-ca` ConfigMap 信任同一根 CA。外部模式不部署 Registry，直接使用配置的 HTTPS OCI endpoint。每个 VK8s 环境 namespace 在配置了 Docker pull Secret 时获得其副本，并将其绑定到禁用 token 自动挂载的 `breakfix-runtime` ServiceAccount；无认证 Registry 则只创建该 ServiceAccount。terminal Pod 使用该 ServiceAccount，Kubernetes admission 可将 image-pull Secret 引用写入持久化的 Pod spec，但不会将 Secret 或 Kubernetes API token 挂载进挑战容器。

多 Server 副本需要共享 artifact/catalog storage 和可协调的文件提升协议；PostgreSQL 本身不解决文件系统边界。Taxonomy 的发布、并发和 Catalog 准入见[Taxonomy 与 Catalog 发布](taxonomy.md)。
