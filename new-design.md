# Breakfix 运行环境设计

Challenge 是文件系统目录，运行环境才是 Kubernetes CRD。平台目前有两种用户运行环境：

- `ContainerEnvironment`：独立 namespace 中的一个 workspace Pod。
- `VClusterEnvironment`：独立 namespace 中的 vcluster、kubeconfig Secret 和 workspace Pod。

题目只以 `runtime: container | vcluster` 说明用户获得的环境。vcluster 不是独立顶层 CRD，因为用户生命周期、终端、超时、自动完成和清理始终以完整做题环境为单位。

无论 runtime 类型，用户都连接 workspace Pod。vcluster 题的 Pod 已挂载 kubeconfig，用户直接在其中使用 `kubectl` 操作自己的 vcluster。`generate.sh` 在 Pod 首次启动时初始化对应环境；controller 周期执行 `checks/checkpoints.sh --json` 并写入状态，实时进度读取该状态，全部检查点通过即为完成。

`VerifyTask` 用相同的真实环境模型验证 agent workflow 交给 Gateway 的题目 artifact：构建镜像，启动对应 runtime，执行 `answer.sh`，再执行检查点。不存在独立 `verify.sh` 验收路径。

环境详细字段、vcluster 生命周期与 controller 责任以 API/CRD 源码为准；产品和工作台改造计划见 [`iximiuz/next-steps.md`](iximiuz/next-steps.md)。
