## Breakfix K8s 多租户环境方案

### 背景

Breakfix 是一个运维练习平台，需要为每道 K8s 题目给用户提供独立的集群环境。由于资源有限（跑在阿里云 ACK 上），不可能为每个用户分配独立集群。核心需求是：单物理集群内虚拟出多个隔离环境，用户感知上拥有完整集群，底层共享资源。

### 约束条件

- 运行在阿里云 ACK 集群，ECS 不支持嵌套虚拟化，无法使用 VM-in-VM 方案
- 需要在有限资源下支持尽可能多的并发用户
- 环境需要快速创建和销毁（秒级到分钟级）
- 需要支持不同深度的故障模拟（应用层、基础设施层、控制面逻辑层）

### 整体架构

```
                      ┌──────────────────────────────────────────────┐
                      │           宿主集群 (阿里云 ACK)               │
                      │                                              │
     ┌─────────┐      │  ┌────────────────────────┐                 │
     │ Gateway  │─────▶│  │  Breakfix Controller    │                 │
     │ (集群外) │      │  │  (K8s Operator)         │                 │
     └─────────┘      │  └────────┬───────────────┘                 │
                      │           │ 按 environmentType 创建环境       │
                      │     ┌─────┼─────────┐                       │
                      │     ▼     ▼         ▼                       │
                      │  vcluster  Kind    KWOK                     │
                      │  (应用层) (基础设施) (控制面逻辑)              │
                      └──────────────────────────────────────────────┘
```

Gateway 放在集群外，负责认证、限流、路由，避免集群本身出故障时用户连入口都没有。Controller 做成 K8s Operator，用 CRD 声明式管理用户环境的全生命周期。

### 三层环境模型

#### 第一层：vcluster — 应用层题目

vcluster（Loft Labs 开源）在宿主集群的一个 namespace 里运行轻量级虚拟控制面（独立 API Server + kine/etcd），用户的 kubectl 连接到虚拟 API Server，所有操作通过 syncer 组件同步到宿主集群的真实资源上。

适用场景：Pod 崩溃、Service 不通、RBAC 配错、ConfigMap/Secret 问题、证书通信故障等。

资源开销：约 100MB/用户。创建速度：秒级。

部署方式：Helm chart 安装到用户专属 namespace，做完题删 namespace 即回收。

#### 第二层：Kind — 基础设施题目

Kind（Kubernetes in Docker）以容器嵌套的方式运行完整的 K8s 集群，每个"节点"是一个 Docker 容器，内部运行真实的 kubelet、containerd、API Server、etcd 等全部组件。不需要嵌套虚拟化支持。

适用场景：kubelet 故障、containerd 挂掉、etcd 数据损坏、网络插件异常、证书过期续期等节点级故障。

资源开销：约 1-2GB/用户。创建速度：分钟级。

故障注入方式：Controller 可以 exec 进 Kind 的控制面容器篡改配置、kill 进程、修改证书，模拟真实的基础设施故障。

#### 第三层：KWOK — 控制面逻辑题目

KWOK（Kubernetes WithOut Kubelet，kubernetes-sigs 官方项目）在 API Server 中创建假的 Node 和 Pod 对象，不启动任何真实容器。可以模拟任意规模的集群拓扑。

适用场景：调度策略排障（taint/label/affinity）、HPA/Cluster Autoscaler 调试、拓扑约束（topologySpreadConstraints）、大规模资源竞争与抢占（PriorityClass）。

资源开销：约 10MB/用户。创建速度：秒级。

独有能力：用 CEL 表达式注入动态变化的假指标（如 CPU 线性增长触发 HPA），模拟多 AZ 节点拓扑，秒级创建数百个假节点。

### 对比总结

| 维度 | vcluster | Kind | KWOK |
|------|----------|------|------|
| 用户体验 | 高（独立 API，有节点概念） | 最高（真实集群） | 中（有节点但 Pod 不可 exec） |
| 资源开销 | ~100MB/用户 | ~1-2GB/用户 | ~10MB/用户 |
| 创建速度 | 秒级 | 分钟级 | 秒级 |
| 可 exec 进容器 | 是 | 是 | 否 |
| 可模拟节点级故障 | 否 | 是 | 否 |
| 可模拟大规模场景 | 否 | 否 | 是（数千节点） |
| 真实网络连通 | 是 | 是 | 否 |
| 嵌套虚拟化需求 | 无 | 无（容器嵌套） | 无 |

### Hybrid 组合模式

三层可以组合使用，覆盖更复杂的题目场景。例如：

- KWOK 创建 200 个假节点制造调度压力 + vcluster 跑用户的真实 workload，排查"为什么 Pod 调度到了错误的节点"
- KWOK 注入动态假指标 + vcluster 中的 HPA，排查"为什么自动扩容没有触发"

### CRD 设计草案

```yaml
apiVersion: breakfix.io/v1alpha1
kind: ExerciseEnvironment
metadata:
  name: user-alice-q1
spec:
  user: alice
  exerciseId: k8s-scheduling-001
  environmentType: hybrid        # vcluster | kind | kwok | hybrid
  ttl: 30m                       # 超时自动回收

  vcluster:
    enabled: true
    resourceQuota:
      cpu: "4"
      memory: 4Gi

  kind:
    enabled: false
    controlPlaneNodes: 1
    workerNodes: 2

  kwok:
    enabled: true
    fakeNodes: 200
    fakePods: 1000
    topology:
      zones: [cn-hangzhou-a, cn-hangzhou-b, cn-hangzhou-c]
    metrics:
      cpuPattern: "linear(0.1, 0.9, 5m)"   # CEL 表达式

  faultInjection:
    type: scheduling
    target: user-workload
    description: "节点 label 配置错误导致 Pod 全部调度到同一 AZ"
```

### Controller Reconcile 逻辑

1. 监听到 ExerciseEnvironment CR 创建
2. 根据 `environmentType` 选择创建路径：
   - `vcluster`：创建 namespace + ResourceQuota + Helm install vcluster
   - `kind`：创建 namespace + DinD sidecar + Kind cluster
   - `kwok`：创建 fake nodes + fake pods + metrics 注入
   - `hybrid`：按组合策略依次创建
3. 注入预设故障（faultInjection）
4. 生成 kubeconfig，通过 Gateway 下发给用户
5. 监控 TTL，到期自动清理全部资源

### 技术栈

| 组件 | 技术选型 | 说明 |
|------|---------|------|
| Gateway | Go / gRPC 或 HTTP | 集群外部署，认证 + 路由 + kubeconfig 分发 |
| Controller | Go + controller-runtime | K8s Operator，管理 ExerciseEnvironment CRD |
| vcluster | Helm chart | 虚拟集群生命周期管理 |
| Kind | kind CLI + DinD | 嵌套集群，基础设施故障模拟 |
| KWOK | kwok + kwokctl | 大规模 API 级仿真 |
| 宿主集群 | 阿里云 ACK | ECS 节点，不支持嵌套虚拟化 |

