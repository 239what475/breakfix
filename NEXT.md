# 下一阶段方向

## 当前阶段结论

当前阶段的目标是完成单道挑战的完整产品闭环。这一目标已经达成：用户可以发现题目、启动和恢复环境、通过自动检查点获得进度、查看提示和解答、使用基于终端上下文的助手、重置环境，并在个人空间回顾学习与创作记录。作者可以通过对话审核题意，由生成与真实验证循环产出可发布的题目。

因此，下一阶段不应继续增加与单题工作台重复的页面或按钮。平台长期最稀缺的是经过验证、彼此有明确学习关系的题目内容；但在生产大量题目前，必须先建立稳定的技能图基础设施。

## 下一阶段：技能图基础设施

本阶段不生成题库，也不批量生产 challenge。目标是建立 Skill、Tag、Mapping、发布和 Catalog 的基础契约，并只用现有的已验证 challenge 完成端到端验证。所有未来题目仍必须经过既有的生成、审核和真实 `VerifyTask` 验证闭环；题意、初始环境、检查点、提示、解答和验证结果必须围绕同一组环境事实。

技能图不是题目数量足够后才添加的展示功能，而是后续题库从第一道题开始就必须遵守的内容契约。若先积累数百道孤立题目，再补前置关系和学习顺序，关系将不可靠且维护成本很高。

### Challenge 与 Taxonomy 的独立 workflow

Challenge 与 Tag/Skill 是两套独立体系。Challenge workflow 只负责生成、审核并真实验证可运行的题目资产；它不生成、不修改、也不依赖任何 Tag 或 Skill ID。Taxonomy workflow 读取最终验证成功的 challenge artifact，为它附加 Tag 和 Skill 关系。所有题目都经过同一套 Challenge workflow，分类体系因而不会被各个生成任务随意塑形。

```text
Challenge workflow
  作者意图 -> generator/judge -> VerifyTask -> 作者确认 -> 已验证 challenge

Taxonomy workflow
  已验证 challenge -> mapping workflow -> 静态校验 -> Catalog 发布
```

作者可以在题目意图中用自然语言说明希望练习的能力，但这只是非结构化的学习意图，不能成为 challenge 内的最终 Tag 或 Skill 关系。Taxonomy workflow 以最终的 `problem.md`、`solution.md`、检查点、答案和真实验证结果为依据，描述题目实际练习的内容。

公开 Tag 的唯一事实来源是 Challenge mapping。项目仍处于开发阶段，因此采用一次性干净切换：现有公开 challenge 也按新 workflow 重新产生 taxonomy，随后在同一次改动中删除 `challenge.yaml.tags`、作者草案中的结构化 Tag、生成 prompt、Catalog API 和前端中读取 artifact Tag 的逻辑。不得保留旧 Tag 数据迁移、运行时回退或任何兼容路径。

平台不维护“题目依赖题目”的树，也不把所有知识强行压成一条学习路线。发布后的内容关系由独立 mapping 表达：

```text
Skill --requires--> Skill       受控的学习依赖 DAG
Mapping --entry--> Skill        挑战默认假定的已有能力
Mapping --outcome--> Skill      挑战练习或应用的能力
Mapping --tag--> Tag            题库检索与浏览维度
Mapping --challenge--> Challenge 被分类的已验证题目
```

`Skill.requires` 是唯一必须无环的跨内容依赖。它表达“合理完成该能力练习前通常应已具备什么”，不是强制解锁规则。用户可以直接开始任意挑战；Catalog 和工作台只应清楚展示建议先练习的能力与相应入门题。

因此必须区分两类关系：`Skill.requires` 是全局能力图的边，只有题目的实际学习前置发生变化时才建立或调整；Challenge 与 Skill 的 `entry`、`outcome` 关系则描述一题假定和训练了哪些能力。每个已验证 challenge 都必须完成后者的 mapping；它不必为每个 mapping 新建或修改 `Skill.requires`。

一道 challenge 的 mapping 可以覆盖多个技能，因为真实运维场景经常跨越网络、系统、容器和 Kubernetes；但通常只能有一个主要学习目标。其他技能应明确是进入题目时预期具备的能力，还是在题目中被强化的次要结果，不能用宽泛标签模糊代替。

现有 `Checkpoint.DependsOn` 继续只表达同一道挑战内部的完成顺序，不能被解释为跨题学习依赖。检查点完成说明用户在该环境中完成了可观察状态；用户资料应记录“练习过”或“应用过”某项技能，不能把单次通过表述为“已精通”。

### Skill、Tag 与 Challenge 的边界

Skill 是可独立解释、可在多个题中复用的能力。例如“SSH 本地端口转发”和“识别监听 TCP socket”是技能；“网络”或“Linux”过于宽泛，而单个命令参数又过于细碎。

Tag 保留，但只服务于 Catalog 的快速筛选和浏览，使用受控词表，例如 `ssh`、`networking`、`linux`、`incident-response`。Tag 不表达前置关系、不拥有 challenge，也不应精确复述技能；`ssh-local-port-forwarding` 应是 Skill 而不是 Tag。这样 Tag、Skill、Challenge 不是三层包含树，而是 mapping 同时连接的几个不同维度。

### 当前 Mapping Workflow

当前阶段只实现由已验证 challenge 触发的 mapping workflow，不引入独立的 Dream 或周期性语义 review。每个任务固定读取一个 taxonomy revision 和最终的 challenge artifact，产出一个完整候选变更集：复用已有 Skill/Tag，建立 Challenge 与 Skill 的 `entry`、`outcome` 关系及 Challenge 与 Tag 的关系；只有确认缺少可复用能力或稳定浏览维度时，才提出新增 Skill/Tag，并在必要时提出 `Skill.requires` 关系。

“找不到同名条目就创建”不是合法规则。新增 Skill 必须是可独立解释、可在多题复用的能力；新增 Tag 必须是非同义、非技能细节、能服务于多题浏览的受控词表项。Challenge generator 不能直接写入 taxonomy。

每个 mapping Work Item 是一个完整的三 agent 委员会任务，不拆成三个独立队列任务。Mapper 基于固定 revision 产出 ChangeSet；严格 JSON 解码与基础静态校验先拒绝格式错误、悬空引用或图约束错误的候选，绝不剥离 Markdown、补字段或猜测引用。合法 Candidate 必须先持久化，再交给 Curriculum Reviewer 和 SRE Reviewer 并行返回结构化 `approve` 或 `reject`。reviewer 的半成品结论不持久化：只有两者都返回合法结构化结论，才将这一对结论作为本 round 的正式审查结果保存；任一 reviewer 的调用或输出失败时，下一次重新并行运行整个 reviewer 对。

`Round` 只表示一次完整的、可解释的语义审查循环。任一正式 reviewer 结论为 `reject` 时，才完成当前 round：新的 Mapper Run 读取 Candidate 与两份意见后修订，再进入下一 round 的 reviewer 审查。两个 reviewer 对同一个 ChangeSet 都通过，任务才进入 Publisher 在最新 revision 上执行最终确定性校验和发布。语义 round 没有人为上限，也不存在“审查若干次后强制通过”。

模型调用错误、超时、typed result 缺失、无效 JSON 或静态候选校验失败都只是技术失败，不得创建新的语义 round。一个 Work Item 在当前 round 共享最多 10 次技术失败预算；Mapper Run 失败时只重试 Mapper Run，reviewer 阶段失败时重新运行完整 reviewer pair Run。重试次数只在已启动的 agent 调用全部结束后结算，预算耗尽绝不取消仍在执行的 agent；单次调用仍受自身超时与 server shutdown context 约束。技术失败保留当前 Candidate 和上一轮完整 reject 意见，以便下一 Mapper Run 获得完整修订上下文。

同一 round 累计 10 次技术失败后，“失败”的是本次执行而不是 Work Item：持久化最后错误和执行失败次数，计算带退避的 `next_run_at`，释放租约并重新放入 Work List。下一次调度继续同一个 Work Item 和同一 round，但创建新的通用 Agent Run，并重新开始该 round 的 10 次技术失败预算。只有 challenge artifact 消失或 revision 改变才进入 `Cancelled`，不再重试。每次 Mapper 成功、正式 reviewer 对完成或 Publisher 发布都持久化进度并释放 worker 租约，避免长时间独占 worker。

多个 Work Item 可以并行运行上述三 agent 审查循环。所有候选只进入一个无模型的 Publisher 队列，Publisher 是唯一可以写入已发布 taxonomy revision 的组件。若候选的 base revision 仍为当前 revision，Publisher 运行静态校验并发布；若候选已过期，仅修改一个 Challenge mapping 且其引用的 Skill/Tag 定义未变时，Publisher 可以在最新 revision 上重新运行确定性校验后提交。任何新增或修改 Skill、Tag、`Skill.requires` 的过期候选都不能机械 rebase，必须以最新 revision 重新运行完整的审查循环，避免并发 workflow 静默创建重复定义或错误前置边。

Work List 是运行状态而不是 taxonomy 内容，必须持久化在数据库。每个 Work Item 至少保存目标 challenge revision、base taxonomy revision、当前阶段及其 active Agent Run、当前 Candidate、成对的正式 review 结论、语义 round、当前 round 的技术失败次数、执行失败次数、`next_run_at`、状态、最后错误、租约 owner 与过期时间；同一 challenge revision 的同类任务必须去重。Mapper 与 reviewer pair 均是无供应商 session 的通用 Agent Run，Worker attempt 只由 PostgreSQL 租约恢复。`next_run_at` 保证重试任务不会被 worker 立刻重新 claim 而形成热循环。数据库租约保证任意时刻只有一个 Publisher 执行发布，但数据库不保存“当前 taxonomy 是什么”的权威值。Publisher 获得租约后读取 `current`、创建新 revision、原子切换 `current`，再释放租约；进程崩溃后的接管者以文件系统中的 `current` 为准恢复工作。

当前的工作队列只需要处理 mapping 任务及其修复重做。结构性错误由确定性扫描器直接发现并创建修复任务，例如未映射的已验证 challenge、challenge revision 失效、悬空引用、ID/title 不一致、缺少或重复 primary outcome，以及 `Skill.requires` 成环；这些检查不需要模型参与。

### 内容定义与关系 Mapping

Skill、Tag 与关系必须分层存放。`skills/*.yaml` 和 `tags/*.yaml` 是内容对象的定义，不能保存它们关联了哪些 challenge、Tag 或其他 Skill；所有跨对象关系都由 `mappings/` 中的文件表达。这样对象定义能稳定描述“它是什么”，而关系可以随题目、技能图和分类演化独立修改。

每个 Skill 至少包含稳定 ID、title、能力定义与 mapping guidance。定义说明学习者获得了什么可复用能力；mapping guidance 用自然语言说明何时应将该技能视为 outcome、何时只是题目预期的 entry skill，以及哪些相似场景不应归入该技能。它是分类和审查 agent 的内容契约，不是依靠关键词匹配的机械规则：

```yaml
kind: Skill
id: skill-01...
title: Repair permission-related service failures
definition: >
  Diagnose and correct file ownership or permission states that prevent a
  service or scheduled maintenance task from operating correctly.
mapping_guidance:
  outcome_when:
    - The challenge requires diagnosing and correcting a permission-related failure.
  entry_when:
    - The challenge assumes the learner can inspect ownership and mode bits.
  exclude_when:
    - Permissions are mentioned incidentally but are not part of the diagnosis or repair.
```

每个 Tag 同样包含稳定 ID、title、定义与 mapping guidance。Tag 的定义回答它服务于哪一种 Catalog 浏览维度；其 guidance 应排除仅因基础镜像、文件名或偶然提及而产生的误分类：

```yaml
kind: Tag
id: tag-01...
title: Linux
definition: Linux operating-system administration and troubleshooting.
mapping_guidance:
  include_when:
    - The primary environment, diagnosis, or repair concerns Linux behavior or administration.
  exclude_when:
    - Linux is merely the container base and not part of the learning content.
```

`Skill.requires` 也不写进 Skill 定义文件。若所有关系都属于 mapping，则必须有两类关系文件：Challenge mapping 表达一题与 Tag/Skill 的关系，Skill mapping 表达一个 Skill 到其前置 Skill 的有向关系。没有 `requires` 边的 Skill 不需要对应的 Skill mapping 文件。

title 改名只需要原子更新所有 mapping 中的显示快照；Skill/Tag 的 `definition` 或 `mapping_guidance` 变化、以及 `Skill.requires` 关系变化，都会改变既有分类关系的语义。Taxonomy workflow 必须为所有受影响的 Challenge mapping 创建重新审查任务，不能只更新快照后继续视为有效。

### 文件系统内容权威

题库继续以文件系统为唯一内容权威。Challenge 需要携带多个资产，单独使用目录；Skill、Tag 和 Mapping 是一个必须整体读取的 taxonomy revision，使用不可变快照目录。Skill、Tag 和 Mapping 仍是可读的 YAML 文件，文件名用于仓库可读性，ID 用于稳定引用、API、用户学习记录、镜像和跨内容关系：

```text
data/
  challenges/
    repair-logrotate-permissions/
      challenge.yaml        # id: challenge-<opaque-id>
      ...
  taxonomy/
    revisions/
      <revision>/
        skills/
          ssh-local-port-forwarding.yaml
        tags/
          ssh.yaml
        mappings/
          challenges/
            repair-logrotate-permissions.yaml
          skills/
            repair-permission-related-service-failures.yaml
    current -> revisions/<revision>
```

Challenge 目录名、Skill/Tag/Mapping 文件名都是稳定、可读的 source slug，不是关系键；改名不能破坏用户记录和技能图。`id` 保持与题意解耦，并且是唯一允许用于 API、数据库、图校验和关系解析的身份。`<revision>` 是完整 taxonomy tree 按确定顺序计算的 `sha256` 内容哈希；相同内容不创建新 revision，已经发布的 revision 永不修改。初期保留全部历史 revision，用于审计和回滚。

Taxonomy workflow 在临时目录构造完整候选 revision，校验成功后将其写入 `revisions/<revision>`，再原子替换 `current` 指针；Server 只扫描 `current` 与已发布 challenge，构建按 ID 查询的不可变 catalog index。因此 Server 永远不会读取到新增 Skill 已写入、相应 Mapping 尚未写入的半成品；旧 revision 也可用于审计和回滚。Server 只有在当前 taxonomy 中找到唯一 Challenge mapping，且 mapping 的 challenge ID 和 revision 都与当前已发布 artifact 精确一致时，才将该 challenge 放入 Catalog；不匹配的 challenge 保持不可见并等待 mapping 修复。请求路径不应依赖 `data/challenges/<id>` 这样的目录约定。

Mapping 记录关系时同时保留 `id` 与 `title`：ID 是唯一关系键，title 是方便人直接阅读和 review 的显示快照。Challenge mapping 记录 challenge revision、Tag、entry Skill 和 outcome Skill。mapping workflow 读取题目的 problem、solution 和检查点来作出分类，但不为每个 checkpoint 保存额外的 Skill 归因或模型推理文本。示例：

```yaml
# data/taxonomy/current/mappings/challenges/repair-logrotate-permissions.yaml
challenge:
  id: challenge-01...
  title: Repair logrotate permissions
  revision: sha256:...

tags:
  - id: tag-01...
    title: Linux
  - id: tag-02...
    title: Permissions

entry_skills:
  - id: skill-01...
    title: Inspect file ownership and permissions

outcomes:
  - id: skill-02...
    title: Repair permission-related service failures
    primary: true
```

Skill mapping 只记录由 source Skill 指向 prerequisite Skill 的 `requires` 边：

```yaml
# data/taxonomy/current/mappings/skills/repair-permission-related-service-failures.yaml
source:
  id: skill-02...
  title: Repair permission-related service failures
requires:
  - id: skill-01...
    title: Inspect file ownership and permissions
```

`title` 绝不能参与关联或查找。发布校验必须要求 mapping 中的 title 与相应 Challenge、Skill、Tag 当前 title 完全一致；不一致直接失败，不做默认值或模糊匹配。重命名 Skill 或 Tag 时，Taxonomy workflow 在新 revision 中更新所有相关 mapping，保留可见的 review diff。Mapping 绑定 challenge revision，因此 challenge 内容变化后必须重新经过 Taxonomy workflow；仅修改分类关系不需要重新构建镜像或运行 VerifyTask。

### 内容校验与生产

`VerifyTask` 继续只验证运行时环境、`answer.sh` 和真实检查点是否成立。它不读取 Taxonomy，也不能证明题目教学关系正确。Challenge 通过真实验证并经作者确认后，Taxonomy workflow 才能创建或更新 mapping；公开 Catalog 的发布需要同时通过这两个独立的门：

```text
已验证的 challenge + 作者确认 + 合法的 taxonomy mapping = 公开 Catalog challenge
```

Taxonomy workflow 的静态校验至少包括：

- 每个公开 Challenge 在当前 revision 中恰有一个 mapping，且 mapping 的 ID、revision 与已发布 artifact 精确一致；每个 mapping 至少有一个 Tag 和一个 outcome。
- 所有 Skill 和 Tag ID 存在，当前 revision 中 `mappings/skills` 构成的 `Skill.requires` 图无环。
- Mapping 中的 title 与引用对象当前 title 完全一致。
- `entry_skills`、`outcomes` 与每个 `requires` 列表内部均不重复；entry Skill 不能同时是 outcome，outcome 中恰有一个 `primary: true`。
- 每个 source Skill 最多有一个 Skill mapping；`requires` 不能引用 source 自身。
- Tag 来自受控词表；新增 Tag 必须证明其是可复用的浏览维度，避免同义词和把 Skill 当 Tag 使用。
- 新增 Skill 不能由 Challenge generator 静默创建；它只能由独立 Taxonomy workflow 提出、审核并发布，且必须说明能力定义、前置关系和预期题目覆盖。

Skill Committee 属于 Taxonomy workflow，而不属于 Challenge generator。它负责基于已验证 artifact 提出和审查 Skill/Tag/Mapping 变更；Challenge generator 不需要理解完整技能本体，也不能直接写 taxonomy 文件。

## 后续阶段：基于技能图的题库生产

只有在技能图基础设施经过现有 challenge 的端到端验证后，才开始按技能蓝图生产新题目。本阶段先编写完整的技能覆盖蓝图，再按蓝图生成、审核和验证 challenge。优先覆盖：

- 容器与 Linux 故障排查：进程、日志、权限、网络、systemd、磁盘与资源问题。
- Kubernetes 与 vcluster 故障排查：工作负载、Service/DNS、配置、RBAC、存储、调度与滚动发布问题。
- 每个领域从基础观察和定位，到跨组件诊断和修复，形成多个可选学习路线，而不是一组互不相关的题。

先选择一个边界清晰的领域，例如网络与 SSH，完成 12 到 20 道真实题作为试点。试点用于校验技能粒度、复合题映射、推荐体验、静态内容校验和目录组织方式。模型稳定后，再按覆盖蓝图扩展到数百题；目标是每个核心技能都有多道高质量练习，而不是只追求挑战总数。

题库扩展不能降低现有契约。每道发布题目应满足：

1. `problem.md` 明确说明场景、可观察症状、目标和边界；不以模糊的“修复问题”替代任务。
2. 每个检查点验证一个用户可观察的最终状态，而不是规定命令、文件名或固定操作路径。
3. 提示按检查点提供渐进帮助；`solution.md` 解释诊断过程和修复理由，而不只给出命令。
4. `answer.sh` 能在运行时初始化后的真实环境中通过全部检查点；`VerifyTask` 是唯一可信的运行时自动验收。
5. 同一主题的题目避免重复考察同一技巧；公开 Catalog 前必须具备运行时、难度和经过 Taxonomy workflow 校验的 Tag/Skill mapping。

题目应优先来自真实的运维故障模型。生成 agent 可以加快实现，但作者必须审核题意是否具体、检查点是否稳定、解答是否具有学习价值。

## 内容规模具备后的产品方向

以下功能依赖足够的题目数量或真实学习数据，现在不应抢先实现：

### Taxonomy Dream 与 Review

当前不存在值得让 Dream 独立审查的额外事实：新题的分类、Skill/Tag 的提出和关系建立均属于 mapping workflow，引用完整性和图约束由确定性扫描器处理。不得为了“自治”而运行没有明确证据来源的周期性 agent 审查。

当题库已有足够多的 mappings，且积累了可靠的学习事件后，再引入只创建 review 任务、不直接修改内容的 Taxonomy Dream。它可以基于重复或重叠的 Skill、长期单题覆盖的 Skill、`entry/outcome` 与 `requires` 关系的矛盾、题目修订后的分类漂移，以及聚合完成率、检查点流失和提示依赖等证据，提出局部 taxonomy review。review workflow 与 mapping workflow 共用同一套候选变更、审查、静态校验和原子发布机制；Dream 本身不能生成或写入 patch。

### 学习路径与技能地图

学习路径是技能图上的人工编排视图，不是另一套题目依赖事实。检查点解决一道题内部的目标；Skill DAG 解决能力之间的推荐前置；路径则为特定学习目标选择其中一条可理解的路线。

路径可以继续使用文件系统作为内容权威来源，例如 `data/paths/<id>/path.yaml` 引用已发布的 challenge 和技能目标。用户在路径中的完成进度属于用户数据，应保存在数据库。初期只提供推荐顺序、已满足的 entry skills 与继续学习，不强制锁定后续题目。

用户体验不应默认展示一张包含数百节点的全局图。Catalog 按 Tag 和领域筛选；单题展示进入题目的预期能力、当前检查点和下一步建议；学习路径或技能地图只展示当前领域的局部子图。

### 题目质量分析与反馈

当有稳定用户量后，记录并聚合学习事件：启动、重置、检查点首次通过、完成、提示/解答打开和助手求助。作者应能查看完成率、检查点流失、中位完成时间、重置率和提示依赖度，以发现描述不清、难度失衡或检查不稳定的题目。

同时提供“报告问题”入口，将题目版本、运行时和当前检查点附给作者或维护者。不要收集或展示用户终端全文。

### 复盘与推荐

在学习路径和事件数据存在后，再提供完成后的检查点复盘、基于解答的复习内容、稍后学习队列和每日推荐。它们应服务于连续学习，而不是为了增加孤立的互动功能。

## 暂缓方向

独立 Playground、讨论区、排行榜、证书、付费体系、团队/LMS/SSO 集成均不属于下一阶段。它们需要更大的题库、稳定用户群或组织级需求才能产生价值。

并发配额、资源预算和滥用控制是公开大规模使用前的运行平台能力；在当前内容建设阶段保持现有策略，待真实并发需求出现后单独设计。
