# 参考项目审查后的修改建议

本文件从 [`REVIEW/`](REVIEW/README.md) 的逐项目审查中收敛出应修改的 Breakfix 设计和
实现。它是待确认清单，不代表已经开始实现。目标是让平台能够稳定生产一批真实题目，
而不是引入新的通用学习平台。

## 结论

不重构 Server、Controller、Agent Worker、Environment/VerifyTask CRD 或 Build/Publisher/
Verifier 的信任边界。它们已覆盖参考项目最关键的能力，并且比通用 workshop/评测平台更
适合真实 SRE 故障题。

## 完整可吸收清单

下表汇总 `REVIEW/` 中所有值得吸收的方向。三项 P0 只是首批题目生产前的前置条件，不是
这次审查的全部结论。

| 阶段 | 参考项目 | 借鉴方向 | 对 Breakfix 的具体调整 |
| --- | --- | --- | --- |
| **P0：现在做** | Educates、PrairieLearn、DOMjudge | 单项内容走真实执行链 | 增加 `make verify-challenge`，复用 Build/Publisher/Verifier/Environment，不做本机 mock。 |
| **P0：现在做** | PrairieLearn、Artemis、Open edX | 步骤级学习历史 | 在 checkpoint status 加 `firstPassedAt`，Server 幂等投影每 Environment/checkpoint 的首次通过事件。 |
| **P0：现在做** | Killercoda、Educates | 可读且可校验的教学资产 | 扩展 `internal/challenge` lint：完整 hint、solution checkpoint 标记、本地 Markdown 资源边界。 |
| **P1：首批题后** | CTFd、PrairieLearn、Artemis、Open edX | 学习行为事件 | 记录 hint、solution、assistant 打开事件；不记录完整终端，不引入扣分。 |
| **P1：首批题后** | Artemis、PrairieLearn | 检查点结果体验 | 工作台/完成页按 checkpoint 显示当前状态、首次通过、提示、解答和诊断；作者看到同一份结构化 VerifyTask 摘要。 |
| **P1：首批题后** | Educates、Zero to JupyterHub | runtime 配置与边界 | 集中 Environment runtime profile 的默认值/上限；补 workspace/vcluster NetworkPolicy 的真实边界验收。 |
| **P1：首批题后** | DOMjudge | 验证报告可重跑性 | 每次重新验证保留独立 VerifyTask/report；补基础镜像和 runner 变更后的 re-verification campaign 设计。 |
| **P2：一个领域 12-20 题后** | Killercoda Groups、Open edX | 人工学习路径 | `data/paths/` 引用已发布的 Skill/Challenge ID；路径是 taxonomy 的视图，不强制解锁。 |
| **P2：有学习事件后** | Artemis、PrairieLearn、CTFd | 题目质量分析 | 聚合 checkpoint 流失、完成率、耗时、重置和提示依赖；按 revision 供作者修订题目。 |
| **P2：有内容需求后** | Play with Docker、Educates、Killercoda | 用户服务预览 | 仅提供按 Environment/端口授权的内部预览代理，环境回收即失效；不允许任意 Ingress。 |
| **P3：有并发数据后** | Zero to JupyterHub、Play with Docker、Educates | 容量与清理运营 | 指标化活跃环境、启动时延、checkpoint 延迟、清理吞吐；再决定 culling 调度和资源配额。 |
| **P3：证明确有瓶颈后** | Kubernetes/运行时实践 | checkpoint 传输通道 | 仅在 `pods/exec` 指标证明瓶颈后，按 `NEXT.md` 迁移到 workspace 内 checkpointd。 |

## 已确认正确，暂不改动

| 参考项目 | Breakfix 已有对应能力 | 结论 |
| --- | --- | --- |
| Educates | 一用户一套真实环境、运行时初始化、镜像供应链边界 | 不替换为 Workshop/Portal。 |
| JupyterHub | 可恢复环境状态、短期终端凭据、清理生命周期 | 补恢复测试覆盖，不引入 Hub/Proxy/长期 home。 |
| CTFd | 用户题目状态与提示展示 | 不引入 Submit、flag、积分或强制解锁。 |
| PrairieLearn、DOMjudge | 独立可信验证执行者和结构化结果 | 维持 VerifyTask 与 Builder/Publisher/Verifier 分离。 |
| Open edX | 发布内容与学习事实分离 | 保持文件系统 artifact、PostgreSQL 事件和 taxonomy snapshot 分层。 |
| Play with Docker | 临时终端和服务可达性问题 | 不采用 privileged DinD、文件状态或固定 session TTL。 |

## 当前实施决策

近期应实施以下三项 P0 改动，并按顺序完成：

1. 单题真实作者验收和内容 fixture。
2. checkpoint 首次通过的持久学习事实。
3. 教学资产的结构约束和 lint。

## 1. 单题真实作者验收和内容 Fixture

### 现状

`make docker-challenge NAME=...` 只构建并推送镜像；完整语义验证由 VerifyTask 执行。
这对发布流程正确，但人或 agent 批量制作题目时，缺少一条针对一个未发布目录的明确验收
入口。当前 `test/fixtures/challenges/` 也只有一个 vcluster 样例，无法覆盖题目格式和工作台
的主要组合。

### 建议修改

新增开发者专用的：

```text
make verify-challenge NAME=<source-slug>
```

它必须将指定目录走**当前真实 VerifyTask 路径**：构建候选、Publisher 推送 staging 镜像、
创建相同 runtime 的临时 Environment、等待 `generate.sh`、执行 `answer.sh`、运行全部
checkpoint，并输出已有的结构化 VerifyTask report。

约束：

- 不实现 Docker-only、本机 mock 或第二份 verifier。
- 不通过公开 HTTP API 接受任意 artifact；这是本地开发/受控 CI 入口，不恢复已经删除的
  用户上传接口。
- 验收 artifact 不提升到 `data/challenges/`、不进入 taxonomy、不出现在 Catalog。
- 成功和失败都清理临时 VerifyTask、Environment、Job 和 staging image；清理失败本身必须
  使验收失败。

同时扩展已有 `test/fixtures/challenges/`，保留小而固定的样例矩阵：container 单检查点、
container 多检查点依赖、vcluster、运行时初始化、Markdown 提示/解答。它们只供测试，
不能作为公开题目混入 `data/challenges/`。

### 验收

- 一个 container 和一个 vcluster fixture 均能在真实集群完整通过。
- 至少覆盖构建失败、`answer.sh` 失败、某 checkpoint 失败和资源清理。
- `make verify-challenge` 的成功/失败 report 与正式 VerifyTask API 投影字段一致。

### 参考

Educates 的可部署 workshop sample、PrairieLearn 的 external grader 和 DOMjudge 的独立
judgehost 都强调：内容作者必须能对单项内容运行真实执行路径。详见
[`REVIEW/educates-training-platform.md`](REVIEW/educates-training-platform.md)、
[`REVIEW/prairielearn.md`](REVIEW/prairielearn.md) 和
[`REVIEW/domjudge.md`](REVIEW/domjudge.md)。

## 2. Checkpoint 首次通过的持久学习事实

### 现状

`Environment.status.checkpoints` 只有最近一次检查报告；`user_challenge_progress` 记录
题目整体完成。页面能展示当前已过数量，但题目完成或环境被回收后，无法准确回答某次
attempt 中每个 checkpoint 何时首次通过。将每四秒的失败 `details` 全写入数据库则会把
轮询日志误当学习数据。

### 建议修改

在 CRD 与 PostgreSQL 间保持现有所有权，但补充一个最小、不可变的进度事实。

1. 在 `CheckpointResultStatus` 增加 Controller-owned 的 `firstPassedAt`。Controller 仅在
   一个 Environment 内该 checkpoint 从未通过变为通过时写入，并在后续检查中保留它。
2. Server 投影 status 时，将每个 `firstPassedAt` 幂等插入新表，例如
   `checkpoint_pass_events`：

   ```text
   environment_uid + checkpoint_id  唯一键
   user_id, challenge_id, challenge_revision
   checkpoint_id, first_passed_at, summary
   ```

3. Reset 会生成新的 Environment UID，因此是新的 attempt event；同一题目的历史 attempt
   不互相覆盖。Server 重启后重新观察到 status 也只能 `INSERT ... ON CONFLICT DO NOTHING`。
4. Workbench 仍读取 CRD 的最新 `passed/summary/details`；My Space、完成复盘和未来分析
   读取数据库事件。Controller 仍是唯一 status 写者，Server 不推断或修改通过时间。

不存储每次失败、不存储完整终端内容，也不把 checkpoint 变成可累加分数。所有 checkpoint
通过仍是唯一完成条件。

### 验收

- Controller 重启、Server 重启、终端重连后，首次通过时间不丢失、不改变。
- checkpoint 反复通过/失败只产生一条该 Environment 的首次通过事件。
- Reset 后同一用户/题目的新 Environment 产生新的事件；旧历史保留。
- container 与 vcluster 的真实验收均证明这一投影；页面可显示当前状态与历史首次通过。

### 参考

PrairieLearn 的 variant/submission 历史与 Artemis 的测试级结果都将“最近界面状态”和
“可回顾的学习结果”分开保存；Open edX 同样分离已发布内容和学习完成事实。详见
[`REVIEW/prairielearn.md`](REVIEW/prairielearn.md)、
[`REVIEW/artemis.md`](REVIEW/artemis.md) 和
[`REVIEW/openedx-platform.md`](REVIEW/openedx-platform.md)。

## 3. 教学资产结构和内容 Lint

### 现状

`internal/challenge.ValidateDir` 已校验必需文件、checkpoint ID、title、description、hint
文件和依赖关系；Generator 也有额外语义校验。现有格式仍允许 `solution.md` 与 checkpoint
之间没有机械可验证的对应关系，且只校验 hint 路径，不校验题面/解答 Markdown 的本地资源
引用。量产后这会产生“检查点已经通过，但解答没有解释这一环”的内容债务。

### 建议修改

不新增另一份 YAML 或把题目限制为固定操作步骤，而是在现有 `internal/challenge` 校验层补充
轻量内容契约，并让 generator、发布目录和 fixture 都复用它：

1. 每个 checkpoint 必须有一个 hint 文件。提示仍是渐进诊断方向，不能替代完整答案。
2. `solution.md` 必须按 checkpoint 有可读章节，并以稳定标记关联。例如每节前使用
   `<!-- checkpoint: <id> -->`；lint 要求每个 ID 恰好出现一次。章节内容仍可自由组织，
   不要求固定命令或固定文件路径。
3. `problem.md`、`solution.md` 和 hint 内的相对资源链接必须解析在 challenge 根目录内；
   拒绝 `..` 逃逸、绝对本地路径和未打包资源。外部 HTTPS 链接可保留，但不能是 runtime
   成功的必要依赖。
4. lint 错误返回具体 checkpoint/文件/引用，供 Generator/Judge 修复；不能为缺失内容补
   默认值或静默跳过。

### 验收

- 缺失 hint、重复/缺失 solution marker、路径逃逸和缺失资源均被同一验证层拒绝。
- 多 checkpoint challenge 允许任意正确修复路径，检查器依旧只观察最终状态。
- 真实发布的 `cleanup-logs` 迁移到新契约后，VerifyTask 和工作台照常运行。

### 参考

Killercoda 的同目录步骤资产、Educates 的 Markdown/action sample 都证明教学材料必须有
可读结构；但它们的命令注入和点击验证不适合 Breakfix。详见
[`REVIEW/killercoda-scenario-examples.md`](REVIEW/killercoda-scenario-examples.md) 和
[`REVIEW/educates-training-platform.md`](REVIEW/educates-training-platform.md)。

## 后续但现在不做

| 项目 | 触发条件 | 方向 |
| --- | --- | --- |
| Hint/solution/assistant 使用事件 | 首批题目已上线 | 记录轻量事件，分析提示依赖和卡点；不扣分。 |
| 学习路径 | 一个领域有 12 到 20 道已验证题 | `data/paths/` 人工编排已发布 Skill/Challenge ID，不强制解锁。 |
| 题目质量看板 | 有稳定学习事件 | 聚合 checkpoint 流失、完成率、耗时和重置率。 |
| 受控服务预览 | 真实题目需要浏览器访问用户服务 | 按 Environment/端口授权的内部代理，不创建任意 Ingress。 |
| Re-verification campaign | 基础镜像或 runner 有平台级变更 | 新 VerifyTask 验证旧 artifact revision，不改写旧报告。 |
| checkpoint 执行通道迁移 | 指标证明 API `pods/exec` 是瓶颈 | 按 `NEXT.md` 的 checkpointd 设计迁移，不提前引入。 |

## 明确不改

- 不恢复任何用户可见 Submit、上传 artifact 或 flag 验证。
- 不采用 CTF 积分、排行榜、题目强制解锁、隐藏 checkpoint 或提示惩罚。
- 不用 Educates/JupyterHub/Open edX 替换 Server、Controller、CRD 或文件系统题库。
- 不引入长期用户 workspace、Hub/Proxy、LMS、Git/CI 作业模型、通用题型插件或 privileged
  DinD。
- 不为检查点引入 sidecar、systemd、tmux 或 kubelet probe；当前 `pods/exec` 直到有指标
  证明瓶颈前保持不变。

## 当前确认的实施范围

现在只完成前述三项，不新增公开 challenge，也不开始一个领域的题目生产。唯一允许新增的
challenge 目录是 `test/fixtures/challenges/` 下的固定测试资产；它们不能进入
`data/challenges/`、taxonomy 或 Catalog。

实施顺序是 **1 -> 3 -> 2**，而不是按文档章节编号机械并行推进。

### 阶段一：单题真实作者验收和 Fixture

先实现第 1 项。目标是让一个未发布 challenge 目录能够通过当前真实 VerifyTask 路径得到
可信结果，不能用 Docker-only 或本机模拟替代。此阶段同时补齐 container/vcluster 的固定
fixture，但不创建任何公开题。

只有以下验收都通过，才能进入下一阶段：

- 指定 fixture 可经 Build、Publisher、真实 runtime init、`answer.sh` 与全部 checkpoint
  完整通过。
- 构建失败、答案失败、checkpoint 失败均返回正式 VerifyTask 结构化 report。
- 每个成功或失败路径都清理临时 Environment、Job、VerifyTask 和 staging image。

### 阶段二：教学资产结构和内容 Lint

再实施第 3 项，使现有 `cleanup-logs` 和第一阶段 fixture 都遵守最终内容契约。校验必须在
`internal/challenge` 这一份共享验证层实现，Generator、发布和 fixture 不能各自维护规则。

这一阶段完成条件：

- 每个 checkpoint 都有 hint 和唯一的 `solution.md` 章节标记。
- 所有 Markdown 本地资源引用都留在 challenge 根目录内。
- 缺失/重复章节、路径逃逸和缺少资源均被静态验证拒绝，且 `cleanup-logs` 的真实 VerifyTask
  与工作台不回归。

### 阶段三：Checkpoint 首次通过学习事实

最后实施第 2 项。先完成 `firstPassedAt` 的 Controller status 语义，再完成 Server 到
PostgreSQL 的幂等投影，最后才在工作台和个人空间展示历史。不能由 Server 猜测通过时间，
也不记录四秒轮询产生的失败日志。

这一阶段完成条件：

- container/vcluster 中每个 checkpoint 首次通过只产生一条 attempt event。
- Controller/Server 重启和终端重连不改变已记录的首次通过时间。
- Reset 创建新的 Environment attempt，旧事件仍可复盘。
- 当前 checkpoint 状态仍由 CRD 提供，长期学习历史只由数据库事件提供。

三阶段均通过真实 container/vcluster 验收后，才讨论首批 5 到 8 道题；在那之前不建设学习
路径、质量看板、服务预览、再验证 campaign 或 checkpointd。
