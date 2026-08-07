# TODO: 领域化运维与 SRE 题库

> 题库不按零散技术名词扩张。先确定少数稳定的运维领域，再逐个把每个领域做成完整、可验证、可复用的学习内容。本文记录当前平台重构和后续内容建设；长期已确认的契约以 [docs/](docs/README.md) 为准。

## P0：当前架构闭环与代码边界

上一阶段的 E2E 分层与可丢弃验收基线已经完成，稳定契约只保留在 [`docs/operations/testing.md`](docs/operations/testing.md)。本 P0 只处理 [`REVIEW.md`](REVIEW.md) 当前确认的五个问题，不扩展产品功能，不增加 Deployment、通用任务队列、兼容层或第二套恢复权威。

### 1. 回收发布失败留下的孤立物化目录

**现状：** Generation 与 Catalog 都先把 challenge source 物化到 Server data PVC，再通过 PostgreSQL 事务公开 ChallengeRevision 和 RoadmapRevision。revision fence 冲突、Catalog 部分 commit 后失败，或者 Server 在 materialize-before-commit 窗口崩溃时，可能留下不属于任何已发布历史的目录。Runtime Worker reaper 无权访问 Server PVC，当前也没有 Server-owned 清理路径。

**目标行为：** Server data PVC 中只长期保留已经发布的历史 ChallengeRevision，以及非终态 publication/finalizer 仍需要的目录。确定性发布失败和进程崩溃留下的孤立目录最终都由 Server 精确回收。

**实现边界：**

- Server 根据 PostgreSQL 中的 ChallengeRevision、Generation publication intent、Catalog release/commit state 派生保留集合；文件系统不是判断业务状态的权威。
- active、superseded 和 deprecated Challenge 的全部已发布 revision 都属于历史事实，不能清理。
- 非终态 Generation finalizer 与非终态 Catalog commit 的目标目录在恢复完成前必须保留。
- Generation 确定性 finalizer 失败后清理本次 intent 的目录；Catalog Release 进入终态 Failed 后清理只属于该失败 release 的目录。
- Server-owned materialization reconciler 在启动恢复期间先扫描一次，随后低频周期扫描规范目录；每次都从数据库重新派生保留集合，不保存第二份 cleanup worklist。
- 扫描处理崩溃窗口和遗留 staging，不得把额外目录当成公开 Catalog；删除失败留给下一次扫描重试，不回滚已经确定的业务终态，也不交给 Runtime Worker。
- 不增加新的 workflow state、通用 cleanup queue 或额外 Deployment；清理是 Server-owned publication lifecycle 的一部分。

**必须验证：** 并发 revision 中落败的一方、部分物化后失败的 Catalog Release 和 materialize 后崩溃三条路径都能最终清理；当前、superseded、deprecated 历史 revision 以及待恢复 finalizer 的目录保持不变；Catalog 完整性读取仍只检查当前 Roadmap 精确引用。

### 2. 将 Catalog Release 收敛为一次性 baseline bootstrap

**现状：** 系统架构把 Catalog Release 定义为初始化流程，但 installer、availability gate 和文档仍支持后续 append-only Release。已有可用 Catalog 时切换到一个未完成或失败的新 digest，会让旧题库进入 503。

**目标行为：** Catalog Release 只在尚未建立内容基线的空平台安装一次。后续题目、新 Topic/Tag 和题目修订全部通过 Authoring、Generation、Classification 与 Roadmap 流程产生。

**实现边界：**

- 没有已发布 Challenge 且没有 Ready Catalog baseline 时，可以配置一个 immutable digest 作为首次 release。
- 首次 release 未成功且没有发布任何 Challenge 时，可以在清理失败 release 资源后改用另一个 digest。
- 已有 Ready Catalog baseline 时，只允许相同 digest 的幂等启动与恢复；不同 digest 是启动配置冲突，不能进入安装或 availability gate。
- 已经通过 Authoring 发布内容的平台不能再导入首次 Catalog Release。
- 未建立题库且 `catalog.release_reference` 为空仍是合法的空平台启动方式。
- 删除基于现有 Roadmap 追加 source_ref 的 installer 分支、repository surface 和文档语义，不保留旧行为兼容。
- 初次安装期间 Catalog-dependent API 继续等待该 release；已有 baseline 不会因为另一个配置 digest 进入 503。
- 不引入 `desired release / active release`、管理员安装 API 或新的发布工作流。

**必须验证：** 空平台首次安装、安装中断后同 digest 恢复、失败且无公开内容时更换 digest、Ready baseline 同 digest 重启均符合契约；Ready baseline 或 Authoring 内容存在时不同 digest 在启动边界被拒绝，旧 Catalog 不进入等待新 release 的状态；固定 fixture E2E 仍能从空目标完成 bootstrap。

### 3. 让 Server bootstrap 显式拥有全部后台生命周期

**现状：** `httpapi.SetupRouter` 在构造路由时执行 AgentRun 恢复，并隐式启动 learning cleanup、Environment projection、Assistant lease maintenance、Generation finalizer 和 Roadmap maintenance。Server 关闭只显式等待其中一部分后台执行，transport 因而同时承担路由、业务编排和进程生命周期。

**目标行为：** HTTP transport 只构造 Handler 和 Router。所有启动恢复、后台循环、取消、等待和依赖关闭顺序由 Server bootstrap 显式拥有。

**实现边界：**

- `SetupRouter` 不启动 goroutine、不执行启动恢复，也不持有进程级 Context 副作用。
- Generation publication finalizer 归入 Generation application；Roadmap 继续使用现有 application service；learning cleanup、Environment projection 和 Assistant lease maintenance 分别放回相应 application 边界；第一项新增的 materialization reconciler 同样由 bootstrap 启动和等待。
- 每个后台服务暴露阻塞的 `Run(ctx)` 或等价的显式生命周期；bootstrap 按已知服务逐个装配，不设计通用 executor、slot 或内存 worklist。
- 启动恢复在 Server 对外 ready 前完成；后台服务启动失败必须阻止错误实例继续服务。
- 停止接收请求后取消并等待全部 Server-owned Agent 和后台循环，再关闭 Incus client 与 PostgreSQL。
- Router 单元测试不因构造路由而修改数据库、领取 lease 或产生后台 goroutine。

**必须验证：** Router 构造无后台副作用；启动恢复仍发生在 readiness 之前；取消 Server 后所有循环和已派发 Generation phase 都可结束并被等待；数据库和 provider 只在后台服务退出后关闭；Server restart recovery 验收继续通过。

### 4. 建立工作文档的完成态收口规则

**现状：** 已完成的 E2E P0 曾继续以当前提交计划保留在 TODO，旧 REVIEW 也混有已经实现的 revision、恢复和并发问题。当前规划虽然替换了这些内容，但还需要固定每个实现提交和整个 P0 完成时的收口规则，避免同样的问题再次发生。

**目标行为：** TODO 只记录当前可执行工作，REVIEW 只记录当前仍成立的问题，`docs/` 保存已经实现的长期契约。

**实现边界：**

- 本 P0 规划提交删除旧 E2E P0，只引用现有测试运维文档。
- 每个实现提交同时更新对应长期文档和 REVIEW 条目，不把文档修复集中到最后。
- P0 全部完成后，从 TODO 删除本节并从 REVIEW 删除已解决问题，让题库内容建设重新成为顶部任务。
- 不在 TODO 或 REVIEW 复制 OpenAPI、CRD、状态枚举和完整架构文档。

**必须验证：** TODO、REVIEW、README 与 `docs/` 不再同时陈述不同的 Catalog、publication cleanup、Server lifecycle 或 checkpoint 契约；所有链接有效，已完成提交不再以待办形式出现。

### 5. 合并 Checkpoint JSON 协议

**现状：** challenge 内容验证和 Environment 运行时状态分别维护一套 `{"checks":[...]}` 类型与解析器。两者当前接近一致，但字段清理、expected ID、重复 ID 和缺失项校验可以独立漂移。

**目标行为：** 平台只有一个与 Kubernetes、数据库和 HTTP 无关的 checkpoint report 协议实现；内容验证与运行时投影共享它，各自只保留自己的业务状态转换。

**实现边界：**

- 在 `internal/domain/checkpoint` 定义共享的 Report、Result、JSON 解析、expected ID 完整性和 `Passed` 语义。
- challenge 验证使用共享 Report，不再维护第二套 wire type 和 parser。
- Environment 只负责把共享 Result 转换为带 `FirstPassedAt` 的持久状态，不重复解析协议。
- 保持现有 JSON 结构、summary/details 语义和 checkpoint 无顺序要求；这是内部收敛，不新增格式版本或兼容分支。
- 删除重复私有类型和重复校验，测试以共享协议为权威。

**必须验证：** 合法、未知 ID、重复 ID、缺失 ID、空 summary、无 checks 和非法 JSON 只由共享协议测试一次；challenge 与 Environment 分别验证发布内容约束和 `FirstPassedAt` 保留逻辑；Node/VK8s checkpoint 消费结果不变。

### P0 迁移清单

1. 建立 Server-owned 物化引用扫描与确定性失败清理，覆盖 Generation 和 Catalog。
2. 删除 Catalog 的持续追加语义，将 release reference 固定为一次性 baseline bootstrap。
3. 将启动恢复、现有五个后台循环和新增的 materialization reconciler 统一移到 application/bootstrap，并补齐关闭等待。
4. 用本轮规划替换已完成的 E2E TODO，并在每个提交中同步清理 REVIEW 与长期文档。
5. 合并 checkpoint report 类型、解析和校验，删除两套实现。

### P0 提交计划

P0 按以下六个提交实施。每个实现提交必须同时包含该项代码、针对性测试和必要长期文档，并在验证通过后立即提交，再开始下一项；不能积累多项修改后一次提交，也不能把实现验证集中到最后。

1. **`docs: plan current architecture corrections`**
   - **目标：** 用当前五个问题替换已完成的 E2E P0，重写 REVIEW 为当前事实，并明确本轮边界、顺序和完成标准。
   - **验证：** TODO 与 REVIEW 不再包含已经实现的 revision、恢复、并发或 E2E 待办；长期 E2E 契约仍可从 operations testing 文档完整找到。
2. **`fix(publication): reclaim orphaned materializations`**
   - **目标：** 增加 Server-owned 物化保留集合、启动扫描和终态失败清理，同时修正 Catalog/Workflow 文档中的虚假回收描述。
   - **验证：** Generation 冲突、Catalog 部分失败和崩溃恢复均清理孤立目录；所有已发布历史及非终态 publication 保持不变；针对性 PostgreSQL/文件系统测试通过。
3. **`refactor(catalog): enforce bootstrap-only release`**
   - **目标：** 删除 append-only 后续安装路径，在启动边界固定首次 baseline 与 digest 规则，并同步部署、Catalog 和 Workflow 文档。
   - **验证：** 首次安装和同 digest 恢复通过；无公开内容的失败安装可更换 digest；已有 baseline 或 Authoring 内容时不同 digest 不进入 installer/gate；固定 Catalog prepare 与公开投影验收通过。
4. **`refactor(server): own background service lifecycle`**
   - **目标：** 让 application 服务承载后台用例，让 bootstrap 显式启动、取消和等待全部服务，使 Router 构造恢复为纯 transport 装配。
   - **验证：** Router 无后台副作用，启动恢复与关闭顺序测试通过，Generation 并发和 Roadmap lease 语义不变，Server restart recovery 验收通过。
5. **`refactor(checkpoint): share report protocol`**
   - **目标：** 建立唯一 checkpoint report 协议，迁移 challenge 与 Environment consumer，删除重复类型和解析器。
   - **验证：** 协议完整性矩阵、challenge 内容校验、Environment 首次通过时间和现有 Controller 测试通过；Node 学习主路径仍完成同一 checkpoint。
6. **`docs: close current architecture corrections`**
   - **目标：** 在前五项全部完成后删除本 P0 和 REVIEW 中已解决条目，只保留题库内容建设及仍真实存在的问题；核对所有长期架构与运维文档。
   - **验证：** TODO 顶部回到题库建设，REVIEW 不保留已解决问题，README/docs 与代码所有权和运行行为一致；全量快速测试、lint、生成物和两套 Kustomize 渲染通过。

P0 完成标准是：失败发布不遗留孤立物化目录；Catalog Release 只有一次性 bootstrap 语义；Server bootstrap 可确定地启动和等待全部后台服务；checkpoint report 只有一个权威实现；TODO、REVIEW 与长期文档只描述各自负责的当前事实。完成后再继续把 Linux 领域场景制作成真实 Catalog Release。

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
