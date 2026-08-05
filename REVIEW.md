# 当前架构审查

本文描述的是当前代码实际形成的架构，而不是 `TODO.md` 中的历史方案。审查重点是所有权、恢复、活性、安全和题库长期演化；单纯的文件数量、表数量或 Deployment 数量不作为问题。

## 当前架构

### 部署与依赖

```text
                                      Model API
                                          ^
                                          | HTTPS
                                          |
Learner / Author Browser -- HTTP/SSE/WS -> Server x1
                                          ^
                                          | fenced internal HTTP:
                                          | claim / renew / typed result
                                          v
                                   Runtime Worker x2

Server x1                                      Runtime Worker x2
  |-- SQL（唯一业务写入口）-> PostgreSQL x1      |-- OCI API -> Registry
  |                            + PostgreSQL PVC  |-- Incus API -> image/build
  |-- file I/O -> Server data PVC (RWO, 20 GiB) |-- Kubernetes API
  |               |-- candidate archive        |     -> verification Environment CRD
  |               |-- staged Catalog source    |-- 无模型、数据库和 Server PVC 凭据
  |               +-- published challenge
  |-- Kubernetes API
  |     |-- learner Environment CRD
  |     +-- Generator workspace PVC
  +-- OpenSandbox API -> Sandbox Pod
                          +-- mount Generator workspace PVC

                              Kubernetes API
                                    |
                         watch / reconcile CRD
                                    v
                              Controller x1
                         +----------+-----------+
                         |                      |
                         v                      v
               NodeEnvironment           VK8sEnvironment
               Incus project             namespace + vcluster
               network + ACL             + management terminal
               system containers
```

图中的固定控制面是：

- `Server` Deployment 单副本：公开 API、Web UI、认证、终端代理、全部 Eino Agent、Catalog installer、Roadmap maintenance、发布 finalizer 和 Generator workspace 生命周期。
- `Controller` Deployment 单副本：只调和 `NodeEnvironment` 与 `VK8sEnvironment`，供应、检查和回收学习/验证环境。
- `Runtime Worker` Deployment 两个副本：通过 Server 内部 API 一次领取一个 fenced action，执行 Build、ArtifactPublishing、Verifying、ChallengePublishing 和 runtime resource reaping。
- `PostgreSQL` StatefulSet 单副本：保存业务状态、工作流、lease、AgentRun、revision、Roadmap 和学习事实。
- 生产 Registry、Incus 和 OpenSandbox 是外部运行时依赖；Kind overlay 额外部署开发 Registry。Environment 是动态资源，不是固定 Deployment。

### 执行归属

```text
交互式：
  Authoring request  -> Server Agent -> 私有 Plan revision
  Assistant request  -> Server Agent -> 只读学习证据 -> session message

作者题目：
  作者确认 Plan
    -> [Server] Generating -> Judging
    -> [Worker] Building -> ArtifactPublishing -> Verifying
    -> [作者] NeedsAuthorReview
    -> [Server] Classifying
    -> [作者] NeedsClassificationReview
    -> [Worker] ChallengePublishing
    -> [Server] source materialization + Roadmap commit
    -> Published

Catalog 基线：
  immutable OCI release
    -> [Server] stage source
    -> [Worker] 每个 Entry: Build -> Publish -> Verify
    -> [Worker] final artifact promotion
    -> [Server] materialize all entries + atomic Roadmap commit
    -> Ready | Failed

Roadmap：
  20 个新题自动请求或手工调试请求
    -> 等待题目执行空窗
    -> [Server] 每个新增 Topic/Challenge: Planner -> 两个 Reviewer
    -> 合并边并发布新的 immutable RoadmapRevision
```

### 权威数据

| 数据 | 当前权威 | 说明 |
| --- | --- | --- |
| 用户、会话、学习事实、AgentRun、Workflow、CandidateRevision、CatalogRelease、RoadmapRevision | PostgreSQL | 只有 Server 直接写数据库；Worker 通过 fenced HTTP 结果间接推进状态。 |
| candidate archive、Catalog staged source、已发布 challenge 文件 | Server data PVC | 数据库只保存路径、hash 和引用；读取题库仍依赖文件存在。 |
| K8s/Node immutable runtime artifact | OCI Registry / Incus | Worker 生产，Controller 和 Environment 消费。 |
| Environment desired/current state | CRD spec/status | Server/Worker 创建 spec，Controller 调和并写 status。 |
| Generator 工作目录 | workflow-owned PVC + OpenSandbox Sandbox | 只是可替换的工作上下文，不是恢复权威。 |

## 总体判断

P0 重构后的主边界是合理的：模型执行统一在 Server，确定性外部操作统一在 Runtime Worker，环境生命周期由 Controller 调和，PostgreSQL state machine 和 lease 代替了额外消息队列。当前复杂度主要来自真实环境、跨 provider 发布和可恢复工作流，而不是无意义的 Deployment 或数据库表。

因此不建议再次做大范围组件合并。P0 的安全、完整性和活性问题已经完成修正；下一步应在积累大量题目之前补齐题目修订生命周期。否则开始积累少量实验题没有问题，但直接积累上百道题后再补内容修订和恢复会明显更贵。

## P1：扩大题库前补齐

### 1. 没有已发布题目的修订、弃用和重新验证生命周期

Catalog Release 是严格 append-only；作者工作流也只会发布一个新 Challenge。当前没有办法修正错误题解、升级存在漏洞的包或基础镜像、替换失效检查点，也没有办法从 Roadmap 中弃用坏题。题库越大，这个缺口越危险。

建议设计最小的 immutable revision 语义：Challenge ID 稳定，每次内容修改产生新 revision；新 revision 必须完整经过 Build/Verify 后才能原子切换为 active；旧 Environment 固定在旧 revision；支持显式 `deprecated`/`superseded`，但不原地改写或立即删除旧 artifact。Catalog 和作者题目应共用这套发布结果，不需要共用生成流程。

### 2. PostgreSQL、Server PVC 与外部 artifact 还没有恢复单元

发布动作已通过 intent、hash 和幂等 finalizer 尽量缩小跨存储窗口，但数据库事务无法原子提交 PVC、Registry 和 Incus。当前文档没有规定 PostgreSQL 与 Server data PVC 如何一致备份、恢复后如何核验，也没有自动从 immutable Catalog bundle 或 Candidate archive 重建 materialized source 的路径。

建议先做简单而明确的契约：PostgreSQL 与 Server data PVC 是同一个备份/恢复单元；恢复后必须运行完整性扫描；Registry/Incus artifact 以 digest/fingerprint 校验。之后若单副本 PVC 成为实际瓶颈，再把 immutable source/archive 迁到对象存储，不应现在额外引入一套存储。

### 3. Catalog Release 的“配置版本”与“当前可服务版本”绑定过紧

Availability gate 只检查配置中的 bundle digest。若已有可用题库后把配置改为一个正在安装或最终失败的新 Release，现有 Catalog 和相关作者操作也会立即返回 503，尽管旧 Roadmap 和题目仍完整可用。

若 Catalog Release 永远只负责空平台初始化，这个取舍可以保留，但必须明确为一次性 bootstrap 契约。若未来用它持续导入 foundation 更新，则应区分 `desired release` 与 `active ready release`：后台安装失败不能使旧 Catalog 下线，只有完整成功后才原子推进 active baseline。

### 4. 发布 finalizer 缺少持久化诊断

Generation promotion 成功后，Server finalizer 如果遇到无法恢复的 materialization/invariant 错误，目前只会每五秒写日志并让 Workflow 永久停在 `ChallengePublishing`。Catalog 在部分 materialize 后失败时，也可能留下未被 Roadmap 引用的目录。状态计数能看到“卡住”，但无法区分等待、瞬时存储错误和确定性冲突。

建议不增加新 workflow state：继续使用现有 state，但持久化 finalizer 的错误类别、最近错误和重试时间；确定性冲突进入已有 `Failed` 并生成精确回收/人工修复证据，瞬时 I/O 错误才继续重试。完整性扫描负责报告或清理孤立 materialization。

### 5. 异步系统的可观测性不足

Server metrics 当前只有 Generation 各 state 数量和 verification Environment 总数；Runtime Worker 只有 up/ready/capability。无法回答 action 等了多久、Catalog/Roadmap 是否卡住、reaper 是否积压、AgentRun 哪个 role 在失败、lease 是否频繁接管。

建议补充低基数指标：各 aggregate/state 数量与最老等待时长、runtime action 成功/失败/耗时、reaper backlog、AgentRun role/status、lease takeover、Catalog configured/active 状态。日志继续携带 workflow/action/environment ID。暂时不需要引入 tracing 平台，但指标必须足以定位工作流停滞。

## P2：由规模触发

### 1. Server 是单点且集中了大量权限

Server 当前同时持有用户流量、模型、PostgreSQL 写权限、Kubernetes CRD/PVC/exec、OpenSandbox、Registry Catalog pull 和 Incus terminal 凭据，并受 RWO PVC 限制只能单副本。这个集中边界简洁，但 Server 故障的影响面很大，任何 rollout 都会中断 active AgentRun。

当前个人项目阶段可以接受。触发拆分或多副本的条件应是明确的可用性目标或实际排队数据；届时先解决共享 immutable source、SSE 接管和最小权限，不要仅复制 Pod。生产前还应收紧 Runtime Worker 的 `0.0.0.0/0:443/6443/8443` egress 和 Controller 的集群级 vcluster RBAC，至少用专用集群/节点与 provider 身份控制爆炸半径。

### 2. Agent 并发两端都缺少边界

Generation AgentRunner 在单 Server 中一次同步处理一个 workflow，慢模型调用会阻塞其他作者；Roadmap 则相反，一次领取所有可用 task，并为每个 task 并发启动 Planner/Reviewer，积压大时会瞬间放大模型请求。

当前题量和用户量下不必增加 pool。先用“最老 Agent state 等待时间”和模型并发指标观察；需要时给 Generation 增加一个小的有界并发值，给 Roadmap claim/goroutine 增加同一个简单 semaphore。两者仍使用现有 PostgreSQL lease，不需要新 Deployment 或通用 executor。

### 3. Controller 的 checkpoint 执行路径会随在线环境线性增长

Node/VK8s Controller 每四秒通过 Incus exec 或 Kubernetes exec 运行检查脚本，最大并发分别为 4 和 2。少量环境下语义简单可靠；几十个持续在线环境后会产生明显控制面调用、排队和日志噪声。

保留 `NEXT.md` 已记录的 checkpoint daemon 方向作为触发式优化：先测量 reconcile duration、checkpoint lag 和 API/Incus error，再决定改为环境内 HTTP 报告。现在不需要提前实现。

## 当前不需要重构的部分

- 约 30 张 PostgreSQL 表按 Agent、Authoring、Generation、Catalog、Roadmap、Environment 和 Identity 分域，表达的是不同持久事实，不是通用任务表膨胀；表数量本身没有问题。
- PostgreSQL state machine + `SKIP LOCKED` + lease 足以承担当前 worklist；再引入 Kafka、Temporal 或独立队列只会产生双重权威。
- Runtime Worker 保持一个 Deployment 是合理的。Build/Publish/Verify/Reap 共享同一套 deterministic action、provider credential 和恢复契约；应先修公平性，而不是重新拆成多个 Worker 类型。
- Environment 作为 CRD 和动态资源而不是 Deployment 是正确边界；Controller 不应重新拥有 Generation/Catalog workflow。
- Generator 使用 Server 创建的 PVC 并让 OpenSandbox 只挂载它，符合 workspace 可替换、业务 revision 才是恢复权威的设计，不需要改成 Eino checkpoint 或 Pod 本地 session。
- Registry 与 Incus 作为外部 artifact/runtime provider 是领域需求，不应为了“所有东西都在 Kubernetes”而强行换成更复杂且未验证的虚拟化方案。

## 建议顺序

1. 在批量生产题目前设计 Challenge immutable revision/deprecation，并写清 PostgreSQL + PVC 的备份恢复契约。
2. 用 5--10 道真实题验证完整内容生命周期和指标。
3. 只有观察到排队、checkpoint lag 或可用性瓶颈后，再做 P2 扩容。
