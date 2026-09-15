# 运行环境

Breakfix 使用单一 `breakfix.dev/v2` `RuntimeEnvironment` CRD 表示短生命周期的学习和验证环境。Server 创建经过公共运行契约校验的 `spec`；Controller 根据不可变 `RunnableRevision` 与 runtime profile 调和 provider 资源，并只写 `status`。Controller 不拥有内容、Workflow 或发布状态。

## Provider

| provider | 学习者入口 | 底层资源 |
| --- | --- | --- |
| `node` | 场景声明的节点终端 | Incus system containers 与每环境隔离网络。 |
| `k8s` | 管理 terminal，通过 kubeconfig 操作 Kubernetes | vcluster、管理 terminal 和工作负载。 |

`spec.runnableRevisionRef`、`purpose`、lease、reset nonce 和生命周期边界在创建后不可变。Controller 从 revision 的 runtime profile 解析 provider，不维护按 provider 分叉的 CRD schema。`status` 只保存当前 phase、operation、条件、资源/endpoint 引用和 `VerificationReport` 引用，不复制内容层步骤或断言结果。

## 生命周期

```text
Pending -> Provisioning -> Ready -> Draining -> Released
                    |             |
                    +-----------> Failed
```

Server 不能借由直接修改 CRD 跳过 Runtime Worker 的前置校验。reset、stop 和回收使用稳定资源 identity、lease fencing、deadline 和幂等 operation。验证结束只记录环境可释放；独立 Reaper 异步停止和回收资源。回收失败不改变验证结果或内容发布状态。

## 验证与观察

验证器消费完整 `RunnableRevision`，按有序阶段执行允许写入的 action 和只读 assertion，并产生绑定 revision digest、artifact digest、环境 profile revision 与 attempt 的 `VerificationReport`。业务断言失败是有效验证结果；协议错误、越权或未声明的结果是 artifact failure。

终端、日志、事件和资源状态只依赖 `RuntimeEnvironment` 的公共资源引用，不依赖 Operations 或 Documentation 字段。内容模块可以将验证报告投影为自己的展示数据，但不能回写 CRD 或改变公共结果。

## 网络与镜像

Node provider 使用每环境 Incus project/network；节点地址由平台生成。Kubernetes provider 的 OCI image 必须使用部署者提供的可达、受信任 Registry 的不可变 digest；不能以 `.svc` 地址作为 image reference。

Kubernetes provider 的 runtime profile 包含网络边界。`public_egress_cidr` 是允许对外连接的 IPv4 CIDR，`protected_cidrs` 是必须排除的平台、Service、Pod 和 metadata 网络。vcluster 使用 `baseline` Pod Security Standard；宿主侧 NetworkPolicy 必须由实际 CNI 执行。没有 NetworkPolicy 的 Kind 仅可用于清单渲染，不能作为隔离验收。
