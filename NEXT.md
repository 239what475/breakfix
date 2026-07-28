# 下一阶段方向

## 技能图驱动的题库生产

Skill、Tag、Mapping、不可变 taxonomy snapshot 和 Catalog 准入已经具备稳定契约。
下一阶段不再改造 taxonomy 基础设施，而是以它为前提生产高质量题库。所有新题仍必须
经过既有的作者审核、真实 `VerifyTask`、作者确认发布和 taxonomy mapping；题目生成
不能直接创建或修改 Skill、Tag、`requires` 或 mapping。

先选择一个边界清晰的领域，例如网络与 SSH，按技能覆盖蓝图完成 12 到 20 道真实题。
试点应同时验证技能粒度、复合题 mapping、推荐体验、静态内容校验和可读目录组织方式。
模型稳定后，再扩展到容器/Linux 故障排查和 Kubernetes/vcluster 故障排查；目标是每个
核心技能有多道高质量练习，而不是只追求 challenge 数量。

每道公开题目必须满足：

1. `problem.md` 明确说明场景、症状、目标和边界。
2. 检查点验证用户可观察的最终状态，不规定唯一命令或操作路径。
3. 提示按检查点渐进提供；`solution.md` 解释诊断与修复理由。
4. `answer.sh` 在真实运行时初始化后的环境中通过所有检查点。
5. VerifyTask、作者确认和合法 taxonomy mapping 都完成后，题目才进入公开 Catalog。

## 检查点执行通道

当前 Controller 对每个 Ready/Draining Environment 周期性通过 Kubernetes
`pods/exec` 执行 `/checks/checkpoints.sh --json`。这在当前规模下保持不变；只有活跃
环境数、checkpoint exec 延迟/错误率或 Controller 队列指标证明 API Server/SPDY 已成为
瓶颈时，才实施以下迁移。

目标是将检查的**执行位置**留在用户真实 workspace container 内，但把 Controller 到
容器的传输从 Kubernetes `exec` 换成集群内 HTTP：

```text
Controller -- HTTP --> workspace container checkpointd -- checkpoints.sh -- JSON
```

- 基础镜像提供 `breakfix-checkpointd`。运行时初始化完成 `generate.sh` 后，直接以它
  作为 PID 1；它处理信号、子进程回收、脚本超时和串行执行，不需要 systemd。
- `checkpointd` 仅暴露固定的检查接口，只能执行
  `/checks/checkpoints.sh --json`，并保持现有 15 秒超时、输出大小限制和 JSON 协议。
- 每个 Environment namespace 有一个仅集群内部可见的 checkpoint Service；NetworkPolicy
  只允许 Controller 访问。Controller 仍是唯一的 Environment status 写者，继续校验
  checkpoint ID、写入结果并判定 Completed。
- `checkpointd` 不持有 Kubernetes ServiceAccount、CRD 写权限或平台凭据。VerifyTask
  仍可保留一次性的可信 `exec` 检查，因为它不是高频用户环境的规模瓶颈。
- 该迁移消除高频 API `exec`/SPDY 开销，但不消除检查脚本本身的 CPU 成本。迁移后仍
  先保留 Controller 拉取模型；不要提前改为 daemon 主动回调，因为回调需要额外的认证、
  重试、去重和 Controller HTTP 生命周期设计。

不采用以下方案：

- **tmux**：它属于用户交互会话，Controller 仍需先进入容器才能使用它，且窗口状态和
  输出都不适合作为确定性检查协议。
- **普通 sidecar**：sidecar 不共享用户容器的可写根文件系统，无法观察对 `/etc`、服务
  配置或任意路径的修复。`shareProcessNamespace` 通过 `/proc/<pid>/root` 穿透文件系统
  会扩大进程和环境变量暴露面，并与 systemd 不兼容，不作为平台设计。
- **Kubernetes liveness/readiness probe**：kubelet probe 可避免 API exec，但只有健康
  布尔语义；失败会重启容器或将 Pod 标为 NotReady，不能表达多个检查点及其诊断结果。

若迁移触发，必须用真实 container 和 vcluster Environment 验证：初始化、连续检查、终端
重连、Controller 重启、网络失败、环境清理、所有检查点完成和 VerifyTask 语义一致性。

## 内容规模具备后的产品方向

### Taxonomy Dream 与 Review

当前不运行独立 Dream。题目分类、Skill/Tag 提出和关系建立都属于 mapping workflow，
引用完整性和图约束由确定性扫描器处理。只有题库和学习事件足够多后，才引入只创建
review 任务、不直接修改内容的 Dream。它可以依据重复 Skill、长期单题覆盖、关系矛盾、
题目修订后的分类漂移及聚合学习数据提出局部审查；审查仍复用 mapping workflow 的
候选、委员会、静态校验和原子发布。

### 学习路径与技能地图

学习路径是技能图上的人工编排视图，不是另一套题目依赖事实。路径可以使用文件系统
保存内容定义，用户路径进度保存在数据库。初期提供推荐顺序、已满足的 entry skills 与
下一步建议，不强制锁定后续题目；界面只展示当前领域的局部图，不默认展示全局大图。

### 题目质量分析与反馈

当有稳定学习数据后，聚合启动、重置、检查点首次通过、完成、提示/解答打开和助手求助。
作者据此查看完成率、检查点流失、中位完成时间、重置率和提示依赖度，并通过“报告问题”
入口接收题目版本、运行时和当前检查点。不要收集或展示用户完整终端内容。

### 复盘与推荐

在学习路径和事件数据存在后，再提供完成后的检查点复盘、基于解答的复习内容、稍后
学习队列和每日推荐。它们应服务于连续学习，而不是增加孤立互动功能。

## 暂缓方向

独立 Playground、讨论区、排行榜、证书、付费体系、团队/LMS/SSO 集成均不属于近期阶段。
它们需要更大的题库、稳定用户群或组织级需求才能产生价值。并发配额、资源预算和滥用
控制也等到真实公开并发需求出现后单独设计。
