# 下一阶段方向

本文只记录尚未实现的产品方向，不重复状态机、API 或部署契约。当前的正式边界以
[工作流](docs/architecture/workflows.md)、[运行环境](docs/architecture/runtime-environments.md)、
[Agent Runtime](docs/architecture/agent-runtime.md) 与 [Catalog Release](docs/architecture/catalog-release.md) 为准。

## 题库建设

下一阶段的重点是生产少数领域内完整、可验证且可复用的题目，而不是按零散技术名词扩张。
内容模型固定为 `Domain -> Topic -> Challenge`：每个 Challenge 唯一归属一个 Topic，Topic 归属一个
Domain；Tag 用于跨 Topic 的场景筛选。作者确认题目后，Classification 提出 Topic/Tag 归属，作者可通过
对话调整；RoadmapWorkflow 只维护 Topic 之间和 Challenge 之间的关系。完整流程见
[工作流](docs/architecture/workflows.md)。

首个完整领域是 Linux 系统与网络运维，使用 `NodeEnvironment`。先将一个领域做深，再依次进入 Kubernetes
工作负载与服务运维和 SRE 可靠性运维；不把容器、集群控制平面、CI/CD 或云厂商题目混入尚未完成的领域。具体
Topic、场景卡、能力前置项和内容审查清单见 [TODO.md](TODO.md) 与 `catalog/curriculum/`。

每道公开 Challenge 必须满足：

1. `problem.md` 说明场景、症状、目标和边界。
2. checkpoint 验证用户可观察的最终状态，不规定唯一命令或操作顺序。
3. hints 按证据逐步引导，`solution.md` 解释诊断与修复理由。
4. `answer.sh` 在真实初始化后的环境中通过全部 checkpoint。
5. 题目经过作者审核、真实 `Build -> ArtifactPublish -> Verify`、Classification 审核和发布后才进入公开 Catalog。

## 检查点执行通道

当前 Controller 对每个 Ready/Draining Environment 在真实执行位置周期执行对应的 `checks.sh`。
`VK8sEnvironment` 通过 Kubernetes `pods/exec` 进入管理终端；`NodeEnvironment` 通过 Incus exec 进入指定节点。
这在当前规模下保持不变；只有活跃环境数、checkpoint exec 延迟/错误率或 Controller 队列指标证明 API Server/SPDY
已成为瓶颈时，才实施以下迁移。

目标是将检查的**执行位置**留在用户真实 workspace container 内，但把 Controller 到容器的传输从 Kubernetes
`exec` 换成集群内 HTTP：

```text
Controller -- HTTP --> runtime checkpointd -- checks.sh -- JSON
```

- K8s 管理终端镜像或 Node system-container base image 提供 `breakfix-checkpointd`。它必须和运行时初始化、
  systemd 和 tmux 的边界共存，不能取代 Node 的 systemd PID 1。
- `checkpointd` 仅暴露固定的检查接口，只能无参数执行运行时提供的 `checks.sh`，并保持现有 15 秒超时、输出大小
  限制和 JSON 协议。
- 每个 VK8s Environment namespace 有一个仅集群内部可见的 checkpoint Service；NodeEnvironment 需要同等的、由
  Server/Controller 安全代理的访问通道。Controller 仍是唯一的 Environment status 写者，继续校验 checkpoint ID、
  写入结果并判定 Completed。
- `checkpointd` 不持有 Kubernetes ServiceAccount、CRD 写权限或平台凭据。Verifier 仍可保留一次性的可信 exec 检查，
  因为它不是高频学习环境的规模瓶颈。
- 该迁移消除高频 API `exec`/SPDY 开销，但不消除检查脚本本身的 CPU 成本。迁移后仍先保留 Controller 拉取模型；
  不要提前改为 daemon 主动回调，因为回调需要额外的认证、重试、去重和 Controller HTTP 生命周期设计。

不采用以下方案：

- **tmux**：它属于用户交互会话，Controller 仍需先进入容器才能使用它，且窗口状态和输出都不适合作为确定性检查协议。
- **普通 sidecar**：sidecar 不共享用户容器的可写根文件系统，无法观察对 `/etc`、服务配置或任意路径的修复。
  `shareProcessNamespace` 通过 `/proc/<pid>/root` 穿透文件系统会扩大进程和环境变量暴露面，并与 systemd 不兼容，
  不作为平台设计。
- **Kubernetes liveness/readiness probe**：kubelet probe 可避免 API exec，但只有健康布尔语义；失败会重启容器或将 Pod
  标为 NotReady，不能表达多个 checkpoint 及其诊断结果。

若迁移触发，必须用真实 Node 和 VK8s Environment 验证：初始化、连续检查、终端重连、Controller 重启、网络失败、
环境清理、所有 checkpoint 完成和 Verifier 语义一致性。

## 内容规模具备后的产品方向

### Roadmap 审查与维护

当前不增加独立的后台审查系统。题目分类在发布流程中完成，Topic/Challenge 关系由现有 RoadmapWorkflow 维护，
引用完整性和图约束由确定性逻辑保证。只有题库和学习事件足够多后，才考虑引入只创建审查任务、不直接修改内容的
Roadmap 审查：它可以根据重复关系、长期单题覆盖、关系矛盾、题目修订后的关系漂移及聚合学习数据提出局部审查。
任何候选仍须经过委员会、确定性校验和原子发布。

### 学习路径与关系图

学习路径是基于 Domain、Topic 与 Challenge 关系的人工编排视图，不是另一套题目依赖事实。路径内容可以保存在
文件系统，用户进度保存在数据库。初期只提供推荐顺序和下一步建议，不强制锁定后续题目；界面只展示当前领域的
局部关系图，不默认展示全局大图。

### 题目质量分析与反馈

当有稳定学习数据后，聚合启动、重置、checkpoint 首次通过、完成、提示/解答打开和助手求助。作者据此查看完成率、
checkpoint 流失、中位完成时间、重置率和提示依赖度，并通过“报告问题”入口接收题目版本、运行时和当前 checkpoint。
不要收集或展示用户完整终端内容。

### 复盘与推荐

在学习路径和事件数据存在后，再提供完成后的 checkpoint 复盘、基于解答的复习内容、稍后学习队列和每日推荐。
它们应服务于连续学习，而不是增加孤立互动功能。

## 暂缓方向

独立 Playground、讨论区、排行榜、证书、付费体系、团队/LMS/SSO 集成均不属于近期阶段。它们需要更大的题库、
稳定用户群或组织级需求才能产生价值。并发配额、资源预算和滥用控制也等到真实公开并发需求出现后单独设计。
