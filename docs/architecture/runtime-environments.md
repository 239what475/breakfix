# 运行环境

Challenge 只描述学习内容。用户和验证所使用的真实运行环境由 Kubernetes CRD 表达，当前只有两种：`NodeEnvironment` 与 `VK8sEnvironment`。它们共享不可变执行快照、运行时初始化、检查点 JSON 协议和生命周期模型，但不会向题目、浏览器或作者暴露底层 Provider。

## Environment CRD

Server 从已发布 challenge 或不可变 CandidateRevision 创建 `NodeEnvironment`、`VK8sEnvironment`。创建时写入完整的运行时快照、来源、检查点和 `purpose`：

- `learning` 是用户开始题目后得到的环境。Controller 周期执行检查点，将结果写入 status；所有检查点通过时环境自动 `Completed`。
- `verification` 只由 Verifier Worker 创建。Controller 只供应、初始化和回收它；Verifier 在参考答案执行后单次运行检查点，避免两个组件同时判定或改变同一环境。

Server 不写 CRD status，Controller 不从文件系统读取 challenge，也不访问 PostgreSQL。完整类型和生成清单分别位于 [`internal/k8s/apis/breakfix/v1/`](../../internal/k8s/apis/breakfix/v1/) 与 [`deploy/crd/`](../../deploy/crd/)。

Environment spec 是不可变执行快照。学习环境引用已发布 revision；验证环境引用 CandidateRevision。Controller 只消费快照中的节点、检查点、镜像/Incus fingerprint、资源档位和运行时配置 revision，不能在运行时重新解释题目目录或候选归档。

## NodeEnvironment

`runtime: node` 对应一组真实 Linux 节点。每个逻辑节点由 Incus 提供 unprivileged system container，以 systemd 为 PID 1；用户可以分别进入全部节点，使用 shell、`systemctl`、`journalctl`、SSH 和网络工具。

一个 NodeEnvironment 具有独占 Incus Project、managed bridge、ACL、profile 和节点实例。逻辑节点名例如 `client`、`proxy`、`app` 是题目协议的一部分，平台在每个节点写入托管 `/etc/hosts` 段实现名称解析；用户不需要也不应了解 Incus instance、Project、bridge 或成员名。不同 Environment 使用不同 Project 与网络，节点不能跨 Environment 互通。

Server 使用 Incus SDK 代理交互 exec WebSocket。每个节点持久保留一个 tmux 会话；浏览器断开只结束本次 attach，Environment 删除才回收节点与会话。Controller 负责 Project、image、network、profile、instance 和 finalizer 的精确清理。

## VK8sEnvironment

`runtime: k8s` 对应一个隔离 Kubernetes 管理实验。Controller 创建专属 namespace、隐藏的 vcluster 和唯一管理终端；终端持有 kubeconfig，用户通过 `kubectl` 操作虚拟集群，而不是登录 Kubernetes worker node。

vcluster 是实现细节，不是 manifest、API 或 UI 中的 runtime 值。`VK8sEnvironment` 同样只在 Controller 的 CRD reconcile 中创建和删除；它与 NodeEnvironment 有相同的 status、deadline、条件和 finalizer 约束。

## 初始化与检查点

所有题目都在运行时初始化。平台总是通过 `/bin/bash` 调用脚本，因此题目脚本不依赖可执行位或 shebang：

- Node 题的每个逻辑节点拥有 `nodes/<node>/generate.sh`、`answer.sh`，有检查点的节点还拥有 `checks.sh`。Node 基础镜像首次启动时运行本节点的 `generate.sh` 并写 sentinel。
- K8s 题使用 `k8s/generate.sh`、`answer.sh`、`checks.sh`。管理终端启动后运行 `k8s/generate.sh`。

`generate.sh` 负责建立错误初态，可以安装题目专属软件；Builder 从不执行它。`answer.sh` 仅用于真实验证，学习环境永远不会自动运行答案。`checks.sh` 不接收平台参数，stdout 只输出覆盖本执行位置全部 checkpoint 的结构化 JSON；未通过应输出 `passed: false` 且退出 0，脚本/协议错误才以非零退出。

学习环境中的 Controller 周期执行同一份 `checks.sh` 并记录首次通过时间、最近结果和执行错误。没有用户可见的 Submit，也没有 `verify.sh` 兼容路径。

## 生命周期与清理

典型状态是 Pending、Provisioning、Ready、Draining、Completed、Destroyed 和 Failed。Server 将终端活动写入 Environment spec，Controller 根据 idle TTL、drain grace 与 deadline 调和状态。Stop 或 Reset 请求删除 CRD；finalizer 必须回收 Node 的 Incus 资源或 VK8s 的 namespace、vcluster、Secret 和终端资源。

Provider、网络、镜像、Project/namespace/vcluster 供应失败属于 infrastructure；候选 `generate.sh` 已开始后非零退出属于 artifact。status 使用结构化 failure class 和稳定 reason，不通过 stderr 关键词重分类。Node provider 不可用时只影响新的 NodeEnvironment，不能影响 VK8sEnvironment。

## 与真实验证的关系

Verifier Worker 从 CandidateRevision 创建 `purpose=verification` Environment，等待 runtime-init 完成，运行所有 `answer.sh`，再运行同一套 `checks.sh`。无论通过、artifact failure、infrastructure failure 或 lease 丢失，Verifier 和 Controller 都按 Environment UID 精确清理资源。Build、ArtifactPublish、Verify、作者审核与 ChallengePublish 的完整所有权见[作者生成与真实验证](authoring-workflow.md)。
