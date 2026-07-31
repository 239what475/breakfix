# Zero to JupyterHub on Kubernetes 对照审查

## 审查对象

- 本地路径：`/home/what/myproject/breakfix-similar-projects/zero-to-jupyterhub-k8s`
- 审查提交：`192c099`
- 定位：JupyterHub 的 Kubernetes Helm chart 与生产部署指南。

## 已确认的设计

1. `jupyterhub/values.yaml` 将 Hub、Proxy、singleuser、scheduling、storage 和 cull 的
   运行策略分区配置，并提供相应 JSON Schema。
2. chart 为 Hub、Proxy、singleuser 生成独立 NetworkPolicy；默认 singleuser 禁止访问
   私有 IP，并显式允许 Hub/DNS 所需流量。
3. singleuser 可动态创建 RWO PVC；idle-culler 以独立服务按 timeout/every/concurrency
   清理 user server。Hub 使用 SQLite PVC 时要求 `Recreate`，并明确说明其 HA 限制。

## 与 Breakfix 的对照

Breakfix 已有相同层面的显式资源：Server data PVC 使用 Recreate、Registry 有 PVC 和
resources、Builder 有独立 egress NetworkPolicy，Environment 有 timeout/cleanup policy，
Controller 依据 `activityAt` 执行 Draining/Destroy。它没有把通用 workspace 存储开放给
用户，这对可回收题目环境是正确的。

## 可以吸收

### 现在吸收

- **运行时配置分组和校验**：将 Environment resources、timeout、cleanup、network 与
  runtime profile 的默认值集中放入配置并启动校验，避免散落在 Controller 常量和 manifest。
  不增加新 CRD 概念，只让已有字段的默认值、上限和来源可审计。
- **NetworkPolicy 验收矩阵**：当前已有 Builder 的真实 Cilium 边界测试。为 workspace
  node/k8s 增加同样的断言：允许 DNS、终端代理、VK8s 必需流量；禁止 host
  Kubernetes API 和不属于题目的控制面访问。实施前必须列出 vcluster 实际依赖，不能机械
  复制 JupyterHub 的 private-IP deny。

### 题库具备数据后再做

- 当并发量有实测数据后，借鉴 culler 的独立调度参数，为 Controller 的清理循环增加指标、
  批量限制和容量目标。现在已有 CRD 驱动回收，不能为了“像 culler”另起第二个清理权威。
- 动态 PVC 仅在某种未来 runtime 确实要求跨 Pod 保留用户工作目录时评估；不适用于目前
  的 challenge attempt。

## 不采用

- 不开放 `singleuser.extraPodConfig`、extra containers 或任意模板覆盖。用户/作者输入若能
  修改平台 Pod spec，会绕过 Breakfix 的 Build/Publisher/Verifier 信任边界。
- 不采用 user scheduler、placeholder pods 或 JupyterHub Proxy。这些都是大规模 notebook
  服务的优化，当前没有对应容量证据。

## 结论

该项目最有价值的是“配置、网络、存储、清理都必须有独立默认值和验收”的运维纪律。应先
把现有 Environment runtime profile 和 NetworkPolicy 测试做清楚，而不是引入其 Helm 架构。
