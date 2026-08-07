# TODO: 领域化运维与 SRE 题库

> 题库不按零散技术名词扩张。先确定少数稳定的运维领域，再逐个把每个领域做成完整、可验证、可复用的学习内容。本文记录当前平台重构和后续内容建设；长期已确认的契约以 [docs/](docs/README.md) 为准。

## P0：E2E 分层与可丢弃验收基线

当前 E2E 把平台准备、Catalog 安装、浏览器行为、真实 Environment、Kubernetes 重启和模型调用混在同一套测试中，导致失败边界不清晰、准备时间过长、失败现场难以复现。本 P0 只重构测试边界，不修改产品工作流语义，也不新增测试专用服务或通用调度器。

E2E 只验证少量真实的用户或平台承诺。单元测试、前端逻辑测试、PostgreSQL/OCI/Incus 集成测试和 Controller 测试负责各自的内部行为；模型自然语言、prompt 文案、工具调用次数和完整故障矩阵不属于日常 E2E。

### 1. 测试分层与责任边界

P0 只保留四类清晰的测试层级，不把它们统称为一套必须同时运行的 E2E：

| 层级 | 负责验证 | 运行方式 |
| --- | --- | --- |
| 快速测试 | Go 领域逻辑、前端纯逻辑和稳定的 API 行为 | 默认测试 |
| 集成测试 | PostgreSQL、OCI/Catalog、Incus provider 和外部适配器契约 | 按各自依赖运行 |
| 平台验收 | 真实 Kind、Registry、Server、Controller、Runtime Worker、Incus 的少量跨组件主路径 | 独立入口 |
| Live Agent 验收 | 真实模型、OpenSandbox 和 Authoring 全链路 | 显式手工运行 |

Controller 需要真实 Kubernetes API 语义时可以使用 `envtest`，但它没有 kubelet、Controller Manager 或真实节点，不能代替 Kind/Incus 平台验收。本 P0 不引入 `k8s-sigs/e2e-framework`，避免为 TypeScript 浏览器、Incus 和模型测试增加第二套编排框架。

E2E 场景遵循“一条场景只证明一个产品承诺”：不在同一个测试中同时验证 Catalog bootstrap、UI 导航、Environment 创建、checkpoint、Agent 工作流和重启恢复。发布冲突、finalizer 重试、revision 并发和数据库边界继续由已有单元/集成测试覆盖。

### 2. 明确可丢弃的 Kind E2E 平台

**目标行为：** 增加显式的 `make e2e-prepare` 与 `make e2e-reset`。二者只接受 `kind-*` context，且只操作专用的 E2E 目标。`prepare` 负责从干净状态部署当前工作树、通过正常 OCI digest 路径安装固定 fixture Catalog，并等待公开投影完成；它结束后不自动运行 Playwright。测试入口只连接已经准备好的目标。

**实现边界：**

- 只使用 `test/fixtures/catalog-release/`，不读取 `catalog/` 课程工作区，不向 PVC 复制文件，不直接写数据库，也不调用隐藏安装 API。
- 复用现有 Kind、Registry、Runtime Worker identity 和部署定义，不新增测试专用 Deployment 或第二套 provider 实现。
- prepare 先做只读 preflight，再清理专用目标、部署当前工作树、发布 fixture digest 并等待公开 Catalog projection。它不能携带上一次的 release reference 或旧题目状态。
- 前置条件是明确的 Kind target、现有 Registry/Incus 凭据和可用的 Node base image；普通基线不调用模型或 OpenSandbox。
- Node artifact 使用专用 E2E Incus build/image project 和 `e2e` 名称前缀；运行时配置由同一份配置注入 Server、Controller 和 Runtime Worker。不得清理共享的 `breakfix-build`、`breakfix-images` 或 `bf` 资源。
- `incus.build_project`、`incus.image_project` 和 `incus.name_prefix` 必须由既有运行时配置注入，prepare 只能操作这组 E2E identity，不按宽泛前缀清理共享 project。
- fixture 必须经过与产品相同的 Registry 认证、信任和 immutable digest 路径发布；不得退回为 `docker load`、节点文件复制、insecure Registry 或修改集群 DNS。
- prepare/reset 只管理明确的 E2E target；非 Kind context 必须拒绝，共享 Incus project、base image、网络、OpenSandbox 安装和外部 Registry 不得被修改。
- reset 先停止会写入数据的 Server、Runtime Worker 和 Registry，保留 Controller 删除并等待 E2E 目标内的 `NodeEnvironment`、`VK8sEnvironment` finalizer；再清理专用 Incus project、OpenSandbox workspace、Registry/Server/PostgreSQL 数据和 release reference。provider 清理失败时保留资源标识并报告失败，不能先删除数据库状态掩盖泄漏。
- Catalog 安装等待属于 prepare 的有界阶段，并在失败时输出 release/entry 诊断；Playwright setup 只做短连接检查，不能用长时间轮询掩盖平台未准备。
- prepare 通过公开 Catalog 校验 fixture 的数量、runtime、Topic 和 Tag；测试不能只按标题查找而把旧 fixture 当成成功。
- 只有公开 projection、immutable digest 和精确 UI Origin 全部校验成功后，prepare 才创建带 target identity 的 prepared marker；测试入口必须验证这个 marker，准备中断或准备失败的目标不能运行 Playwright。
- 基线浏览器与 Node runtime 不调用模型；真实模型与 OpenSandbox 只由 Live Agent 验收检查。

**必须验证：** 从空的专用目标完成 prepare 后，当前 control plane 健康且公开 Catalog 只包含当前 fixture；reset 后专用 PVC、Incus project、Sandbox 和 Environment finalizer 不残留，共享 provider 资源不受影响。准备失败时不运行测试；清理失败时保留资源标识并报告失败。

### 3. 浏览器与 Node runtime 平台验收

**现状问题：** 当前浏览器测试、Node 运行时测试和恢复测试共享长 global setup；恢复场景还自行管理 port-forward 和 Deployment，导致测试之间的责任边界不清楚。部分 helper 维护手写状态快照，容易与 OpenAPI 和当前状态机漂移。

**目标行为：** 将平台验收拆成三个独立入口：轻量浏览器 UI、Node 学习主路径、平台恢复。每个入口只消费已准备好的 fixture target；浏览器测试不创建 Environment，Node 主路径只验证一次完整学习闭环，恢复测试不承担浏览器 UI 回归。

**实现边界：**

- 保留 `make test-e2e` 作为轻量浏览器入口，增加独立的 `make test-e2e-node` 和 `make test-e2e-recovery`；它们不在同一 Playwright 项目中混跑。Node 和 recovery 套件可以较慢，但不能拖慢 UI 回归。
- prepare 为每个可丢弃 target 选择一个动态空闲端口，并将同一个精确 `ui_origin` 写入 Server 配置，以保持终端 WebSocket 的 Origin 校验。每次测试入口只有一个 Server connection/port-forward 所有者，显式使用该 target-local base URL；测试不能依赖开发者遗留的 `localhost:9090`，Server 重启后的重新连接也由同一个入口 helper 管理。
- UI 套件只通过用户可见的 role、label、文本和明确的 test id 交互；API 或 Kubernetes 状态断言放在 Node/recovery 套件。禁止伪造 workflow 完成、直接 patch 数据库、把 fixture 解压到 PVC，或按宽泛前缀清理共享资源。
- 所有 helper 与当前 OpenAPI 字段、当前 `GenerationWorkflow` state 和实际 Deployment 名称对齐；删除过期的手写状态枚举和旧架构兼容分支。
- fixture 保持一个确定性 Node 场景：无模型、无公网依赖、一个节点、一个可观察 checkpoint。内容改变时必须重新生成 release 和 roadmap content revision，不能手改 hash。
- 每个测试使用独立用户和精确登记的资源 identity。正常结束只回收自己创建的 Environment；失败时保留 workflow、Environment 和组件日志 identity，整套 target 由显式 `e2e-reset` 回收。
- UI 用例可以依赖隔离的 BrowserContext 并行运行；Node、recovery 和 Live 套件在同一个 E2E target 上串行且独占执行，因为它们会创建真实 Environment 或重启 Deployment。这是资源所有权，不新增 slot、pool 或测试调度器。

**场景范围：**

- **UI：** Guest 浏览 Catalog，认证用户进入 My Space 并完成一个响应式导航 smoke；排序、过滤等纯前端细节下沉到前端测试，不为它们启动真实 Incus 环境。
- **Node：** 注册用户、启动固定 Node challenge、连接终端、执行 `answer.sh`、完成 checkpoint，并在同一场景中确认学习记录可见。需要切换页面的行为作为该场景的一步，不拆成依赖前一个测试的串行用例。
- **Recovery：** 独立验证已有 NodeEnvironment 经 Server 重启和 Controller 重启后仍可继续收敛并完成 checkpoint；它可以使用 API 和 Kubernetes 观察，不承担 UI 导航断言。

Playwright 默认不自动重试平台场景。异步状态只使用有界的 web-first assertion 或状态轮询，不用固定 sleep 掩盖问题；失败时保留 trace、截图、Server/Controller/Runtime Worker 日志和资源 identity。

**必须验证：** prepared target 上 UI 和 Node 主路径分别通过；recovery 套件分别通过 Server restart 与 Controller restart 场景。每个套件都能单独运行，不依赖其他用例的用户、Environment、端口或状态。

### 4. 保留一次真实 Node authoring 验收，而非把模型变成门禁

**现状问题：** 真实模型、OpenSandbox、Incus 和 Registry 的 Authoring 主路径尚未在当前架构上验收。把模型调用、K8s Authoring、Assistant 和 soak 混入日常 E2E 会使结果不可重复、反馈过慢。

**目标行为：** 提供显式的 `test-acceptance-node` 入口，串行执行一次 Node Authoring -> 发布 -> 学习环境主路径。它是人工 Live 验收，不是 `make test-e2e` 或日常 CI 门禁。

**实现边界：**

- 测试只断言持久化状态、公开 challenge、Environment 和 checkpoint 的可观察结果，不对模型自然语言、工具调用次数或 prompt 文案做渲染或文本断言。
- live 测试使用全新的 prepared target；它会产生不可变 authoring Challenge，不能在测试结束后原地删除或伪造回滚。测试的 `finally` 只回收自己创建的 Environment，整套 target 由显式 reset 丢弃。
- Node live 验收失败时保留该 target 以及 AuthoringSession、GenerationWorkflow、AgentRun、CandidateRevision、Environment 和 Worker 日志的标识，供定位；不自动重跑掩盖问题。
- K8s authoring live、Assistant live 和 Assistant soak 保留为单独手工验收入口，本轮不把它们纳入 P0 完成门槛。它们开始前必须先有真实 VK8s 或 Assistant 需求和相应的稳定性预算。

**必须验证：** 在真实模型与 provider 凭据可用时，Node live 验收从新的 AuthoringSession 到学习环境答案通过一次；随后重置该 Kind target，确认不会将生成出的测试 Challenge 作为题库内容保留。

### P0 迁移清单

1. 建立专用 Kind/Incus E2E target 的 prepare/reset 生命周期，明确资源所有权和失败清理边界。
2. 发布确定性的 immutable fixture Catalog，并让准备阶段独立验证 control plane 和公开 Catalog projection。
3. 拆分 UI、Node 主路径和 recovery 三个验收入口，移除长 global setup、固定端口和跨套件状态依赖。
4. 将真实 Node Authoring 保留为显式 Live 验收，模型、OpenSandbox、K8s Authoring 和 Assistant 不进入日常 E2E。
5. 更新测试运行与故障诊断文档，完成快速测试、集成测试、平台验收和 Live 验收的边界核对。

### P0 提交计划

P0 按以下五个提交实施。每一项的实现、对应测试、生成物和必要文档必须在该项验证通过后一起提交，再开始下一项；不能先堆积多项修改，也不能把验证集中成最后的独立提交。

1. **`test(e2e): define disposable target lifecycle`**
   - **目标：** 建立 Kind-only 的 E2E target identity、专用 Incus project 与精确 reset 语义，明确 Environment finalizer、Sandbox、Registry、Server 和 PostgreSQL 数据的清理顺序。
   - **验证：** 在专用 target 上完成 reset，确认受管资源全部清理且共享 Incus project/base image 不受影响；非 Kind context 被拒绝，provider 清理失败不会被数据库删除掩盖。
2. **`test(e2e): prepare deterministic catalog target`**
   - **目标：** 建立 `e2e-prepare`，从当前工作树部署平台，经真实 Registry 发布固定 fixture Catalog，并以公开 projection 而非旧状态确认准备完成。
   - **验证：** 从空 target 成功准备当前 control plane 和 fixture；验证 digest、Catalog runtime/Topic/Tag projection 以及准备失败诊断，确认不依赖模型、PVC 文件复制或旧 Catalog。
3. **`test(e2e): split browser and node acceptance`**
   - **目标：** 移除长 global setup 与固定端口假设，建立单一连接所有者；将 UI smoke 与 Node 学习主路径拆成可独立运行的套件，并收敛重复的 runtime 场景。
   - **验证：** UI 套件与 Node 主路径可分别在同一 prepared target 上通过；每个用例的用户、Environment、端口和 cleanup 独立，失败时能得到 trace、日志和资源 identity。
4. **`test(e2e): isolate platform recovery acceptance`**
   - **目标：** 将 Server restart 和 Controller restart 从浏览器基线移为独立 recovery 套件，使用共享连接 helper 和真实 Environment 状态，不保留测试专用数据库或 CRD patch。
   - **验证：** 两个重启场景分别证明已有 NodeEnvironment 可以继续收敛、重连并完成 checkpoint；UI 与 Node 主路径不因 recovery 套件的 Deployment 操作而受影响。
5. **`test(acceptance): isolate node authoring live validation`**
   - **目标：** 提供显式 Node Authoring Live 验收与运行文档，保持模型、OpenSandbox、K8s Authoring、Assistant 和 soak 与日常 E2E 隔离。
   - **验证：** 真实凭据可用时完成一次从 Authoring 到学习环境完成的主路径；失败现场可保留并由 reset 丢弃，且不对模型文本、prompt 或工具调用次数做断言。

P0 完成标准是：任何开发者都能在明确的 Kind E2E target 上从空状态准备固定 Catalog，并分别运行 UI、Node 主路径和 recovery 验收；真实 Node Authoring 主路径在显式 Live 验收中至少通过一次。达到该标准后，才开始将 Linux 领域场景制作成真实可发布题目。

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
