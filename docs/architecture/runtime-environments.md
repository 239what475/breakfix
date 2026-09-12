# 运行环境

Breakfix 的学习与真实验证都使用相同的 Environment 契约。Environment 是短生命周期 CRD；
Server 写 `spec`，Controller 调和实际资源并写 `status`。Controller 不拥有候选、Workflow 或
发布状态。

## 两类环境

| CRD | runtime | 学习者入口 | 底层资源 |
| --- | --- | --- | --- |
| `NodeEnvironment` | `node` | 可以进入场景声明的所有节点 | Incus system containers 与每环境隔离网络。 |
| `VK8sEnvironment` | `k8s` | 进入管理 terminal，通过 kubeconfig 操作 vcluster | vcluster、管理 terminal 和场景工作负载。 |

环境 `spec.environment.purpose` 是 `learning` 或 `verification`。学习环境来自当前 active scenario revision；
验证环境来自 immutable CandidateRevision artifact。两者都复制运行时 profile、scenario revision、
可选 checkpoint 定义和 artifact reference，因此后续配置或场景修改不会改变已运行环境。

Environment 的 `spec.environment.source` 同时保存稳定 `scenario_id` 和不可变 `scenario_revision_id`。作者发布新
revision 或弃用 Scenario 后，已有 Environment、Progress、Assistant 和 Terminal 仍按这个 revision 读取；只有新建
Environment 才解析当前 Catalog。Deprecated Scenario 不出现在公开 Catalog，也不能创建新的学习 Environment。

## 生命周期

```text
Pending -> Provisioning -> Ready -> Draining -> Destroyed
                              |
                              +-> Completed
                              +-> Failed
```

Controller 根据 CRD finalizer、用户停止、完成、空闲时间和 drain grace period 回收资源。Server 记录
终端/学习活动并更新 Environment `spec` 中的 activity 信息；Controller 即使 Server 重启也能继续按已
持久化的生命周期策略收敛。

## 运行时初始化与检查点

每个运维场景携带 `generate.sh`，但它不是镜像构建步骤。基础镜像只包含平台运行时；Environment 启动后由
runtime init 挂载场景 artifact、执行 `generate.sh` 并进入可交互状态。这样同一 scenario bundle 可以
在学习与验证环境使用一致的初始化语义。

检查点没有人为 Submit。声明了 checkpoint 时，`internal/domain/checkpoint` 是 `checks.sh` JSON report 的唯一协议实现；Controller 与
Runtime Worker Verifier 都用它校验字段、expected ID 完整性和整体通过状态。Controller 只额外把共享 Result 转换为
包含 `FirstPassedAt` 的 Environment status，并将首次通过事件投影到学习记录。没有 checkpoint 的环境保持 Ready，不伪造完成记录；
所有已声明检查点通过后，环境自动完成。

Runtime Worker 在 `Verifying` state 创建 `purpose=verification` Environment；它等待 runtime init，先运行
`reproduce.sh` 收集目标现象的结构化证据。任一证据未观察到时，验证以 artifact failure 结束且不会执行参考修复；只有全部证据
成立后，若场景提供完整参考修复，才运行 `answer.sh` 并收集修复后相同检查点的结构化结果；没有参考修复时复现证据本身就是该次验证的
终点。Environment identity 会先持久化到 CandidateRevision；验证报告持久化后由 Runtime Worker 的异步 reaper 删除该 Environment。
删除失败只重试清理，不会重新执行验证。

## 网络与镜像

NodeEnvironment 使用场景私有 Incus project/network；Node 名称与静态地址由平台生成并写入对应节点的
`/etc/hosts`，避免向学习者暴露 Incus DNS 细节。VK8s 的 OCI image 由 Kubernetes node 按
`registry_repository` 拉取；私有 Registry 必须让 node 与平台 Pod 使用同一个可解析、可访问且受信任的
HTTPS authority，不能用 `.svc` 作为镜像引用。

每个 `VK8sEnvironment` 的 immutable runtime snapshot 还包含 `network`：

- `public_egress_cidr` 是允许对外连接的 IPv4 CIDR，通常为 `0.0.0.0/0`。
- `protected_cidrs` 是从该 CIDR 中排除的部署边界。部署者必须明确包含实际 Service CIDR、Pod CIDR、平台私网、
  云 metadata/link-local 和其他不应由学习者访问的网络；不能根据某个开发集群在代码中推断。

vcluster chart 原生创建 control-plane 与同步 workload 的 NetworkPolicy。Breakfix 只创建 management terminal
的最小策略，并通过 chart 的 control-plane ingress 扩展允许该 terminal 访问自己的虚拟控制面。学习者 workload
只能访问同一虚拟集群的必要组件、DNS 和 `public_egress_cidr` 去除 `protected_cidrs` 后的地址。terminal 同样不能
访问平台 Service、宿主 API、私网或 metadata。

vcluster 启用 `baseline` Pod Security Standard，阻止 learner 通过 host network 或 privileged Pod 绕过该边界；虚拟
NetworkPolicy 不同步到宿主集群，以免 learner 放宽 host-side 策略。当前外部出口契约仅支持 IPv4；双栈集群中的 IPv6
不会获得默认公网放行，直到有单独、经过真实 CNI 验证的 IPv6 策略。

NetworkPolicy 是否真正生效是底层 CNI 的职责。没有执行 NetworkPolicy 的 Kind 默认网络只能用于清单渲染，不能作为
VK8s 隔离验收依据。
