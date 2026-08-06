# TODO: 领域化运维与 SRE 题库

> 题库不按零散技术名词扩张。先确定少数稳定的运维领域，再逐个把每个领域做成完整、可验证、可复用的学习内容。本文记录当前平台重构和后续内容建设；长期已确认的契约以 [docs/](docs/README.md) 为准。

## P0：扩大题库前补齐工作流与发布生命周期

当前架构边界已经稳定，下一步不再合并 Deployment 或引入新的执行框架。本轮只处理会在题库扩大后显著增加迁移成本、或者会直接阻碍真实题目调试的四件事：独立 GenerationWorkflow 的并发推进、已发布 Challenge 的不可变修订生命周期、发布 finalizer 的持久化诊断，以及 PostgreSQL、Server data PVC 与外部 artifact 的恢复契约。

本轮不把 Catalog Release 扩展为持续升级机制。它仍然只是空平台的一次性 bootstrap；也不提前建设全面指标、Tracing、对象存储、自动备份系统或 artifact 自动重建。异步系统的容量与可观测性优化等待首批真实题目的运行数据。

### 1. 并发推进独立 GenerationWorkflow

**现状问题：** 每次作者启动一次 generate 本来就应该形成一个独立的 `GenerationWorkflow`，但当前 Server 的 `AgentRunner` 同步领取并执行一个 workflow；一个慢的模型调用会阻塞其他作者的生成任务。这不是状态机或数据库的限制，而是当前调度循环的实现过于保守。

**目标行为：** `GenerationWorkflow` 同时是一个生成任务的持久化聚合和 Server 的 worklist 记录。作者每次启动 generate 都创建一条独立记录；Server 从数据库独立领取处于可执行状态的 workflow，并可以同时推进多条记录。不存在一个统一处理所有作者任务的 workflow，也不增加额外的 worklist 表。

**实现边界：**

- 保留现有 `generation_workflows` 作为唯一的生成任务状态和 worklist 权威；不新增统一 workflow、内存队列或第二套任务表。
- `AgentRunner` 的后台循环成功领取一条 workflow 后，异步启动该 workflow 当前阶段的执行，并立即继续领取其他可执行 workflow。
- 一个执行协程只负责当前已领取的阶段；阶段完成并持久化状态转移后协程结束，workflow 后续阶段由下一次自然领取推进。作者审核、Runtime Worker 阶段和终态不会占用 Server 执行协程。
- 继续使用 PostgreSQL 的 `FOR UPDATE SKIP LOCKED` 和 workflow lease 防止重复领取。不同 workflow 之间可以并发；不增加固定并发数、slot、semaphore、worker pool 或新的调度组件。
- 单个 workflow 的执行错误只影响该 workflow 的 AgentRun 和现有重试/失败路径；数据库领取错误才影响调度循环本身。
- Server 关闭时取消并等待已启动的阶段执行；取消不能被当成技术失败消耗 AgentRun attempt，启动恢复继续使用现有中断和新执行语义。

**必须验证：** 一个 workflow 的模型调用阻塞时，其他 workflow 仍能开始；同一 workflow 不会被重复领取；一个 workflow 失败不会阻塞其他 workflow；状态转移后下一阶段可以再次被领取；Server 关闭不会遗留执行协程或错误消耗重试次数；重启恢复、lease 丢失和现有状态机回归保持成立。

### 2. 建立 Challenge 修订、弃用和重新验证生命周期

**现状问题：** Catalog Release 与作者发布都只能产生一个新的 Challenge。发布后如果题解、检查点、基础镜像或软件版本需要修正，只能原地改变不可变内容或创建另一个无关 Challenge；前者破坏可复现性，后者会丢失稳定身份、Roadmap 关系和历史 Environment 的归属。

**目标行为：** Challenge ID 在整个生命周期中稳定，每次内容修改都创建新的不可变 revision。新 revision 完整经过现有 Generation、Build、ArtifactPublishing 和 Verify 链路后，才能原子替换 active revision；历史 revision 和它引用的 artifact 继续可追溯，已经创建的 Environment 始终固定到创建时的 revision。

**实现边界：**

- 将 Challenge 的稳定身份、不可变 revision 和 active revision 指针建模为不同事实；`content_revision` 继续标识可移植内容，不能被重新写入另一份内容。
- Catalog bootstrap 和作者发布共用同一种已发布 Challenge/revision 结果，但不强行共用输入或生成流程。Catalog 首次安装创建初始 active revision；后续修订从已有 Challenge 发起作者工作流。
- 修订或弃用沿用现有作者会话的所有权边界，不新增管理员角色；作者创建的 Challenge 从自己的 My space 发起。Catalog bootstrap 创建的 Challenge 由 release 持有，本轮不允许用户覆盖；若以后需要在线更新 foundation，必须先单独实现 deferred 的 Catalog desired/active release 语义。
- revision 只能修正同一道题的题干、教学内容、实现、题解、检查点和 runtime 依赖；学习目标或核心故障场景已经改变时必须创建新的 Challenge，不能借 revision 偷换稳定身份。
- 修订必须记录其 base active revision。发布时使用乐观并发检查；若 active revision 已变化，本次修订不能覆盖较新的发布结果，必须回到作者可见的失败边界重新确认。
- 新 revision 在 Build 或 Verify 完成前不可被公开 Catalog、Roadmap 或新 Environment 读取。发布成功时，active revision、当前 Roadmap challenge binding 和物化内容必须按现有 intent/finalizer 边界一致推进。
- 已发布 source 和最终 runtime artifact 按 revision 寻址；active 切换不能覆盖旧 revision 的目录或删除旧的已发布 artifact（candidate、workspace 等临时资源仍按现有 reaper 清理）。当前 Catalog 与导出只投影当前 Roadmap 引用的 revision，历史引用仍可解析。
- active revision 被替换后成为 `superseded`，但不立即删除；Challenge 可以显式进入 `deprecated`，此后不再出现在正常 Catalog 导航中，也不能创建新 Environment，历史记录和已存在 Environment 仍引用原 revision。
- Roadmap 边继续绑定稳定 Challenge ID；每份不可变 `RoadmapRevision` 的 challenge binding 同时固定发布当时的精确 Challenge revision。当前 Catalog 从当前 Roadmap 读取 active revision，旧 Roadmap、Environment、checkpoint 进度和报告都不能跟随后续 active 指针漂移。
- revision 发布后将该 Challenge 以新 `content_revision` 重新加入 Roadmap maintenance 的待处理集合；发布本身不等待关系审查。用户的“已完成”仍是稳定 Challenge ID 上的终身学习事实，不因修订被抹除；Environment attempt、checkpoint 和 revision 级质量统计必须保留实际完成的 revision，不能把不同版本的运行证据混为一份。
- 不保留旧的“每次发布都创建无关 Challenge”兼容路径，也不为开发阶段已有数据编写迁移兼容层；schema、领域模型、API、前端作者入口和正式架构文档一次性切换到新语义。

**必须验证：** 初次发布创建稳定 Challenge 与首个 active revision；修订必须重新完成 Build/Verify 才能切换；失败修订不影响当前可服务 revision；并发修订不能覆盖更新的 active revision；切换后新 Environment 使用新 revision，已有 Environment 仍使用旧 revision；旧 materialization/artifact 不被覆盖；完成事实保留而 revision 级证据隔离；deprecated Challenge 不再接受新 Environment，但历史结果仍可读取；用户不能修订 release-owned Challenge 或其他作者的 Challenge。

### 3. 为发布 finalizer 增加持久化诊断

**现状问题：** Generation promotion 之后的物化或发布冲突如果无法恢复，Server 只会周期写日志并让工作流停在 `ChallengePublishing`。进程重启后缺少持久证据，接口也无法区分正常等待、瞬时 I/O 故障和确定性 invariant 冲突；Catalog 的部分物化失败还可能留下孤立目录。

**目标行为：** Generation 与 Catalog 发布 finalizer 的每次失败都形成持久、可查询的诊断。确定性错误结束到现有失败状态；只有确实可能恢复的瞬时错误才保留原状态并重试。用户和调试接口能够直接看到失败类别、最近一次错误和下一次重试时间。

**实现边界：**

- 不增加新的 workflow state；在现有 Generation/Catalog 聚合上持久化 finalizer error category、sanitized last error、last attempted at 和 next retry at，并让成功提交原子清空诊断。
- 错误分类由 materializer/finalizer 返回 typed error 决定，不能通过日志文本猜测。内容冲突、revision/invariant 不一致和不可接受的物化结果属于确定性错误；临时数据库、PVC 或 provider I/O 故障属于瞬时错误。
- Generation 的确定性错误进入现有 `Failed`，保留 candidate、发布 intent 和精确失败证据，并交给现有异步回收；Catalog 的确定性错误进入其现有失败边界，不能继续反复安装同一损坏提交。
- 瞬时错误按持久化的 `next_retry_at` 继续幂等重试，Server 重启后沿用数据库中的诊断和调度时间，不能恢复成固定每五秒盲重试。
- finalizer 对自己创建但未提交的物化结果负责登记并交给现有回收路径；完整性检查继续把 Roadmap 未引用的内容视为非权威数据，不允许孤立目录被公开读取。
- 日志保留 workflow、release/commit、candidate revision 和 publication intent 标识，但日志不是错误状态的唯一权威。

**必须验证：** 确定性发布错误会结束工作流而不是永久停留；瞬时错误持久化诊断并在到期后重试；重启不会丢失错误和重试时间；成功重试会清空旧诊断；部分物化失败不会让孤立内容进入 Catalog，且对应资源能够由后台回收。

### 4. 明确 PostgreSQL、Server PVC 与外部 artifact 的恢复契约

**现状问题：** PostgreSQL 事务不能原子提交 Server data PVC、Registry 和 Incus。当前虽然使用 intent、内容 hash 和幂等 finalizer 缩小跨存储窗口，但没有规定备份边界、恢复顺序和恢复后必须满足的完整性条件。

**目标行为：** PostgreSQL 与 Server data PVC 被视为同一个需要协调备份和恢复的权威单元；Registry/Incus 是由数据库引用的外部不可变 artifact provider。恢复完成后，平台必须先核验数据库、物化源和外部 artifact 的引用关系，再恢复服务。

**实现边界：**

- 在运维文档中定义简单的静默备份流程：停止接收新工作，结束或中断正在执行的工作，再停止会产生持久写入的 Server 与 Runtime Worker；确认 Server data PVC 没有写者后，在同一次维护窗口备份 PostgreSQL 和该 PVC。
- 定义恢复顺序：先保持写入组件停止，恢复 PostgreSQL 和 PVC，再按 Roadmap/materialized revision、Registry digest 和 Incus fingerprint 执行完整性核验，最后启动 Runtime Worker 与 Server 并让现有 finalizer/reaper 接管未完成 intent。
- 恢复验收必须明确检查数据库引用缺失、PVC 内容不匹配、Registry digest 缺失和 Incus fingerprint 缺失；发现损坏时不能宣布恢复完成，也不能静默隐藏 Challenge 或自动改写权威记录。
- 本轮只建立契约和可执行 runbook，并使用现有 Catalog 完整性检查与 provider API 说明核验方法。不把 Registry/Incus 在线探测接入日常 `/readyz`，不新增恢复服务、产品 API、对象存储、分布式快照或自动备份控制器，也不承诺从 Candidate archive 或 Catalog bundle 自动重建全部外部 artifact。
- 外部 Registry 配置和内置 Registry 使用同一 digest 核验语义；Incus 单成员与未来多成员使用同一 fingerprint 核验语义。provider 的备份策略由部署者维护，不与 PostgreSQL/PVC 假装组成原子事务。

**必须验证：** runbook 明确覆盖正常恢复以及 PostgreSQL、PVC、Registry、Incus 任一不一致的处置，现有接口足以核对每类引用；日常 readiness 不新增外部 provider 依赖，恢复后未完成的幂等 intent 和清理任务仍由原有机制继续推进。

### P0 迁移清单

1. 将每次作者 generate 创建独立 GenerationWorkflow、数据库 worklist 领取和不同 workflow 并发推进写入正式架构，并完成调度、取消和恢复验证。
2. 一次性替换 Challenge 发布模型，统一 Catalog 初始 revision 与作者创建/修订的发布结果，并贯通 Catalog、Roadmap、Environment、checkpoint 与前端作者入口。
3. 将 revision 的完整 Build/Verify、active 原子切换、并发修订冲突、superseded/deprecated 和历史 Environment 固定语义写入正式架构文档并完成聚焦集成验证。
4. 为 Generation/Catalog finalizer 建立 typed error、持久诊断和可恢复重试调度，接入现有失败与回收路径。
5. 补充备份恢复 runbook，写清 PostgreSQL/PVC 的维护窗口、恢复顺序，以及使用现有 Catalog 完整性检查和 provider API 核验 Registry/Incus 引用的方法。
6. 最后运行仓库现有的 Go、前端、生成物、部署清单与核心集成检查，确认 P0 没有引入第二套 revision、队列或恢复权威。

### P0 提交计划

P0 按以下四个提交实施。每一项的实现、对应测试、生成物和必要架构/运维文档必须在该项验证通过后一起提交，再开始下一项；不能先堆积多项修改，也不能把验证集中成最后的独立提交。

1. **`feat(generation): dispatch independent workflows concurrently`**
   - **目标：** 让每次作者启动的 generate 都成为一条独立的 `GenerationWorkflow`，由现有数据库记录同时承担持久状态和 worklist；Server 异步推进多个 workflow，不引入额外队列、固定并发限制或新的调度组件。
   - **验证：** 覆盖多个 workflow 的同时领取和推进、单个 workflow 失败不阻塞其他 workflow、重复领取防护、阶段转移后的再次领取，以及取消和重启恢复语义；确认没有遗留执行协程或错误消耗 AgentRun attempt。
2. **`feat(challenge): add immutable revision lifecycle`**
   - **目标：** 建立稳定 Challenge ID、不可变 revision、active 原子切换、并发修订保护和 deprecated/superseded 语义，并让 Catalog、Roadmap、Environment 与作者入口统一使用它。
   - **验证：** 覆盖初次发布、修订重新验证、失败与并发修订、active 切换、历史 Environment 固定和弃用后的读取/创建边界；同时通过相关 API、前端和集成回归。
3. **`fix(publication): persist finalizer diagnostics`**
   - **目标：** 为 Generation/Catalog finalizer 增加 typed error 分类、持久错误证据和持久重试调度，让确定性错误进入现有失败边界，瞬时错误继续幂等重试，并收敛部分物化的回收责任。
   - **验证：** 覆盖确定性失败、瞬时失败、重启恢复、成功后清空诊断和孤立物化回收；确认工作流不会永久无诊断地停在发布状态。
4. **`docs(operations): define and exercise recovery unit`**
   - **目标：** 明确 PostgreSQL + Server data PVC 的协调备份恢复单元、Registry/Incus 的外部 artifact 核验边界和可执行 runbook；不增加常驻恢复组件或日常 readiness 依赖。
   - **验证：** 文档覆盖正常恢复、四类存储不一致和未完成 intent 的恢复边界，现有完整性检查与 provider API 能提供所需证据；完成文档链接与相关聚焦回归，且没有引入对象存储或第二套恢复权威。

P0 完成标准是：不同作者启动的 GenerationWorkflow 可以独立并发推进，单个慢任务或失败不会阻塞其他任务；作者发布的题目可以在保留稳定身份和历史可复现性的前提下修订或弃用，Catalog bootstrap 仍保持一次性 immutable 基线；发布 finalizer 不会无诊断地永久卡住；部署者能够以一套明确、经过演练的流程恢复 PostgreSQL、Server PVC，并核验 Registry/Incus 引用。达到该标准后再批量制作和发布 Linux 题库。

## 后续：题库内容建设

以下内容只服务于题目设计、讨论、审查和 portable release 制作，不直接显示在用户 UI 中。

### 内容工作区

根目录 `catalog/` 是可读、可审查的内容工作区；`curriculum/` 是作者整理 Domain/Topic 边界和题目设计的材料，`releases/<name>/` 才是可打包、可安装的实际 Catalog Release：

```text
catalog/
  curriculum/
    domains/<domain>/README.md
    topics/<domain>/<topic>.md
  releases/
    foundation-linux-system-network/
      release.yaml
      challenges/linux-system-network/<readable-source>/
      roadmap/
        domains/
        topics/
        tags/
        challenge-bindings/
        topic-edges.yaml
        challenge-edges.yaml
```

- 每个 Domain 导读说明学习目标、范围、非范围、建议学习顺序、对运行时的要求、参考来源和完成标准。
- 每份 Topic 导读记录范围、前置背景、心智模型、典型故障、诊断证据、修复原则、常见误区、面试追问和来源。它是题库设计材料，不是用户 UI 页面，也不是 Agent 分类规则机器。

### 领域范围调研

调研日期：2026-08-02。没有一个权威框架把“运维”缩成唯一目录，但 Linux 系统管理、Kubernetes 管理和 SRE 的官方能力纲要收敛出以下稳定边界。

| 领域 | 边界与核心内容 | 调研依据 | 当前取舍 |
| --- | --- | --- | --- |
| Linux 系统与网络运维 | Shell/文件、用户与权限、软件包、进程与资源、systemd/journal、存储、网络、DNS、TLS 基础、SSH 与远程排障 | [RHCSA objectives](https://www.redhat.com/en/services/training/ex200-red-hat-certified-system-administrator-rhcsa-exam) 覆盖 essential tools、运行中系统、存储、服务、网络、用户/组和安全 | **首个完整领域**；使用 NodeEnvironment |
| 容器运行时运维 | 镜像、容器进程、日志、挂载、网络、资源限制与调试 | [roadmap.sh DevOps roadmap](https://roadmap.sh/devops)、[devops-exercises](https://github.com/bregman-arie/devops-exercises) 将 containers 作为独立能力域 | 暂不单列首批 release；先作为 Linux/Kubernetes 题的必要子能力，实际内容量足够时再独立 |
| Kubernetes 工作负载与服务运维 | Workload、配置、调度与资源、Service/DNS、存储、RBAC、NetworkPolicy、应用排障 | [CKA Domains & Competencies](https://training.linuxfoundation.org/certification/certified-kubernetes-administrator-cka/) 的 workload/scheduling、services/networking、storage、troubleshooting；[Kubernetes Concepts](https://kubernetes.io/docs/concepts/) | **第二个完整领域**；使用 VK8sEnvironment |
| Kubernetes 集群管理 | 控制平面、节点生命周期、etcd、证书、CNI、集群安装与升级 | CKA 的 cluster architecture/install/config；[Kubernetes The Hard Way](https://github.com/kelseyhightower/kubernetes-the-hard-way) 的 CA、etcd、控制平面、worker 与网络顺序 | 暂缓。现有 VK8sEnvironment 首先服务于工作负载运维，不能假装覆盖真实集群管理 |
| SRE 可靠性运维 | SLI/SLO/error budget、指标/日志/追踪、告警、容量、事故指挥、runbook、复盘与 toil 自动化 | [Google SRE Book: SLO](https://sre.google/sre-book/service-level-objectives/)、[Monitoring](https://sre.google/sre-book/monitoring-distributed-systems/)、[Toil](https://sre.google/sre-book/eliminating-toil/)、[Incidents](https://sre.google/sre-book/managing-incidents/) | **第三个完整领域**；建立在 Linux/Kubernetes 题库之上 |
| 交付与平台自动化 | Git、CI/CD、配置管理、IaC、GitOps、变更与回滚、镜像与供应链 | [roadmap.sh DevOps roadmap](https://roadmap.sh/devops)、[devops-exercises](https://github.com/bregman-arie/devops-exercises) | 暂缓，不为覆盖面强行增加题目 |
| 安全、身份与供应链 | 主机加固、密钥、访问控制、镜像/依赖安全、审计与响应 | RHCSA 的安全目标与 Kubernetes 的 security/policy 概念 | 初期作为各领域的横向约束；有足够独立内容后再判断是否单列 |

因此，基础题库只激活以下三个领域，并且**一次只生产一个领域**：

1. **Linux 系统与网络运维**：先完成，目标是建立主机、服务和多节点排障的扎实基础。
2. **Kubernetes 工作负载与服务运维**：Linux 领域达到完成标准后再开始。
3. **SRE 可靠性运维**：前两个领域已有真实服务和故障素材后再开始。

容器、Kubernetes 集群管理、交付自动化和专项安全不是遗漏，而是明确延期；在前三个领域没有做深之前，不为它们创建零散题目。

### Linux 系统与网络运维领域设计

### 边界

这个领域的目标是让学习者能够在一台或多台 Linux 主机上，以运行证据定位并恢复服务，不是训练命令记忆，也不是覆盖所有 Linux 内核、硬件或云厂商知识。

- 运行时固定为 `NodeEnvironment`。题目可创建 `client`、`gateway`、`app`、`resolver`、`storage` 等场景角色节点，学习者可以进入所有节点；这些名称不暴露 Incus。
- 首发包含主机文件与权限、账号与远程访问、软件包与配置、进程与 systemd、日志、资源与存储、IP/DNS/端口/TLS 和多节点网络诊断。主机防火墙、抓包和挂载恢复留待 capability probe 后扩展。
- 应用程序只作为可观察服务载体，例如 Nginx、OpenSSH、dnsmasq 或一个小型自定义 HTTP 服务；不把数据库、容器或 Kubernetes 专项知识混入本领域。
- 不包含 bootloader、内核模块、硬件驱动、真实宿主机内核调优、RAID/LVM、云 VPC 控制面或公网依赖。现有 Node 运行时是非 privileged Incus system container，不能假装它等同裸机。
- 检查和答案必须完全在环境私有网络中完成。不得像某些公开练习一样依赖 `google.com`、真实公网 DNS 或外部软件服务作为通过条件。

### 调研结论

| 参考 | 实际观察 | 对本领域的取舍 |
| --- | --- | --- |
| [Red Hat RHCSA objectives](https://www.redhat.com/en/services/training/ex200-red-hat-certified-system-administrator-rhcsa-exam) 与 [Linux Foundation LFCS](https://training.linuxfoundation.org/certification/linux-foundation-certified-sysadmin-lfcs/) | 两者都把主机工具、运行中服务、存储、网络、身份和安全作为系统管理的稳定内容边界。 | 用作领域范围的交叉校验，不把认证命令清单或考试时间限制直接变成题目。 |
| [Linux Upskill Challenge](https://github.com/livialima/linuxupskillchallenge) | 以 Day 1--20 从 SSH、主机认识、权限、软件包、服务、网络、计划任务、日志、磁盘逐步建立基础。 | 借鉴前置知识的推荐顺序；每个练习仍改写为症状驱动、可独立完成的故障场景，不复制讲义、任务或命令。 |
| [SadServers](https://github.com/SadServers/sadservers) | 124 个公开 scenario 中可见 `cordoba` 的 deleted-but-open 文件、`sume` 的内网 DNS、`pokhara` 的 SSH key、`valladolid` 的 systemd service 等真实故障模型。它的许多检查同时也绑定文件哈希、固定脚本或 CTF 结果。 | 借鉴“症状 + 目标状态 + 环境内验证”的场景形态；不复制 scenario、脚本或题干，尤其不采用哈希、固定编辑路径和唯一命令式检查。 |
| [bregman-arie/devops-exercises: Linux](https://github.com/bregman-arie/devops-exercises/tree/master/topics/linux) | systemd、SSH、存储、性能、进程、安全、网络、DNS、软件包、服务、用户/组覆盖很广，但主体是概念问答。 | 用作知识遗漏检查和 Topic 导读中的面试追问来源；不把问答页直接转写为 runtime 题目。 |
| [Killercoda scenario examples](https://github.com/killercoda/scenario-examples) | 一个场景将初始化、说明和每步 verify 资产分开。 | 借鉴 `generate.sh`、教学材料与验证资产分离；不使用它的有序 step、点击式 verify 或预置命令。 |
| [Educates](https://github.com/educates/educates-training-platform) 本地样例 | Workshop 将内容文件、运行时镜像和 session 配置分开，examiner 支持自动轮询。它也支持 cascade 的强制步骤链。 | 借鉴运行时与教学资产分离、自动检查；保持 Breakfix checkpoint 独立、无 Submit、无完成顺序。 |
| [The Art of Command Line](https://github.com/jlevy/the-art-of-command-line)、[Command-line Text Processing](https://github.com/learnbyexample/Command-line-text-processing) 与 [Awesome Sysadmin](https://github.com/awesome-foss/awesome-sysadmin) | 命令上下文、文本证据和系统管理工具是高频基础，但工具清单本身不是课程结构。 | 将它们嵌入日志、配置、进程和网络诊断，不出“记住 `grep`/`awk` 参数”的孤立题。 |

上述项目的许可证和内容形式并不相同，正式题库只保留来源链接和内容依据。题干、答案、初始化脚本、验证脚本和图片必须原创或另行确认可再利用的许可。

SadServers 的 `jakarta` 场景直接以 `google.com` 连通为目标；这恰好说明公网 DNS 不能成为 Breakfix 自动验证的前提。Breakfix 的 DNS、TLS、HTTP、SSH 和转发题必须自带 resolver、服务端、证书和预期路径，所有 checkpoint 仅观察环境私网状态。

### 首轮核心场景与长期目标

当前先定义 **24 道核心场景卡**：12 个 Topic 各两道，统一复用少数实验系统。它们不是已经可发布的题目，也不是每个 Topic 的固定配额；用途是先验证课程边界、运行时能力、题目资产结构和真实验证模式。只有通过 capability probe 的场景才能进入 candidate 制作，随后必须在真实 `generate -> answer -> checks` 环境中通过。

长期仍以 Linux Domain 积累到 80--100 道以上高质量题目为方向，但不预先设计一百个变体，也不以题数决定发布。每轮扩展都必须新增故障模型、证据类型、约束或合理的跨 Topic 组合；缺少独立价值的场景不进入题库。

下面是读者可直接浏览的 Topic 目录，而不是“每个 ID 必须对应若干题”的配额矩阵。一个 Topic 可以自然地包含很多题，也可以在缺少有意义场景时暂时很少；不得通过更换文件名、端口或变量来填满目标数量。

| Topic | 覆盖内容与可形成的场景 |
| --- | --- |
| Shell、文件与配置定位 | shell 执行上下文、PATH、文件层级、文本检索、配置发现和运行证据。 |
| 用户、组与权限 | 所有权、模式位、ACL、特殊目录、账户/组和 sudo 最小权限。 |
| 软件包与配置管理 | 软件包版本、仓库状态、配置语法、配置漂移和安全恢复。 |
| 进程与 systemd 服务 | 进程树、信号、后台任务、unit 生命周期、依赖、drop-in、执行用户、环境和工作目录。 |
| 日志与计划任务 | journal、应用日志、日志轮转、保留策略、cron、at 和 systemd timer。 |
| CPU、内存与资源限制 | load、CPU 争用、内存、swap、OOM、文件描述符、ulimit 和服务级限制。 |
| 磁盘与文件空间 | 磁盘空间、inode、deleted-but-open 文件、日志空间与可恢复的文件系统状态。 |
| 网络地址与路由 | 链路、地址、邻居、默认路由、路由表和多节点连通性诊断。 |
| DNS 与名称解析 | `/etc/hosts`、resolver、搜索域、记录、缓存和解析路径定位。 |
| 端口、TCP 与 HTTP 服务 | socket、监听地址、端口冲突、进程归属、TCP/HTTP 请求路径和服务暴露。 |
| TLS 与证书信任 | 证书有效期、主机名、SAN、信任链、客户端与服务端 TLS 配置。 |
| SSH 与远程运维 | sshd、密钥认证、`authorized_keys` 权限、client config、host key、ProxyJump、端口转发和安全文件传输。 |

`multi-node` 是场景拓扑 Tag，不是一个 Topic。多节点题按其根因归入 DNS、网络地址与路由、端口/TCP/HTTP、TLS 或 SSH；这样用户既能看到完整的 DNS/SSH 学习主题，也能筛选所有多节点事故。`incident`、`least-privilege` 和 `configuration-drift` 同理只表达跨 Topic 的约束或情境。

首发的推荐学习路径可以从“Shell、文件与配置定位”开始，经过“用户、组与权限”“软件包与配置管理”“进程与 systemd 服务”“日志与计划任务”，再进入资源、存储与网络 Topic；网络部分建议按“网络地址与路由 -> DNS -> 端口、TCP 与 HTTP -> TLS -> SSH”浏览。该顺序是导览，不是解锁规则；正式路线只由经 Topic 导读和题目内容证明的 `precedes` 边表达。

以下候选内容不进入当前 24 道核心场景，因为它们依赖当前非 privileged Incus system container 尚未证明可用的能力。capability probe 通过后再独立纳入目录，不能让它们拖慢或污染首轮制作。

| 后续候选 Topic | 需要先证明的能力 |
| --- | --- |
| 文件系统挂载与持久化 | 在该 profile 中安全地执行 `mount`/`fstab` 语义，不影响底层宿主机。 |
| 主机防火墙与流量过滤 | `nftables`/`iptables` 的 namespace 与 `CAP_NET_ADMIN` 行为。 |
| 抓包、MTU 与网络性能 | `CAP_NET_RAW`、可控流量和修改网络参数的隔离边界。 |

直接写 cgroup、创建 network namespace、loop device 和修改路由不属于基础 NodeEnvironment 的默认承诺。资源限制和路由类核心场景均标为 capability-gated，只有隔离行为被真实验证后才实施；不能把容器限制伪装成裸机权限。

### 题目形态与领域完成标准

题目可自然采用以下形态，具体由故障模型决定，不要求每个 Topic 都完整覆盖全部形态：

1. **状态识别**：从日志、状态、配置或网络现象形成正确假设。
2. **单故障诊断与修复**：一个根因、多个可观察证据和不限定的修复路径。
3. **受约束修复**：例如保持最小权限、不能中断另一服务、不能删除数据、必须保留 SSH 连通性。
4. **复合事故**：两个或三个相互关联但可区分的根因，要求先缩小故障域再恢复端到端状态。

首轮完成标准不是题数，而是 24 张场景卡均经过内容审查，已通过能力验证的场景都具备可运行 candidate、答案和真实验证报告；不具备能力的卡明确保留为 gated，不用伪实现替代。之后以五到十道已验证题为一个内容审查批次扩展。任一题重复、不可解释或无法在干净初态稳定验证时，必须替换为新的故障模型，不能以降低标准凑数。

题目清单应按 Topic 维护覆盖情况、场景根因、证据类型、约束、节点拓扑和验证结果，但不为 Topic 设定题数配额。一个 Topic 只有在能自然解释题目为何属于它时才收录该题；无法归类的题先回到 Topic 导读审查，而不是临时塞进 Tag。

### NodeEnvironment 能力验证前置项

当前 Node profile 是非 privileged、isolated-idmap 的 Incus system container，默认每节点为 1 CPU、512 MiB 内存、5 GiB 根盘、最多四个节点。candidate 制作前应先做一次专门的 runtime capability probe，并把结论写入 Domain 导读：

- [ ] 验证 systemd unit、timer、journal、APT 本地包/仓库、OpenSSH、低端口监听、进程信号、ACL、`/etc/hosts` 和多节点私网在学习与 verification 环境中的一致行为。题目不能依赖公网 APT 或外部镜像服务。
- [ ] 验证或明确排除 `nftables`/`iptables`、`tcpdump` 所需 capability、修改地址/路由、network namespace、mount/`fstab`、loop device、cgroup 写入和 resource limit 调整。未验证的能力只能保留为 gated 或后续候选内容，不能伪装成可发布题目。
- [ ] 所有 CPU、内存、I/O 和磁盘压力题使用有界、可自动回收的小规模负载；不得让一个 512 MiB/5 GiB 学习节点失控或影响其他环境。
- [ ] 所有 DNS、TLS、HTTP、SSH 和转发题都在题目私网内提供目标服务、证书和名称解析；禁止依赖公网、宿主机 DNS 或不可控外部网络。

### 一个领域如何做到完整

“完整”不是覆盖所有厂商产品，也不是堆到任意数字。一个领域发布前至少满足：

- 有一个经过人工审查的 Domain 导读和一组自然、边界清楚的 Topic 导读；Topic 不因技术实现而拆成难读的内部术语。
- 每个 Topic 的题目覆盖真实且不同的故障模型、证据类型或约束；不通过替换变量名、文件路径或命令参数制造重复题。
- 一个领域至少有 40 道真实、完整验证的 challenge；只有新增题能覆盖新的 Topic 内容、故障模型、证据类型或跨 Topic 组合时，才继续扩展到 80 至 100 道以上。
- 每道题有 Topic 关联、来源、渐进提示、可解释解答、参考答案和真实验证报告。面试追问写入 Topic 导读，不成为 Submit、分数或独立 runtime。
- challenge 的 checkpoint 只观察结果，不规定唯一命令、编辑器或完成顺序；`answer.sh` 必须在同一初态的真实 Environment 中通过所有 checkpoint。
- 领域内的目录、Topic 定义、Tag 用法和 Roadmap 关系能让新作者理解新增题应放在哪里；不能分类的题先回到内容审查，而不是用额外 Tag 掩盖问题。

## 后续执行清单

### P1：完成 Linux 系统与网络运维领域

- [x] 建立 Domain 导读、实验系统约定、12 个 Topic 导读和 24 张核心场景卡；它们是首轮内容审查输入，不是已发布题目。
- [ ] 将 Domain/Topic 导读、Tag、challenge、solution、hint、source citation 和内容审查清单固化为 `catalog/curriculum/` 与 release 模板。
- [ ] 完成上面的 runtime capability probe，并把每项结论回写到 Domain 导读和场景卡的能力状态。
- [ ] 先实现并真实验证 capability-ready 的核心场景；capability-gated 场景等待对应 probe 结论，不以降级题意或伪造运行时提前发布。
- [ ] 以五到十道已验证题为一个内容审查批次自然扩展；当 Linux Domain 已有至少 40 道真实、不同的题目后，再评估是否继续走向 80--100 道以上，不把未成熟的 Kubernetes、CI/CD 或云题混进该领域。
- [ ] 包含真正的多节点 SSH/网络故障题，验证 `NodeEnvironment` 的节点访问、私有网络、节点名和跨节点 checkpoint 语义；场景名称不能泄露 Incus。
- [ ] 每道候选题均在真实 `generate -> answer -> checks` 环境中通过，完成内容审查后再进入 Linux foundation release。

### P2：完成 Kubernetes 工作负载与服务运维领域

- [ ] 在 Linux 领域通过完成标准后，定义 Kubernetes Domain 导读和 Topic 目录，再持续生产至少 40 道真实 VK8s 题。
- [ ] 覆盖 workload/config、Service/DNS、调度与资源、事件/日志调试、RBAC、存储和 NetworkPolicy；每题明确证据来自哪些用户可见状态，而非唯一 YAML 写法。
- [ ] 控制平面、etcd、证书和 CNI 题不混入本领域，直到运行时能力与独立 Domain 设计都成熟。

### P3：完成 SRE 可靠性运维领域

- [ ] 以前两个领域中的真实服务和故障为素材，建立 SLI/SLO、观测信号、告警、容量、事故缓解、runbook、复盘和 toil 自动化的 Topic 目录。
- [ ] 先构造小型、受控的服务故障；只有资源与内容质量足够时，才采用 [OpenTelemetry Demo](https://github.com/open-telemetry/opentelemetry-demo) 或 [Online Boutique](https://github.com/GoogleCloudPlatform/microservices-demo) 的局部组件。
- [ ] 事故场景按“信号 -> 影响判断 -> 诊断 -> 缓解/恢复 -> 验证 -> 复盘”组织；自动 checkpoint 只判断技术状态，沟通与决策写入 Topic 导读和解答。

## 附录：研究来源与借鉴边界

| 来源 | 可借鉴内容 | 不应照搬 |
| --- | --- | --- |
| [Red Hat RHCSA objectives](https://www.redhat.com/en/services/training/ex200-red-hat-certified-system-administrator-rhcsa-exam) | Linux 主机运维的内容边界：工具、运行中系统、存储、服务、网络、用户/组和安全。 | 认证考点和厂商命令清单不能直接等同于课程或题目。 |
| [Linux Foundation CKA Domains & Competencies](https://training.linuxfoundation.org/certification/certified-kubernetes-administrator-cka/) | Kubernetes 的 cluster architecture、workloads/scheduling、services/networking、storage、troubleshooting 划分。 | 不把考试权重、限时和单一命令操作变成产品规则。 |
| [Google SRE Book](https://sre.google/sre-book/table-of-contents/) | SLO、监控、toil、事故与可靠性工程之间的关系。 | 不将大型组织流程或生产事故直接压缩为一条 shell 题。 |
| [roadmap.sh DevOps](https://roadmap.sh/devops) | 对操作系统、网络、容器、CI/CD、IaC、监控、云和安全的覆盖盘点。 | 它是广度检查清单，不是本项目的学习顺序或 Domain 契约。 |
| [bregman-arie/devops-exercises](https://github.com/bregman-arie/devops-exercises) | Linux、网络、Kubernetes、容器、可观测性等领域的面试追问与遗漏检查。 | 不复制问答，也不将其扁平主题列表变成题库结构。 |
| [Killercoda Scenario Examples](https://github.com/killercoda/scenario-examples) 与 [Educates](https://github.com/educates/educates-training-platform) | 题目资产、说明与运行时环境分离的方式。 | 不采用强制逐步验证，也不引入其完整平台复杂度。 |

所有公开资料只用于理解、覆盖盘点和结构参考。题干、答案、脚本、图片或其他具体内容的复用必须先单独核对许可；正式题库只收录可以独立解释、独立验证的原创或明确授权内容。
