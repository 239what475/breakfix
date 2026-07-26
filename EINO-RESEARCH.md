# Agent Runtime 迁移设计

> 设计日期：2026-07-26
> 目标：以 Eino、PostgreSQL 和远程 Sandbox 执行平面替换 `eino-claude-code` / Claude Code CLI
> 范围：Agent Runtime、Authoring、Assistant、Generator/Judge、Taxonomy 和 VerifyTask 交接

## 结论

Breakfix 应暂停继续扩展技能图和题库，先完成 Agent Runtime 迁移。Authoring、做题助手、Generator/Judge 和 Taxonomy 都依赖同一套模型、工具和会话基础；继续在 Claude Code session 上扩展只会增加后续迁移成本。

目标架构固定为：

- Eino 是唯一 Agent 引擎，不再为 Claude Code CLI 或第二套 Agent SDK 保留适配层。
- PostgreSQL 是唯一关系数据库，不先为新 Runtime 实现 SQLite 版本。
- Server、Controller 和 Agent Worker 都运行在 Kubernetes 中，但使用三个独立 Deployment。
- PostgreSQL 使用独立 StatefulSet、Service 和 PVC。
- Agent Worker 直接领取和更新 PostgreSQL 中的 `agent_*` Runtime 数据；它不直接读写业务领域表。
- Server 负责 HTTP/WebSocket、领域状态、Agent Session 和用户消息创建、领域工具、artifact 和文件系统题库。
- Controller 只协调 CRD 和 Kubernetes 资源，不访问 PostgreSQL 或 Server 数据目录。
- Agent 的故障恢复以 Run attempt 为边界，不恢复模型隐藏状态，也不精确重放某个 Eino Step。
- 删除 `Generation` CRD、Generation Reconciler 和一次性 generator Job。
- 保留 `VerifyTask`、`ContainerEnvironment` 和 `VClusterEnvironment` CRD。
- VerifyTask 由独立 verifier Job 执行，不再复用 generator 二进制和镜像。
- Generator workspace 使用 OpenSandbox 原生 Kubernetes workload provider；授权、持久化和 fencing 必须先通过 POC，不能假设不存在的 scoped token。

最终状态不保留双运行时、Claude CLI fallback、旧配置兼容、Eino checkpoint、逐 token 持久事件、通用 outbox 或 Sandbox metadata adopt 协议。

## 研究依据

### 当前仓库

- 当前依赖 Eino `v0.9.10` 和 `eino-claude-code v0.2.0`。
- 当前模型通过 DeepSeek Anthropic 兼容入口调用 `deepseek-v4-pro`，并不是 Anthropic 模型。
- Server 和 Controller 已拆成两个二进制，但仍以集群外主机进程运行。
- Server 当前独占本地 SQLite 和 `data_dir`；Controller 通过 kubeconfig 协调集群。
- `cmd/generator` 同时执行 Generation workflow 和 VerifyTask verify 模式。

### 已核对版本

| 项目 | 研究版本 | revision |
| --- | --- | --- |
| Eino 稳定版 | `v0.9.13` | `c5e6aef927cca02bea934541f8dff2ea711b2ca7` |
| Breakfix 当前 Eino | `v0.9.10` | `0abcc824167071616b2a642f06792d15b7b2b23d` |
| Eino Ext DeepSeek | `v0.1.7` | `9137edd89e72b72735ede69db1c5ae29178a6e41` |
| Eino Ext local backend | `v0.2.6` | `9473ae28db1f31272ef8a16cfd2dfb14f6169d23` |
| OpenSandbox Go SDK | `v1.0.5` | `e9d0a63919739b1bed05914373acbacb11e37d43` |

迁移时统一升级到已经探测过的 Eino `v0.9.13`，但版本升级和业务迁移必须分步骤定位问题。

### 真实模型探测

使用当前 DeepSeek Key 和 Chat Completions 入口完成了以下真实探测，没有记录密钥：

| 能力 | 结果 |
| --- | --- |
| Thinking + `tool_choice=auto` | 成功返回合法 tool call 和 JSON 参数 |
| Thinking + 强制 `tool_choice` | HTTP 400，不受支持 |
| `response_format: json_object` | 返回合法 JSON，但不保证领域 schema |
| Eino DeepSeek adapter 流式调用 | 成功 |
| Eino `ChatModelAgent` 流式工具循环 | 成功产生模型、工具和最终正文事件 |
| Eino `DeepAgent` 文件工具循环 | 成功完成真实 write/read |

这些探测只证明接口和工具循环可用，不证明长时间稳定性、OpenSandbox 集成或题目生成质量。

## 当前实现需要修正的问题

### Claude session 无法跨 Generation Job 恢复

当前 Server 向新 Generation Job 传递相同 Claude session ID，但 Claude session 数据保存在上一个 Pod 的本地磁盘。Job 没有 session volume，完成后还会被删除，因此 `--resume` 只有 ID，没有可恢复的数据。

迁移后不再依赖供应商 opaque session。长期对话保存在 PostgreSQL；immutable artifact 记录已经提交验证的候选，Sandbox workspace 只是当前技术执行的可丢弃工作副本。

### VerifyTask 失败后删除了最有价值的候选

当前验证失败会删除 submission，再启动新的 Generation Job，导致下一轮只能从空目录或旧的已验证版本开始。

迁移后保留最新失败 artifact，直到替代候选验证成功、作者取消或保留期结束。只有 artifact 类验证错误才把该候选和结构化报告交给下一 Generator Run。

### VCluster Generator 会接受半成品

当前 vcluster 流程可能在必需文件存在且一段时间没有事件后主动取消 Agent，并把 cancellation 或其他 Agent error salvage 为成功。

迁移后必须由 Agent 正常结束后才能进入确定性结构校验和 Judge。timeout、cancellation、模型错误或工具错误都不能根据文件存在情况兜底。

### 技术失败和语义修订混在同一个循环

模型传输错误、工具协议错误、Judge reject 和 VerifyTask failure 目前可能都直接进入下一生成 round。

目标语义分为：

- 模型传输错误：当前 attempt 内有限网络重试。
- typed result 协议错误：结束当前 attempt，同一 Run 重新执行。
- Judge reject：同一 Generator Run 内的语义修订。
- VerifyTask artifact 错误：创建同一 Generator Session 的下一 Run。
- VerifyTask infrastructure 错误：使用同一 artifact 重新验证，不让 Agent 修改题目。

### `lab_*` 隔离和权限边界不足

当前 Generator 可以扫描、执行和删除 namespace 中的 lab Pod，并与 Verify Job 共用 privileged Job 模板和 ServiceAccount。并发 Generation 之间可以互相影响。

迁移后删除 `lab_create/exec/checkpoints/logs/destroy`。Generator 只操作绑定当前 Session 的 OpenSandbox workspace；Agent Worker 没有 kubeconfig、Pod exec、VerifyTask 或 Environment 权限。

### Assistant 和 Authoring 的运行状态不持久

Assistant 的 running Turn、partial content 和 subscriber 只存在于 Server 内存。Authoring 的每个 Plan 工具又立即发布 Revision，导致 Agent 中途失败时留下部分修改和无回答消息。

迁移后用户消息、Agent Session 和 Run 都进入 PostgreSQL。Authoring 工具只修改私有 staged Plan，一个成功 Run 最多提交一个公开 Revision。Worker 故障只重启当前 attempt。

### 模型输出契约过于脆弱

Judge 仍依赖 `PASS` / `FAIL` 文本，Taxonomy schema、prompt 和 Go 类型又重复描述同一结构。迁移后严格结果全部使用 typed result tool 和 Go domain validator；纯文本、未知字段、缺失字段和非法枚举都明确失败，不解析近似输出。

### 日志包含模型和工具内容

迁移后的 telemetry 只记录模型、prompt version、token、耗时、首 token 延迟、工具名、工具次数和错误类别。不得记录 reasoning、完整 prompt、工具参数、工具结果或模型正文。用户可见消息和 artifact 写入各自权威存储，而不是日志。

## 集群部署

### 资源拓扑

```text
                         Ingress
                            |
                            v
                 Service/breakfix-server
                            |
                            v
                 Deployment/breakfix-server
                    |          |          |
             domain SQL   Kubernetes   artifact/files
                    |          API          |
                    v                       v
        Service/postgresql          PVC/server-data
                    |
                    v
            StatefulSet/postgresql
                    ^
                    | agent_* SQL
                    |
         Deployment/breakfix-agent-worker
              |              |
              v              v
        DeepSeek API   Service/opensandbox
                             |
                             v
                    Sandbox Pod / PVC

         Deployment/breakfix-controller
                    |
                    v
       VerifyTask / Environment CRD
                    |
                    v
              verifier Job
```

这些资源由同一个 Helm Chart 或等价部署包安装，但不能合并成一个 Kubernetes Deployment：

- Server 初期一个副本，挂载 RWO `server-data` PVC，拥有题库、artifact 和 CA 文件；Deployment 使用 `Recreate` 更新策略，不能依赖同一 PVC 上的双 Pod 滚动更新。
- Controller 初期一个副本；启用 leader election 后可以安全滚动升级或增加副本。
- Agent Worker 使用独立副本数，按 Agent Run 队列扩缩容。
- PostgreSQL 使用独立 StatefulSet 和 PVC，不作为 Server sidecar。
- verifier 是 VerifyTask 按需创建的一次性 Job。

一个 Deployment 只能表达一组同构 Pod。把 Server、Controller 和 Worker 放入同一个 Pod 会绑定三者的扩缩容、发布和故障边界，增加 Worker 时也会无意义地增加 Server 和 Controller。

### 网络

- Browser 只访问 Server 的公开 Service/Ingress。
- Worker 通过 PostgreSQL Service 访问 `agent_*` 表，通过 Server ClusterIP 调用领域工具和提交 artifact。
- Server 和 Worker 都通过 ClusterIP 访问 OpenSandbox。
- Controller 通过 Kubernetes API 协调 Breakfix CRD。
- verifier Job 通过 Server ClusterIP 下载 immutable submission。
- PostgreSQL、OpenSandbox 和 Server 内部接口不创建公网入口。

### 持久存储

- PostgreSQL PVC：关系数据。
- Server data PVC：`challenges/`、submission、verified artifact 和 CA 材料。
- Sandbox PVC：单个 Generator Session 的临时 workspace。
- Environment 存储：由 ContainerEnvironment 或 VClusterEnvironment 自己管理。

文件系统 challenge catalog 仍是发布题目的权威来源。Server 使用单副本和可写 PVC；未来需要多个 Server 时，再单独设计 RWX 文件系统或对象存储，不在本阶段隐式解决。

## 数据所有权

| 数据 | 权威来源 | 写入者 |
| --- | --- | --- |
| Agent 对话 | PostgreSQL `agent_sessions` / `agent_messages` | Server 和 Agent Worker；Worker 只写最终助手消息 |
| Agent 执行队列和租约 | PostgreSQL `agent_runs` | Server 和 Agent Worker |
| Authoring Plan、Revision、可见 artifact 状态 | PostgreSQL Authoring 领域表 | Server |
| Taxonomy WorkItem、Candidate、Review、Round | PostgreSQL Taxonomy 领域表 | Server |
| 用户、学习记录和产品统计 | PostgreSQL 业务表 | Server |
| 已发布 challenge | Server data PVC 的 `challenges/` | Server |
| submission 和 verified artifact | Server data PVC | Server |
| Generator workspace | OpenSandbox / Sandbox PVC | Agent Worker 通过 OpenSandbox |
| VerifyTask 和用户环境 | Kubernetes CRD status | Controller 和 verifier |

“领域表”只是业务表的统称，不是另一种数据库。领域表保存 Plan、Revision、WorkItem 等产品事实；`agent_*` 表保存跨领域复用的对话和执行事实。

同一份消息只能有一个权威来源。Authoring 和 Assistant 领域表只引用 `agent_session_id`，不再各自保存一套重复的 Agent 消息。

Controller 不连接 PostgreSQL，也不读取 Server data PVC。Agent Worker 不直接读写 Authoring、Taxonomy、用户或发布题目表；所有领域工具都调用 Server 内部 API。

## PostgreSQL Agent Runtime

### 数据模型

首轮只需要三个通用表：

- `agent_sessions`：长期对话或 Generator 修复链，包含 purpose、owner kind/ref、user ref、状态和时间戳。
- `agent_messages`：Session 内已确认的用户、助手和平台反馈消息，使用单调 sequence；system prompt 按版本重建，不作为伪用户消息保存。
- `agent_runs`：一次逻辑 Agent 请求，包含可选 session、owner kind/ref、状态、输入 revision、deadline、model、prompt version、attempt、`next_attempt_at`、lease owner、lease expiry、最后错误和时间戳。

本阶段不增加 `agent_steps`、`agent_events` 或 `agent_checkpoints`。工具调用过程不是长期会话权威，Eino opaque checkpoint 也不进入数据库。

Authoring 和 Assistant 的用户消息与 Run 在同一个 PostgreSQL 事务中创建。消息在 Run 成功后成为完整对话的一部分；Run 最终失败或取消时，UI 明确显示失败状态，不能把它伪装成已回答消息。

Taxonomy Mapper/Reviewer 可以创建没有 Agent Session 的独立 Run，因为它们的完整上下文已经存在于 WorkItem、Candidate 和 Review 中。Judge 是 Generator Run 内部的 typed model invocation，也不创建独立 Session。

### Session、Run 和 attempt

```text
Session
  long-lived conversation or repair lineage
  |
  +-- Run
        one logical user request or autonomous task
        |
        +-- attempt 1: worker execution
        +-- attempt 2: retry after failure
```

- Session 表示长期对话，例如一次 Authoring 会话或 Generator 修复链。
- Run 表示一个逻辑任务，例如处理一条用户消息或生成一个候选。
- attempt 表示某个 Worker 对同一 Run 的一次技术执行，不建立独立表，只使用 `agent_runs.attempt` 计数。

“同一 Session 只允许一个 active Run”只适用于 Authoring 和 Assistant 等顺序对话，由领域服务在创建 Run 时保证。Taxonomy 的两个 Reviewer 是同一 WorkItem 下的两个独立 Run，可以并行。

### 领取和租约

Agent Worker 直接使用 PostgreSQL `FOR UPDATE SKIP LOCKED` 原子领取 `status=pending AND next_attempt_at <= now()` 的 Run。领取时：

- 状态转为 running。
- `attempt` 加一。
- 写入随机 lease owner 和 lease expiry。
- Worker 定期续租。

Worker 对 `agent_runs` 的状态更新必须匹配当前 run ID、attempt 和 lease owner。Server 处理领域工具时也查询当前 attempt；已经失去租约的旧 Worker 不能继续修改领域状态。

技术失败需要重试时，Worker 清空 lease、把 Run 恢复为 pending，并原子写入新的 `next_attempt_at`。退避由 PostgreSQL 状态表达，Worker 不持有 lease 原地 sleep，也不依赖进程内定时器。

lease 同时是 Sandbox 写入的 fencing boundary。任何续租失败都立即取消当前 Agent context；`OpenSandboxBackend` 随后拒绝新操作并取消仍在执行的远程命令。每次 files/exec 请求都绑定 run ID 和 attempt：经过 Server 代理时由 Server 校验当前 lease；直接访问只允许使用可撤销、短时且绑定当前 attempt 的官方凭据。POC 如果不能满足该约束，就必须使用 Server 代理，不能把仅绑定 Sandbox 的长期凭据交给 Worker。

Run deadline 是整个逻辑任务的上限。模型网络重试和 Worker attempt 都不能延长 deadline。

### 故障语义

- Browser 断开：Run 继续。
- Server 重启：Worker 仍可执行模型和更新 PostgreSQL；需要领域工具时等待 Server Service 恢复或结束当前 attempt。
- Worker 优雅退出：停止领取新 Run，并在 Kubernetes termination grace 内等待当前 attempt 完成。
- Worker 突然退出：lease 过期后由另一个 Worker 领取同一 Run，`attempt` 加一。
- Worker 续租失败：立即取消模型和 Sandbox 操作；旧 attempt 不能等 lease 到期后继续执行。
- 模型提供商响应流中断：当前 attempt 失败，丢弃 partial draft，下一 attempt 重新调用模型。
- Worker 到 Server 的 delta 转发中断：模型调用和当前 attempt 继续；只丢弃无法送达的 partial draft，不能因为展示通道失败而取消模型调用。
- 远程 shell 结果不确定：不精确重放该 tool call；下一 attempt 读取当前 workspace 并重新判断。
- deadline 到期、用户取消或领域 revision 已改变：Run 终止，不再领取。

下一 attempt 从已确认 Agent 消息、当前领域状态、Plan、VerifyTask report 和当前 Sandbox workspace 重建输入。同一 Run 的技术重试复用工作副本；新的语义 Run 使用对应 immutable artifact 重置工作副本。它不恢复模型 reasoning、HTTP stream、Eino graph state 或某个隐藏 CLI session。

### 流式输出

Worker 将模型 delta 作为瞬时流发送给 Server，Server 只转发给当前浏览器订阅者。PostgreSQL 不逐 token 保存 delta。Worker 到 Server 或 Server 到 Browser 的瞬时流失败只影响当前草稿展示，不能取消仍在继续的模型调用。

Run 的完成责任按是否提交领域状态划分：

- Assistant 等只读 Run 由 Worker 在一个 PostgreSQL 事务中保存最终非空回复并完成 Run。
- Authoring、Taxonomy 等需要提交 PostgreSQL 领域状态的 Run，由 Worker 调用 Server finalize API；Server 在一个事务中提交 Revision/Candidate/Review、最终消息和 Run 完成状态。
- Generator 先通过幂等 Server API 完成 submission 和 VerifyTask 交接，再由 Worker 原子保存最终消息并完成 Run；若 Worker 在两步之间退出，下一 attempt 使用同一 submission ID 重放交接后收敛。

浏览器重连时：

- completed Run 直接读取最终消息。
- running Run 清空旧 partial draft 并重新显示运行状态。
- 后续新 delta 继续实时展示。

Server 或浏览器在流式过程中重启可能丢失未完成草稿，但不会丢失用户消息、Run、最终回复或业务状态。这是首轮明确接受的边界。

## Eino 与模型调用

### 配置

迁移后的配置直接表达 Chat Completions provider：

```yaml
agent:
  base_url: https://api.deepseek.com
  api_key_env: DEEPSEEK_API_KEY
  model: deepseek-v4-pro
  request_timeout: 2m
```

首轮只使用 `deepseek-v4-pro`，不同时引入 fast model 路由。最终删除 `haiku_model`、`effort`、`ANTHROPIC_*` 和 `CLAUDE_CODE_*`。

### typed result

Judge、Mapper 和 Reviewer 保持 Thinking，并只暴露唯一 `submit_*` 结果工具，使用 `tool_choice=auto` 和 `ReturnDirectly`。

- 纯文本结束、没有调用、重复调用或非法参数都是协议错误。
- 参数由命名 Go 类型生成 schema，并使用严格 JSON 解码。
- 未知字段、第二个 JSON 文档、缺失必填值和非法枚举必须拒绝。
- 不解析 Markdown、不剥离代码围栏、不补默认值，也不把近似文本当成成功。

协议错误是技术失败：结束当前 attempt，在 Run deadline 内按领域退避重新执行，不创建新的语义 round。

### 重试

- 模型传输最多重试 3 次，只覆盖明确的 429、5xx、连接中断和临时超时。
- 4xx 参数错误、schema 错误、工具业务错误和 context cancellation 不作为网络错误重试。
- 工具参数错误可以作为 tool result 返回当前 Agent，让模型在 `MaxIterations` 内修正。
- Agent 最终没有满足 typed result 协议时结束 attempt。
- 需要新 attempt 的技术错误写入 `next_attempt_at`；claim 查询只领取已经到期的 Run。
- Taxonomy 的语义 round 和技术失败预算继续由 Taxonomy service 管理。
- Generator Run 和 verifier Job 都保持一小时 deadline；verifier Job 使用 `activeDeadlineSeconds`，不在 VerifyTask 上再实现一套重试 deadline。

## OpenSandbox 执行平面

### 采用边界

OpenSandbox 用于 Generator workspace 的文件、命令和生命周期；用户做题的 ContainerEnvironment、VClusterEnvironment 和 VerifyTask 运行环境不迁移到 OpenSandbox。

首选路径固定为 OpenSandbox 原生 Kubernetes workload provider，POC 只验证这一条路径：

- 固定实际验证通过的 OpenSandbox Server、SDK、`execd` 和原生 provider 版本。
- 不使用 `latest`。
- 真实验证 create、files、streaming exec、PVC workspace、TTL/delete、NetworkPolicy 和 run/attempt fencing。
- OpenSandbox Server 或 Agent Worker 重启后，同一 workspace 仍可访问；删除后 Pod、PVC 和路由资源全部回收。
- 全部硬性要求通过后立即固定该 provider，不再测试或实现第二套 provider。

Kubernetes SIG Agent Sandbox 不是本阶段的并行方案。只有 OpenSandbox 原生 provider 明确无法满足某个硬性要求时，才另开设计决策评估 OpenSandbox 的 `workload_provider = "agent-sandbox"` 适配；当前迁移不安装其 CRD/Controller，也不建立双 provider 抽象、配置或测试矩阵。

### 授权门槛

Server 持有 OpenSandbox lifecycle credential，Agent Worker 不持有全局生命周期凭据。

文档不假设 Server 可以自行签发 OpenSandbox token。POC 必须确认官方是否提供绑定单 Sandbox 的 data-plane connection material：

- 如果支持，Server 将官方 connection material 临时交给当前 Worker。
- 如果不支持，files/exec 通过 Server 的受控代理访问。
- 禁止把 OpenSandbox 全局 API key 交给 Agent Worker。

该选择只影响 `OpenSandboxBackend` 的连接方式，不改变一个 Generator Session 对应一个 workspace 的业务模型。

### Sandbox 生命周期

1. Server 为 Generator Session 创建 pending workspace record。
2. Server 调用 OpenSandbox create，成功后保存 opaque sandbox ID。
3. 如果 Server 在 create 成功后、保存 ID 前崩溃，该 Sandbox 由 OpenSandbox TTL 回收；不进行 metadata 查询和 adopt。
4. Worker 使用该 Session 的 workspace 执行文件和命令工具。
5. 同一 Run 的 Worker attempt 失败时复用相同 sandbox ID 和当前工作副本。
6. VerifyTask artifact 错误时保留 immutable failed artifact；下一 Generator Run 开始前用它原子重置 workspace，再把结构化 report 交给 Agent。
7. 验证成功后 Server 删除 Sandbox；作者审核后要求修改时，新 Generator Session 创建新 Sandbox，并用上一份 verified artifact 初始化。
8. 作者取消或 Session 到期后，Server 删除 Sandbox；TTL 是 orphan 最终回收保证。

Sandbox 使用专用 authoring 镜像，不包含模型 Key、PostgreSQL 凭据、Server 内部凭据、Registry 写凭据、kubeconfig 或 ServiceAccount token。

### Eino backend

`OpenSandboxBackend` 只实现 Eino DeepAgent 实际需要的能力：

- read/write/edit 映射到 OpenSandbox files API。
- glob/grep 优先使用官方原生能力；没有原生能力时使用固定 argv 的 Sandbox 内命令，不把整个 workspace 拉回 Worker。
- streaming execute 映射到 OpenSandbox command API，并固定工作目录为 `/workspace/challenge`。
- Backend 在构造时绑定当前 Sandbox，工具参数不能选择其他 sandbox ID、Pod 或 namespace。

通用 shell 只存在于隔离 Sandbox 内，不能退化为 Agent Worker 本地 shell。

Generator 关闭 DeepAgent 默认 todo 和 general sub-agent：

```text
WithoutWriteTodos = true
WithoutGeneralSubAgent = true
Backend = OpenSandboxBackend
StreamingShell = OpenSandboxBackend
```

## Workflow 迁移设计

### Authoring

`authoring_sessions` 保存产品状态并引用一个 `agent_session_id`；对话正文只保存在 `agent_messages`。

每次用户消息：

1. Server 在 PostgreSQL 事务中保存用户消息、检查唯一 active Run，并创建带 base revision 的 Authoring Run。
2. Server 为该 Run 初始化私有 staged Plan。
3. Worker 领取 Run，加载已确认历史、当前 staged Plan 和本次消息。
4. 领域工具通过 Server API 修改 staged Plan，不发布公开 Revision。
5. attempt 失败后，新 Worker 读取当前 staged Plan 并重新执行 Agent。
6. Agent 正常返回非空正文后，Server 校验 base revision 和完整 Plan。
7. 最终事务最多创建一个 Revision，同时保存助手消息并完成 Run。

未成功完成的 Run 不发布 staged Plan，不补默认回复，也不把部分工具执行伪装成一次成功对话。

### 做题助手

Assistant 使用 `ChatModelAgent` 和现有只读工具：

- `get_terminal_scrollback`
- `get_checkpoint_status`
- `list_environment_files`
- `read_environment_file`
- `get_solution`

终端 scrollback 只通过工具按需读取，不自动塞入上下文。Worker 显式加载该 Agent Session 的已确认消息和最新环境摘要；需要当前事实时重新调用工具，不把旧终端结果当作当前状态。

模型 delta 使用瞬时流，最终 Markdown 回复进入 `agent_messages`。Worker 或 Server 故障时可以丢弃 partial draft，但同一 Run 会通过新 attempt 重新生成最终回复。Assistant 没有写文件或执行命令的工具。

### Taxonomy Committee

Taxonomy WorkItem、Candidate、Reviews、Round、技术失败预算和 Publisher 继续由领域表管理。

- Mapper invocation 是一个无 Session Agent Run。
- Curriculum Reviewer 和 SRE Reviewer 是两个独立 Run，可以并行。
- Mapper 使用 `submit_changeset(ChangeSet)`。
- Reviewer 使用 `submit_review(Review)`。
- Publisher 不调用模型，继续使用 PostgreSQL 事务原子发布。

Worker attempt 失败不能增加 semantic round。只有两份合法 Reviewer 结论中存在 reject 时，Mapper 才进入下一语义 round。

### Judge

Judge 是 Generator Run 内部的只读 typed invocation，不创建独立 Session，也不提供 filesystem 或 shell tool。

```text
submit_judgement:
  decision: pass | reject
  feedback: string
```

- pass 时 feedback 必须为空。
- reject 时 feedback 必须非空且具体。
- 只能提交一次；没有调用就是协议失败。
- prompt 审核题面、solution、检查点、技术正确性、元数据和 Dockerfile 的一致性。

### Generator

一个 Generator Session 对应一个 Agent Session 和一个 OpenSandbox workspace。workspace 是技术执行的工作副本，immutable candidate 才是已经提交给 Judge 和 VerifyTask 的语义基线。VerifyTask artifact 错误会在同一 Session 创建下一 Run，但该 Run 必须先用失败 candidate 重置 workspace，不能同时把两份文件状态当作输入。

单个 Generator Run：

1. 加载当前 Plan、已确认 Agent history 和结构化 VerifyTask feedback；artifact 文件本身不作为一份额外模型上下文输入。
2. 首轮初始化空 workspace；作者修改已验证方案时从上一份 verified artifact 初始化；VerifyTask artifact 修复轮从对应 failed artifact 原子重置。
3. 同一 Run 的技术 attempt 重试沿用当前 workspace，并从实际文件状态继续判断。
4. DeepAgent 通过 OpenSandbox 修改文件并执行创作期实验。
5. Agent 必须正常结束；timeout、cancellation 或工具错误结束当前 attempt。
6. Worker 通过 files API 生成规范化 tar.gz，并运行 manifest 和 semantic 校验。
7. Judge 审核同一份 immutable candidate。
8. Judge reject 时在同一 Run 内继续语义修订，并生成新的 immutable candidate 供下一次 Judge 审核。
9. Judge pass 后，Worker 使用由 Run 派生的确定性 submission ID 调用 Server。
10. Server 原子保存 artifact，并 create-or-get 由 submission ID 派生名称的 VerifyTask。
11. Server 返回 VerifyTask ref 后，Worker 完成当前 Generator Run；Worker 不等待真实验证结束。

Server、Worker 或网络在提交短请求中断时，下一 attempt 使用同一个 submission ID 重试。已经存在的 artifact 和 VerifyTask 被直接采用，不需要 outbox。

### VerifyTask

VerifyTask 继续使用 CRD，因为它表达的是需要 Controller 持续协调的 Kubernetes 复合任务，而不是模型计算。

每个 immutable submission 只对应一个确定名称的 VerifyTask。Generator 是唯一提交来源，因此 CRD 不保留恒为 `agent` 的 source kind，只记录实际 Agent Run ref：

```yaml
spec:
  source:
    ref: <agent-run-id>
  submission:
    id: <deterministic-submission-id>
```

```text
VerifyTask CRD
  -> Controller create-or-gets one deterministic verifier Job
  -> verifier downloads immutable submission
  -> build and publish temporary image
  -> create ContainerEnvironment or VClusterEnvironment
  -> run answer
  -> run checkpoints
  -> update VerifyTask status
  -> cleanup environment and failed image
```

`cmd/verifier` 和 verifier 镜像只包含真实验证所需的 BuildKit、Kubernetes 和 challenge 逻辑，不包含 Claude Code、Eino 或模型 Key。verifier 使用独立 Job 模板和 ServiceAccount，不能与 Agent Worker 共用权限。

VerifyTask phase 只使用 `Pending`、`Running`、`Failed` 和 `Succeeded`。失败类别写入 `status.report.class`，不引入 `Failed/artifact` 这类复合 phase：

- `status.phase=Failed, status.report.class=artifact`：artifact 构建、answer 或 checkpoints 的确定性问题。
- `status.phase=Failed, status.report.class=infrastructure`：Server、Registry、Kubernetes 或环境供应故障。

分类依据是失败原因，不是执行阶段。例如 Dockerfile 语法错误属于 `artifact`，构建时 Registry 超时仍属于 `infrastructure`。

每个 VerifyTask 只对应一个由 VerifyTask 名称派生的确定名称 verifier Job。重复 reconcile 只能 create-or-get 同一个 Job，不在 VerifyTask status 中维护另一套 attempt。

Controller 和 verifier 的 status 写入边界固定为：Controller 写入 `Pending -> Running`、Job 引用以及 `phase=Failed, report.class=infrastructure`；verifier 只写入 `phase=Succeeded` 或 `phase=Failed, report.class=artifact` 及其 report。任何 terminal status 都不可被后续 Pod 或 reconcile 覆盖。

Job 内所有有副作用的资源都由 VerifyTask ID 确定命名。临时镜像使用固定 tag；ContainerEnvironment 或 VClusterEnvironment 使用固定名称，Environment 和 Job 都持有 VerifyTask owner reference。重试 Pod 启动时先删除上一 Pod 遗留的 Environment，等待删除完成后再以同名资源创建干净环境，不能复用已经执行过 answer 的环境，也不能创建随机重复资源。Controller 在 terminal 状态清理 Environment 和失败临时镜像，Job 由 TTL controller 延迟删除以保留日志；VerifyTask finalizer 对删除或取消路径执行相同兜底清理。验证成功的镜像作为结果保留。

verifier 的退出语义是：

- 成功得出“题目通过”结论：写入 `Succeeded` 和 report，进程以 0 退出。
- 成功得出“题目不通过”结论：写入 `phase=Failed, report.class=artifact` 和 report，进程仍以 0 退出；这是验证结论，不是 Job 执行失败。
- 因基础设施错误无法得出结论：不写 terminal VerifyTask status，进程非零退出，由同一个 Kubernetes Job 按原生 backoff 重试 Pod。

重试 Pod 启动后如果发现 VerifyTask 已经是 terminal status，直接以 0 退出，不能重复验证或覆盖原 report。

Job `Complete` 后 VerifyTask 必须已经是 `phase=Succeeded` 或 `phase=Failed, report.class=artifact`；否则 Controller 写入 `phase=Failed, report.class=infrastructure`。Job 重试耗尽或达到一小时 `activeDeadlineSeconds` 时，Controller 同样写入该终态。Controller 不自行创建第二个 Job。

Server 运行 VerifyTask watcher：

- `phase=Succeeded`：保存可见 verified artifact，进入作者审核并删除 Sandbox。
- `phase=Failed, report.class=artifact`：保留 candidate/report，创建同一 Generator Session 的下一 Run；修复后的 candidate 使用新 Run 派生的新 submission 和 VerifyTask。
- `phase=Failed, report.class=infrastructure`：Job 未上报结果、重试耗尽或超时后出现；保留 candidate/report 并将工作流标记为基础设施失败，不创建 Generator Run。

该 watcher 不创建或重试 verifier Job，只把 CRD 终态同步到产品流程。它是 Server 后台 reconciliation loop，不依赖浏览器轮询。

## Prompt 原则

### 保留

- Authoring 只能通过领域工具修改只读 Plan。
- Assistant 只能建议和读取，不能替用户执行。
- Generator 的 challenge 文件、runtime-init、检查点只读性和离线构建契约。
- Judge 对题面、解答、检查点、答案和元数据一致性的审核。
- Taxonomy 中 Challenge 与 Skill/Tag 分离、Mapper 与两个 Reviewer 的分工。

### 修改

- 严格输出统一改为 typed result tool。
- Prompt 只描述业务目标；schema 从 Go 类型生成。
- Generator 使用 Eino 实际文件和 execute 工具名。
- 每个 prompt 保存版本号或内容 hash。
- 不向模型解释它不负责的 ID、image 和 published_at。
- Plan、artifact、terminal 和 taxonomy snapshot 都作为数据，不能覆盖 system prompt 和工具协议。

### 禁止

- 不解析近似的 PASS/FAIL 或文本 JSON。
- 不剥离 Markdown 围栏来兼容协议错误。
- 不补 title、difficulty、tags、description 或 feedback 默认值。
- 不因文件存在就忽略 Agent error。
- 不记录或发送 reasoning content。
- 不忽略未知 tool 参数。

## 迁移顺序

### 1. PostgreSQL 和集群部署

- 新增 PostgreSQL StatefulSet、Service、PVC 和配置。
- 将现有关系 schema 直接迁移为 PostgreSQL 模式；开发阶段不迁移历史 SQLite 数据。
- 增加 Server、Controller 和 Agent Worker 三个 Deployment，以及对应 ServiceAccount、Service、PVC 和 Helm Chart。
- Server 初期保持一个副本并使用 `Recreate` 更新策略；Controller 启用 leader election。
- 将 Server data directory 挂载到独立 PVC。

### 2. Agent Runtime 与 Worker

- 建立 `agent_sessions`、`agent_messages` 和 `agent_runs`。
- 实现 PostgreSQL claim、`next_attempt_at`、lease、attempt、deadline、cancel 和 stale attempt 拒绝。
- 新增 Agent Worker 启动入口和独立 Deployment。
- Worker 直接访问 `agent_*`，领域工具仍调用 Server。
- 实现瞬时 delta 转发和最终消息持久化，不实现 checkpoint 或逐 delta event store。

### 3. Eino 基础与 Judge

- 引入 Eino DeepSeek adapter 和 Chat Completions 配置。
- 建立 model factory、有限网络重试、strict typed result helper 和无内容 telemetry。
- 先迁移 Judge，验证 Thinking + auto tool + ReturnDirectly 的严格协议。

### 4. 做题助手

迁移 Assistant Agent Session、消息、Run、只读工具和瞬时流式输出。验证浏览器断线、Server 重启和 Worker attempt 重试。

### 5. Authoring

迁移 Authoring Agent Session、私有 staged Plan、领域工具和最终单 Revision 提交。删除进程内 Claude session 和 session mutex。

### 6. OpenSandbox、Generator 和 VerifyTask

- 只对 OpenSandbox 原生 Kubernetes workload provider 完成授权、files/exec、PVC、TTL、NetworkPolicy、fencing 和删除语义 POC。
- 建立带 run/attempt fencing 的 `OpenSandboxBackend` 和 Generator Agent Session/Run。
- 拆出 `cmd/verifier`、verifier 镜像、Job 模板和 RBAC。
- 实现确定性 submission/VerifyTask/Job/Environment 命名、终态清理和 Server VerifyTask watcher。
- VerifyTask 删除恒定 source kind，增加 `report.class`，并固定 Controller/verifier status 写入边界。
- 先通过 container 题，再通过 vcluster 题。
- 切换后删除 `Generation` CRD、Reconciler 和 generator Job。

### 7. Taxonomy

迁移 Mapper 和两个 Reviewer Run，保留现有 WorkItem durable workflow 和 Publisher 事务。

### 8. 删除 Claude Runtime

删除：

- `github.com/239what475/eino-claude-code`
- Claude Code CLI 和 Node generator 镜像内容
- `AgentSessionID`、`WorkflowSessionID`、`ResumeAgent` 和 taxonomy Claude session 字段
- `ANTHROPIC_*`、`CLAUDE_CODE_*`、`haiku_model` 和 `effort`
- quiescence、salvage 和 Claude event 兼容逻辑
- `lab_create/exec/checkpoints/logs/destroy`
- Generation API、CRD、Controller、Job 和相关 RBAC
- `cmd/generator` 中的 Agent generation 模式

不删除 verifier 能力；它已迁移到独立 `cmd/verifier` 和镜像。

## 验收门槛

### PostgreSQL 与 Runtime

- 三个 Worker 并发领取同一 Run 时只有一个成功。
- Worker 崩溃后 lease 到期，同一 Run 的 attempt 加一并由其他 Worker 领取。
- 旧 attempt 不能更新 Run，也不能调用成功的领域写工具。
- Run deadline、取消和 `next_attempt_at` 退避在 Worker 重启后仍然有效。
- Worker 续租失败后，旧 attempt 不能继续修改 Sandbox；新 Worker 接管时不存在仍由旧 attempt 执行的远程命令。
- PostgreSQL 中不存在 Eino checkpoint、逐 delta event 或供应商 opaque session。
- Controller 不持有 PostgreSQL 凭据。
- Worker 到 Server 的 delta 通道中断时，仍在继续的模型调用不被取消，最终消息只保存一次。
- 领域 finalize 故障注入不能产生“领域状态已提交但 Run 未完成”或相反的半提交状态。

### 集群部署

- Server、Controller 和 Worker 使用三个独立 Deployment，由同一个部署包安装。
- PostgreSQL 使用独立 StatefulSet/PVC，Server data 使用独立 PVC。
- Server、Worker、Controller 和 PostgreSQL 可以独立升级和重启；Server 使用 `Recreate`，不要求 RWO data PVC 支持双 Pod 滚动更新。
- Controller leader election 和 CRD reconciliation 正常。
- Worker 和 Server 可通过 ClusterIP 访问 PostgreSQL、OpenSandbox 和内部 API。
- 集群只安装实际选定的 OpenSandbox 原生 workload provider，不安装 SIG Agent Sandbox CRD/Controller。

### 模型和 typed result

- 使用真实 `deepseek-v4-pro` 验证 streaming、auto tool 和 typed result。
- Judge、Mapper、Reviewer 纯文本结束时明确失败，不解析兜底。
- 未知字段、缺失字段和非法枚举全部拒绝。
- 429、5xx、超时和流中断不会被误判为语义 reject。
- 连续 20 次真实调用作为显式 soak test，不作为每次日常测试的硬门槛。

### 做题助手验收

- 启动真实 `cleanup-logs` 环境，只通过浏览器键盘输入终端命令。
- Assistant 通过工具读取真实 scrollback、检查点、文件和 solution。
- Markdown 最终回复正确渲染。
- 浏览器断开不取消 Run；重连时允许 partial draft 重置，最终消息不能丢失或重复。
- Worker 中途退出后新 attempt 最终只产生一条完成消息。

### Authoring 验收

- 多轮对话形成 Plan，一个成功 Run 最多发布一个 Revision。
- Worker 在多次 Plan 工具调用之间退出后，新 attempt 从 staged Plan 继续。
- 失败 Run 不发布中间 Revision，也不补默认回复。
- 两轮对话之间重启 Server 和 Worker 后，历史和当前 Plan 仍然存在。
- 作者只能看到 VerifyTask 成功后的 artifact。

### Generator、Judge 和 VerifyTask

- 使用 `cleanup-logs` 的明确 Plan 完成真实全流程，不使用 stub challenge、假 VerifyTask 或预制成功结果。
- DeepAgent 通过真实 OpenSandbox files/command API 生成所有文件。
- Judge 通过唯一 typed result tool 审核。
- Worker 没有 kubeconfig、ServiceAccount token、Registry 写凭据或 OpenSandbox lifecycle key。
- Sandbox 中没有模型 Key、PostgreSQL 凭据、Server 内部凭据或 kubeconfig。
- OpenSandbox 原生 provider 通过真实 create、files、streaming exec、PVC 持久化、TTL/delete、NetworkPolicy 和 fencing 验收。
- Worker 在模型、文件修改和远程命令阶段退出后，新 attempt 复用同一 workspace 并完成任务。
- VerifyTask artifact 错误后，下一 Generator Run 从 immutable failed artifact 重置 workspace；验证成功后作者要求修改时，从 verified artifact 创建新 workspace。
- submission 重复提交只产生一个 immutable artifact 和一个逻辑 VerifyTask。
- Controller 重复 reconcile 同一 VerifyTask 时始终只有一个确定名称的 verifier Job。
- verifier Job 真实构建镜像、创建 ContainerEnvironment、执行 answer 和全部 checkpoints。
- artifact 错误触发下一 Generator Run，并使用失败候选和报告修复成功。
- 首个 verifier Pod 遇到 infrastructure 错误后，由同一个 Job 重试并成功，不触发 Agent 修改或创建第二个 Job。
- verifier Pod 异常退出后，重试只存在一个干净 Environment；terminal、取消和删除路径不遗留 Environment 或失败临时镜像。
- artifact 错误写入 `phase=Failed, report.class=artifact` 后不重试 verifier Pod；Job 重试耗尽或超时后才写入 `phase=Failed, report.class=infrastructure`。
- 成功、取消和到期最终清理 Sandbox；孤儿由 TTL 回收。
- 再完成一题真实 `runtime: vcluster` 流程。

### 最终清理

- 源码和 `go.mod` 中不存在 `eino-claude-code`。
- 镜像中不存在 Claude Code CLI。
- 配置和 Secret 中不存在 Claude/Anthropic 专用字段。
- 集群中不存在 `Generation` CRD、Generation Reconciler 或 generator Job。
- VerifyTask spec 中不存在恒定的 source kind，失败类别只使用 `status.report.class`。
- Agent Runtime 只使用 PostgreSQL Session/Message/Run 和 attempt 级重试。
- `VerifyTask` 使用独立 verifier Job。
- 全量 Go tests、前端 tests、Playwright 和真实 Generator E2E 通过。

## 非目标

- 本阶段不批量生成题库，也不继续扩展 Skill/Tag 产品功能。
- 不重做 Authoring、Assistant 或 Catalog UI。
- 不把 Generator 和 VerifyTask 合并。
- 不创建 `AgentRun`、`AgentSession` 或新的 Generation 类 CRD。
- 不引入 Temporal、Dapr Agents、LangGraph 或通用 workflow DSL。
- 不实现 Eino checkpoint、任意 Step 精确恢复或 shell exactly-once。
- 不持久化逐 token delta。
- 不实现通用 outbox、Sandbox metadata adopt 或跨 PostgreSQL/文件系统/Kubernetes 的分布式事务。
- 不在应用层建立细粒度 Artifact 容量契约。
- 不在首轮启用 WarmPool、pause/resume、rootfs snapshot、MCP、fast model 路由或第二个模型供应商。
- 不同时实现或维护 OpenSandbox 原生 provider 与 SIG Agent Sandbox provider。
- 不兼容旧 Claude session、旧 Agent 配置或开发阶段 SQLite 数据。

## 参考

- Eino ADK ChatModelAgent：<https://github.com/cloudwego/eino/blob/v0.9.13/adk/chatmodel.go>
- Eino DeepAgent：<https://github.com/cloudwego/eino/tree/v0.9.13/adk/prebuilt/deep>
- Eino filesystem middleware：<https://github.com/cloudwego/eino/tree/v0.9.13/adk/middlewares/filesystem>
- Eino Ext DeepSeek adapter：<https://github.com/cloudwego/eino-ext/tree/components/model/deepseek/v0.1.7/components/model/deepseek>
- OpenSandbox：<https://github.com/opensandbox-group/OpenSandbox>
- OpenSandbox Go SDK：<https://github.com/opensandbox-group/OpenSandbox/tree/main/sdks/sandbox/go>
- OpenSandbox Agent Sandbox integration（仅后备资料）：<https://github.com/opensandbox-group/OpenSandbox/blob/main/docs/examples/agent-sandbox.md>
- Breakfix Authoring：`internal/authoring/service.go`
- Breakfix Assistant：`internal/assistant/service.go`
- Breakfix Generator/Judge：`internal/generator/workflow.go`、`internal/generator/prompts.go`
- Breakfix Taxonomy：`internal/taxonomy/service.go`
- Breakfix 当前 Generation/VerifyTask：`internal/controller/generation.go`、`internal/generator/verifytask.go`
