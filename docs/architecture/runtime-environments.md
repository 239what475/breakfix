# 运行环境

Breakfix 的学习与真实验证都使用相同的 Environment 契约。Environment 是短生命周期 CRD；
Server 写 `spec`，Controller 调和实际资源并写 `status`。Controller 不拥有候选、Workflow 或
发布状态。

## 两类环境

| CRD | runtime | 学习者入口 | 底层资源 |
| --- | --- | --- | --- |
| `NodeEnvironment` | `node` | 可以进入题目声明的所有节点 | Incus system containers 与每环境隔离网络。 |
| `VK8sEnvironment` | `k8s` | 进入管理 terminal，通过 kubeconfig 操作 vcluster | vcluster、管理 terminal 和题目工作负载。 |

环境 `spec.environment.purpose` 是 `learning` 或 `verification`。学习环境来自已发布 challenge；
验证环境来自 immutable CandidateRevision artifact。两者都复制运行时 profile、challenge revision、
checkpoint 定义和 artifact reference，因此后续配置或题目修改不会改变已运行环境。

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

每道题携带 `generate.sh`，但它不是镜像构建步骤。基础镜像只包含平台运行时；Environment 启动后由
runtime init 挂载题目 artifact、执行 `generate.sh` 并进入可交互状态。这样同一 challenge bundle 可以
在学习与验证环境使用一致的初始化语义。

检查点没有人为 Submit。Controller 按题目定义运行对应的检查脚本、写入结构化 checkpoint 状态，并将首次
通过事件投影到学习记录。所有检查点通过后，学习挑战自动完成。

Generate Worker 在 `Verifying` state 创建 `purpose=verification` Environment；它等待 runtime init、运行
`answer.sh`、收集相同检查点的结构化结果，再删除该 Environment。验证报告属于 CandidateRevision，
Workflow 只保存当前阶段和 lease。

## 网络与镜像

NodeEnvironment 使用题目私有 Incus project/network；Node 名称与静态地址由平台生成并写入对应节点的
`/etc/hosts`，避免向学习者暴露 Incus DNS 细节。VK8s 的 OCI image 由 Kubernetes node 按
`registry_repository` 拉取；私有 Registry 必须让 node 与平台 Pod 使用同一个可解析、可访问且受信任的
HTTPS authority，不能用 `.svc` 作为镜像引用。
