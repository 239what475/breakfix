# 运行环境

Challenge 只描述练习内容；用户获得的运行环境才是 Kubernetes CRD。当前支持两种 runtime：`container` 和 `vcluster`。二者共享题目包、初始化和检查点协议，因此用户产品流程保持一致。

## Environment CRD

Server 创建 `ContainerEnvironment` 或 `VClusterEnvironment`。创建时写入题目的不可变执行快照和用户引用，并提供活动时间等期望输入。Controller 在 `status` 中记录资源名称、阶段、条件、检查点结果、错误、生命周期时间和过期时间。

这条边界是强制的：Server 不写 status；Controller 不从文件系统重读 challenge。完整 CRD 类型和生成的部署清单分别位于 [`internal/k8s/apis/breakfix/v1/`](../../internal/k8s/apis/breakfix/v1/) 与 [`deploy/crd/`](../../deploy/crd/)。

## Container runtime

`runtime: container` 对应一个隔离 namespace 和一个 workspace Pod。用户的浏览器终端连接该 Pod；题目镜像中的 runtime init 在首次启动时执行 `generate.sh`，再进入交互 shell。Controller 周期运行 Pod 内的检查点脚本并更新 CRD status。

## VCluster runtime

`runtime: vcluster` 仍以一个用户运行环境为单位，但 Controller 还会在隔离 namespace 中创建 vcluster、kubeconfig Secret 和 workspace Pod。workspace Pod 挂载 kubeconfig，用户通过同一个浏览器终端使用 `kubectl` 操作自己的 virtual cluster。

vcluster 的安装、等待和删除由 [`internal/vclustercli/`](../../internal/vclustercli/) 封装并由 Controller 调用。它不是独立的顶层用户 CRD，因为终端、租约、检查点、完成和清理都以完整的 VClusterEnvironment 为单位。

## 初始化与检查点

基础镜像入口脚本在 sentinel 不存在时执行 `/breakfix/generate.sh`，成功后写入 sentinel；vcluster 基础镜像会先等待 kubeconfig 可用。初始化脚本见 [`deploy/images/base/runtime-init.sh`](../../deploy/images/base/runtime-init.sh) 和 [`deploy/images/k8s-base/runtime-init.sh`](../../deploy/images/k8s-base/runtime-init.sh)。

Controller 只执行 `/checks/checkpoints.sh --json`，并用 Environment spec 中的 checkpoint ID 校验输出。检查点结果必须覆盖每个预期 ID 且没有未知或重复 ID。所有检查点通过后，Controller 将环境标为 Completed；没有用户可见的 Submit 按钮或独立 `verify.sh` 路径。

## 生命周期与清理

典型阶段为 Pending、Provisioning、Ready、Draining、Completed、Destroyed 与 Failed。活动终端会让 Server 更新 `spec.activityAt`；Controller 依据 idle TTL 和 drain grace 计算状态转换。显式 Stop/Reset 请求删除 CRD，Controller finalizer 清理 namespace、Pod、vcluster 和 Secret。

Destroyed 与 Failed CRD 在 Server 完成数据库投影后才删除，避免临时运行资源的消失丢失学习事实。Completed 环境也由清理策略回收，长期完成记录不依赖 CRD 留存。

## 与真实验证的关系

`VerifyTask` 不在本机模拟题目。它构建 artifact 镜像，启动与 challenge runtime 相同的真实 Environment，等待运行时初始化，执行 `answer.sh`，再运行同一套检查点。验证成功后的镜像和 artifact 才能由作者发布到文件系统题库。详细流程见[作者生成与验证](authoring-workflow.md)。
