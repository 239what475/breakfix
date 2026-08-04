# TODO: 领域化运维与 SRE 题库

> 题库不按零散技术名词扩张。先确定少数稳定的运维领域，再逐个把每个领域做成完整、可验证、可复用的学习内容。本文是下一阶段的内容、分类与 Roadmap 迁移计划；平台长期契约仍以 [`docs/`](docs/README.md) 为准。

## P0：Roadmap 与 Catalog 迁移

题目的课程归属和题库路线是两件不同的事，必须由不同阶段维护，不能让一个后置 workflow 既重写题目分类又维护全局关系。

```text
Generate + 作者审核
  Challenge --belongs_to--> Topic       # 恰有一个
  Challenge --has_tag--> Tag            # 零到多个

Roadmap Workflow
  Topic --roadmap edge--> Topic
  Challenge --roadmap edge--> Challenge
```

### Roadmap 内容检索

`Domain`、`Topic` 和 `Tag` 都属于同一份可版本化的 Roadmap 内容；遗留 `Skill` 和第二套分类模型均不再存在。Server 持有一份 immutable `RoadmapRevision`，其中同时包含定义、Challenge 的 Topic/Tag 绑定以及 Topic/Challenge 关系图。分类 Agent 不能把完整词表作为隐式上下文猜测，也不能直接读写 revision；它通过只读 Roadmap Retrieval 查询当前 revision，并在一次 Agent run 内固定同一个 `roadmap_revision`。这样候选引用始终可复现，也避免定义在运行中变化造成前后不一致。

- Classifying Agent 的首次分类运行只提供四个 typed、只读工具：`search_topics(query, domain_id?, limit)`、`read_topic(id)`、`search_tags(query, limit)` 与 `read_tag(id)`。搜索结果只返回足够比较的 `id`、`title`、摘要、Domain 和匹配原因；需要完整定义时再显式读取。
- Roadmap Planner 的 task input 只携带当前 subject；它只使用四个 typed、只读工具：`search_topics(query, domain_id?, limit)`、`read_topic(id)`、`search_challenges(query, topic_id?, limit)` 与 `read_challenge(id)`。两种 search 均在固定 revision 的全部已发布实体中检索，Server 自动排除当前 subject；搜索结果只返回足够比较的紧凑索引，需要完整定义或题目内容时再显式读取。固定快照只定义本次需要处理和最终合并的 entry，不是给模型遍历的另一份目录。
- Server 在 RoadmapRevision 发布时建立检索索引，并为工具调用返回所固定的 revision。Classifying Agent 只能引用该 revision 中存在的既有定义，或提出等待作者审核的新 Topic/Tag 候选；Roadmap Planner 只能提出已发布实体之间的关系；两者都没有写入全局 Roadmap 的工具。
- 初始排序先保证精确 `id` 和 title 的命中优先，再使用字段加权的 BM25F 排序 `title`、`definition`、`scope`、`challenge_guidance` 与 Tag description。中文正文采用字符二元组，英文技术词、命令、缩写和 slug 保持独立 token。
- Topic 的规范化 title 在同一 Domain 内唯一，允许不同 Domain 使用自然但相同的 title；Tag 的规范化 title 在全局唯一。Server 在发布时执行这些确定性冲突检查。
- 不在首个题库阶段引入 Elasticsearch、OpenSearch、向量数据库、embedding 同步链路或混合检索。它们只有在真实题库扩大后，且 BM25F 的召回缺口被可复现案例证明时才讨论。

### Generate、内容审核与分类审核

Generate 不把题目内容审核和课程分类混在同一轮。先让作者审核一份完整、已真实验证的题目；只有作者确认内容后，才对冻结的 candidate 做 Topic/Tag 分类。这样分类不会随着题目内容的多次生成、修复和作者修改反复失效，也不需要重新 Build 或 Verify。

```text
AuthoringSession -> PlanRevision n
  -> Generating -> Judging -> Building -> ArtifactPublishing -> Verifying
  -> NeedsAuthorReview
       | 内容反馈
       v
     Plan 草稿 --确认重新生成--> PlanRevision n+1 -> 新 GenerationWorkflow

       | 确认题目内容
       v
     Classifying -> NeedsClassificationReview
       | 分类反馈
       v
     Classifying

       | 确认分类并发布
       v
     ChallengePublishing -> Published

任一未发布 workflow
  -- 作者取消 --> Cancelled
  -- 被已确认的新 PlanRevision 替代 --> Superseded
  -- candidate 尚未验证时不可恢复的执行失败 --> Failed
```

- 一个确认的 `PlanRevision` 对应一个独立 `GenerationWorkflow` 与 workflow-scoped Generator session。自动修复只在同一 workflow 内复用该 session 并产生多个 CandidateRevision；作者改变题目内容则创建新的 PlanRevision 和 workflow。一个 AuthoringSession 同时最多有一个产生或等待作者决策的 workflow；旧 workflow 在新 PlanRevision 确认时直接持久化 `superseded_by_workflow_id` 并进入 `Superseded` 终态，不等待外部资源回收。
- 每个 CandidateRevision 固定关联输入 PlanRevision、可选 `parent_candidate_revision_id`、repair reason、Generator/Judge AgentRun、archive digest、构建产物和验证报告。所有 candidate source archive、失败原因和验证报告都保留为审计血缘；只清理可重建的 build output、验证环境和 staging 资源。
- `Judging` 只阻断有明确契约依据的问题：candidate archive 结构、Plan 与题目 metadata/checkpoint 的一致性、必要资产缺失、检查脚本与题意的可解释矛盾。教学价值、难度、文案和开放式改进建议属于作者审核，不能成为隐藏的自动修复理由。
- `NeedsAuthorReview` 只审核题目内容：作者看到完整、只读的 problem、solution、hints、runtime 脚本、检查脚本和真实验证报告。未通过真实验证的 candidate 只保留内部诊断，不对作者展示。
- 在 `NeedsAuthorReview` 中，作者的自然语言内容反馈先由 Authoring Agent 形成私有 Plan 草稿、`change_scope=content` 和面向作者的变更摘要；它不会立刻创建 workflow 或消耗构建资源。只有作者显式确认“重新生成”时，草稿才 materialize 为新的 PlanRevision，旧 workflow 才被 supersede，并创建新的完整 GenerationWorkflow。
- 作者确认题目内容后，`Classifying` Agent 读取这个冻结的 verified candidate，并通过 Roadmap Retrieval 查询当前 immutable Domain、Topic、Tag 定义。它是 Generate Worker 中的一个独立 role/state，不是新的 deployment、worklist 或 Roadmap Workflow。
- Classifying Agent 的初始 typed result 是 `proposed` 或 `unclassifiable`。`proposed` 对每题提出恰有一个已有 Topic 或新 Topic 候选，以及零到多个已有 Tag 或新 Tag 候选，并为每项绑定给出理由；`unclassifiable` 只返回原因与建议的内容调整，不能发布，也不能创建 Domain。每份 ClassificationProposal 固定 `candidate_revision_id` 与 `roadmap_revision`。模型输出、传输或 typed-result 校验失败只重试 `Classifying`，不回到 Build 或 Verify。
- 首次分类运行只拥有上述四个只读工具，并以 typed result 创建私有 ClassificationProposal。只有在 `NeedsClassificationReview`、Roadmap barrier 未活动且作者要求调整既有提案时，分类调整运行才额外获得 `set_topic` 与 `set_tags`。前四者只读当前固定 RoadmapRevision。`set_topic` 替换私有 ClassificationProposal 的唯一 Topic 及其理由，输入必须是已有稳定引用或包含 Domain、title、definition、scope、non_goals、challenge_guidance 的完整新 Topic 候选；`set_tags` 原子替换完整 Tag 列表及理由，每项是已有稳定引用或包含 title、description 的完整新 Tag 候选。Server 校验既有引用、Domain、必填字段和 Tag 去重；最终提案必须恰有一个 Topic。
- 这两个 setter 只修改当前 workflow 的**私有** ClassificationProposal，绝不创建、修改或删除全局 Topic/Tag，也不分配 source_ref、运行时 ID 或 Challenge ID。新定义可在私有 proposal 中反复修改；已发布 Topic/Tag 只能被选择和读取，不能被一道题的作者改写。确认发布后，Server 才一次性分配可读、不可变的 `source_ref`、默认 source filename 与运行时稳定 ID，并把新定义、最终绑定和 Challenge 原子写入下一份 RoadmapRevision；Agent 和作者不维护另一份 key 或平台身份。
- 在 `NeedsClassificationReview` 中，作者仍然只能通过自然语言让 Agent 调整。分类反馈由同一个 Classifying role 搜索/读取定义后调用 `set_topic` 或 `set_tags`；两个审核态都先产生 typed `change_scope`：`content` 一律形成私有 Plan 草稿并等待“确认重新生成”，`classification` 才保留同一个 verified candidate 并只回到 `Classifying`；无法明确归类的反馈继续对话澄清，不得隐式选择较便宜的分类路径。`unclassifiable` 结果继续停留在该审核态，作者只能修改内容并重新生成，或取消 workflow。分类审核的显式“确认发布”是唯一公开动作。

#### 分类审核界面

`NeedsClassificationReview` 本身就是持久化的作者审核请求，不另建通知对象或第二份待办数据。作者离开后重新打开会话，仍能看到同一 candidate、当前 ClassificationProposal 或 `unclassifiable` 结果和审核状态。

- 沿用作者工作台现有的左侧内容 Tab 与右侧对话。已验证题目的概览、检查点、文件、Diff 和验证 Tab 保持不变；`proposed` 结果额外显示相邻的“Topic”与“Tags”两个 Tab，并在首次进入 `NeedsClassificationReview` 时自动选中“Topic”。
- “Topic”Tab 将唯一 Topic 的 title、Domain、definition、scope、non_goals、challenge_guidance 和绑定理由渲染为 Markdown；“Tags”Tab 将每个 Tag 的 title、description 和使用理由分别渲染为 Markdown。两个 Tab 都清晰标记“已有”或“新建”，让作者一眼区分本题引用了什么与将要新增什么。
- Topic 与全部 Tag 仍作为同一份 ClassificationProposal 审核，不为每个 Tag 设置独立确认按钮。作者要调整 Topic、Tag 或新定义内容时在右侧对话说明，Agent 分别调用 `set_topic` 或 `set_tags` 更新私有 proposal；作者不能在面板中直接编辑字段。
- `unclassifiable` 没有 Topic/Tags 提案，改为显示单个“分类结果”Tab，以 Markdown 呈现无法分类的原因和建议的内容调整；不显示“确认分类并发布”，作者只能提出内容修改并确认重新生成，或取消 workflow。
- `NeedsAuthorReview` 的主操作是“确认题目内容，进入分类审核”，不再直接发布 Challenge；`NeedsClassificationReview` 的主操作才是“确认分类并发布”。Roadmap barrier 活动时，Topic/Tags 仍可只读浏览，但禁用分类调整与确认发布，不创建等待执行的分类请求；Barrier 结束后作者再发起分类调整或确认。Server 对竞态请求返回可重试冲突。
- 发布是分类审核的显式确认，也是 Challenge 唯一的公开提交点。Server 在公开提交前以最新 RoadmapRevision 重新校验 proposal 的既有引用和新 Topic/Tag 的规范化名称与 `source_ref`。无关的 revision 增量不阻塞发布：若所有引用仍存在且 immutable、所有新定义仍无冲突，Server 将 proposal rebase 到最新 revision；只有引用失效，或新 Topic/Tag 的 `source_ref`、规范化 title 发生确定性冲突时才回到 `Classifying`。若 Challenge 由其 title 派生的 `source_ref` 与既有 Challenge 冲突，则回到 `NeedsAuthorReview`，由内容调整形成新的 PlanRevision，分类 Agent 不得借此改写题目内容。Server 不判断不同名称定义是否语义重复，绝不静默合并语义冲突的提案。校验成功后，Server 才以 copy-on-write 方式生成包含新 Topic/Tag 定义和 Challenge 绑定的下一份 RoadmapRevision，并 materialize 正式 Challenge。Domain 从唯一 Topic 推导；不保存 `related_topics`，也不在 Challenge 上重复保存 Domain。
- 发布 intent 在首次“确认发布”时创建并固定 candidate artifact digest；确定性冲突校验成功后，它才一次性保留 Challenge ID 与 Challenge `source_ref`。Server 先将正式内容写入 staging，再以可重试、幂等的 promote 完成文件物化，最后用数据库事务一次标记 Topic、Tag、Challenge 和关系为公开；它不是虚假的跨数据库、文件系统和 artifact store 事务。崩溃恢复只复用同一 intent，继续或清理该次 promote，绝不分配第二个 Challenge 或替换已验证 artifact。
- 资源回收不是 workflow state。验证 Environment 在报告持久化后立即删除；Generator workspace 在对应 AgentRun 结束时释放；被修复、失败、取消、supersede 或已发布的 candidate 的 OCI/Incus staging artifact 和 build intermediate 都成为可回收资源。正式 runtime artifact、candidate source archive、失败原因和验证报告保留。资源以 workflow/candidate ID 标识归属，Controller 与 Generate Worker 对各自资源执行幂等 reaper；回收失败记录资源错误并持续重试，不改变 Challenge 的业务状态，也不阻塞新 workflow 或 `Published`。
- 每个活动 state 最多允许十次连续技术重试；同一 workflow 最多产生十个 CandidateRevision（含首次）。模型、传输或 typed-result 错误只消耗当前 state 的技术重试；Judge、构建或真实验证的 artifact failure 消耗 CandidateRevision 预算并回到 `Generating`。candidate 尚未验证时，任一预算或总 execution deadline 耗尽后直接进入 `Failed`，不向作者展示失败 candidate。verified candidate 之后的 `Classifying` 或 `ChallengePublishing` 若耗尽技术重试，则保留 candidate、ClassificationProposal 与 publication intent，释放 worker 并回到 `NeedsClassificationReview`，以 `classification_unavailable` 或 `publication_unavailable` 说明可重试的基础设施状态；重试只恢复对应阶段，不重新 Build 或 Verify。
- `NeedsAuthorReview` 与 `NeedsClassificationReview` 都暂停 execution deadline。新的 PlanRevision 创建新 workflow 并获得新的完整执行预算；从任一审核态恢复 `Classifying` 或 `ChallengePublishing` 时也开始新的执行窗口，作者等待时间永不消耗 Worker deadline。
- `Failed` 与 `Cancelled` 只终止对应 GenerationWorkflow，不终止 AuthoringSession。Server 保留最后一份 Plan 与结构化失败摘要；作者可继续对话、形成新的 Plan 草稿并确认新的 PlanRevision。失败 candidate 始终只作为内部诊断，不进入作者审核界面。
- 本流程只创建新 Challenge。已发布 Challenge 的内容编辑、重新验证、重新分类和版本升级不隐式复用 AuthoringSession，留作后续独立设计。
- Topic 是唯一课程归属，Tag 是横向检索面，二者不要求名称互斥。`systemd` 可以作为 Tag，但仅在题目的诊断或修复实质依赖 systemd 时使用；`linux`、`kubernetes` 同样表达学习场景而非底层镜像或 Provider 实现。没有筛选价值的偶然技术细节不创建 Tag。
- Roadmap Workflow 只维护关系边，不审查、不改写也不产出分类意见。Challenge 的内容或最终分类发布变更只在同一事务中标记 Roadmap pending，等待下一次空闲窗口中的增量维护处理。

### Roadmap Workflow

RoadmapWorkflow 是 Server 内部拥有的、持久化的异步增量 workflow，不单独部署 Roadmap Worker，也不引入通用 executor、slot 或第二套后台调度。PostgreSQL 持久化 workflow、其固定 entry 快照、TopicTask/ChallengeTask、每个 Agent 的调用次数、结果和 lease；Server 创建或接管 workflow 后直接并发运行 task。Server 重启或多副本竞争时，workflow/task lease 只允许一个实例恢复同一项工作。

```text
RoadmapWorkflow: Queued -> Running -> Publishing -> Completed | Failed
RoadmapTask:     Pending -> Running -> Accepted | Failed
```

每个已发布 Challenge 都有一个 Roadmap entry。entry 至少包含 Challenge 关系是否已处理；若该 Challenge 首次引入了一个 Topic，还包含该 Topic 关系是否已处理。entry 只有在它所需的 task 均 `Accepted` 后才不再 pending。TopicTask 失败时，其来源 Challenge entry 仍保持 pending；下一次只重建这个未完成的 TopicTask，不重复已成功的 ChallengeTask。

Roadmap 维护只在题库没有任何 Generation 执行阶段运行的空闲窗口启动，不等待 drain。这里的执行阶段包括 `Generating`、`Judging`、`Building`、`ArtifactPublishing`、`Verifying`、`Classifying` 和 `ChallengePublishing`；`NeedsAuthorReview` 与 `NeedsClassificationReview` 是等待状态，不阻塞 Roadmap 启动。每累计 20 道**新发布** Challenge，Server 只创建一个持久化的 `roadmap_requested` 请求；20 只是自动触发阈值，不切分 pending entry，也不是一次 workflow 的任务上限。请求已存在时，后续新题只继续进入 pending，不额外排队。仅供调试的内部手工触发可在任意 pending entry 存在时创建同一种请求；它不是前端功能、不引入用户角色，也不作为常规业务流程。两种触发都必须先确认没有执行中的 GenerationWorkflow，才允许创建唯一的 RoadmapWorkflow。

```text
Challenge 发布 -> 创建或更新其 Roadmap entry
  -> 累计 20 道新发布 Challenge 后标记 roadmap_requested
  -> 无活动 GenerationWorkflow 时，原子创建 Queued RoadmapWorkflow 并消费请求
  -> 固定此刻全部 pending entry
  -> 并发 TopicTask 与 ChallengeTask -> 合并 -> Publishing -> Completed
```

- 同一时刻最多存在一个非终态 RoadmapWorkflow。Server 在同一数据库事务中检查没有执行中的 GenerationWorkflow，并创建 `Queued` RoadmapWorkflow；这个已持久化的 workflow 就是唯一的执行门控，不另设 `Draining` 或 barrier state。RoadmapWorkflow 创建后，任何 Generation 执行阶段都不得启动：新的 PlanRevision 确认、从内容审核进入或重跑 `Classifying`、`ChallengePublishing` 以及 Catalog Release commit 都必须等待其终止；Authoring 对话和内容 Plan 草稿仍可继续，但 `NeedsClassificationReview` 中的 Topic/Tag 调整及其确认必须保持只读，不创建等待执行的分类请求。审核确认请求在此期间不创建新的执行 workflow，界面可禁用确认，竞态请求由 Server 以可重试冲突拒绝。
- Generation 执行态仅指 `Generating`、`Judging`、`Building`、`ArtifactPublishing`、`Verifying`、`Classifying` 或 `ChallengePublishing`。`NeedsAuthorReview`、`NeedsClassificationReview`、`Failed`、`Cancelled`、`Superseded` 与 `Published` 均不阻塞 Roadmap 启动。每次 workflow 进入或离开执行态、每次 Challenge 发布以及每次内部手工请求后，Server 都尝试满足上述原子启动条件。
- 一个 workflow 固定启动时的**全部** pending entry；它们可以远多于 20。快照建立后才发布的 Challenge 留在 pending，进入下一次 workflow，绝不改变正在执行的 task 输入。每个 entry 只为尚未完成的部分创建 task：TopicTask 规划该 entry 首次引入的 Topic 与其他已发布 Topic 的关系；ChallengeTask 规划该 Challenge 与其他已发布 Challenge 的关系。Tag 不是路线图节点，不创建 TagTask。
- TopicTask 与 ChallengeTask 没有数据依赖，全部并发执行。每个 task 的输入只有一个 subject；Planner 通过 Roadmap Retrieval 在固定 revision 的全部其他已发布实体中搜索候选并按需读取详情，不遍历快照，也不把快照内实体作为特殊输入。ChallengeTask 可以读取 Challenge 的既有唯一 Topic 作为描述元数据，但不读取 TopicTask 结果，也不建立 Topic 边。每个 task 内部始终串行：Roadmap Planner 先提出 typed ChangeSet，Curriculum Reviewer 与 SRE Reviewer 随后并发审查；任一 reject 把两份意见交回 Planner 进入下一轮。Planner 每次产生新的 ChangeSet 后，两位 Reviewer 都必须重新审查该版本，不能复用上一轮的通过结论。
- Roadmap Planner、Curriculum Reviewer 与 SRE Reviewer 各自在一个 task 内最多调用五次。模型、传输、工具或 typed-result 错误只消耗对应 Agent 的调用次数，并重试同一阶段；reviewer reject 触发下一次 Planner 调用，也消耗 Planner 的次数。没有独立的 task retry 预算。任一必要 Agent 耗尽调用次数时，task 进入 `Failed`，来源 entry 保持 pending，等待下一次自动或内部手工 RoadmapWorkflow；它不阻塞其他 task。
- 所有 task 达到 `Accepted` 或 `Failed` 后，Server 分别合并 Topic 图和 Challenge 图的 Accepted ChangeSet。`related` 按无向规范化键去重，并优先于同一对实体的任何 `precedes`：只要现有边或本次 ChangeSet 提出 `related`，就保留一条 `related`，删除或忽略该对的 `precedes` 并记录原因。同一对实体只出现反向 `precedes` 时，也以一条 `related` 替换两条有向边。若一条方向已存在于当前 revision、另一条由当前快照提出，且该对至少一端属于当前快照 entry，也同样按上述规则处理，不改写两端均为历史实体的边。
- 合并剩余 `precedes` 时，Server 按 entry 在固定快照中的稳定顺序和 edge 的稳定 ID 处理，绝不依赖并发 task 的完成时序。每次插入前检查 target 是否已可达 source；会形成环的当前候选边直接忽略，已合并的边保持不变。每条合并、转换或忽略的边都记录确定性原因；这些都是 `Accepted` task 的正常结果，不使 task 失败。
- 合并后的有效增量边以 copy-on-write 方式发布为一份新的 RoadmapRevision。成功 entry 标记为已处理；含 Failed task 的 entry 自然保持 pending，不创建额外的失败队列或重试项。task failure 不会使整个 workflow 失败，也不会立即创建下一次请求；下次自动运行必须再有 20 道新发布 Challenge，内部手工触发可提前重试。RoadmapWorkflow 的 `Failed` 只表示其自身的持久化或 revision 发布在基础设施重试与 deadline 内仍无法完成；Agent task failure、进程重启和 lease 过期都不直接导致 workflow Failed。进入该终态后保留全部 pending entry 并释放执行门控。
- RoadmapWorkflow 创建快照/门控、Roadmap publish 与 Catalog Release commit 在同一 RoadmapRevision 写锁上串行。若 Catalog 已进入 `Committing`，它先完成安装，随后 Server 再重新判断是否可创建 RoadmapWorkflow；一旦 RoadmapWorkflow 已 `Queued`，新的 Catalog commit 必须等待其终止。Catalog source 自带完整关系图，commit 后将其安装的 entry 标为已处理，不创建 Roadmap task 或 `roadmap_requested`。

- **Topic 图**维护课程级路线。例如“网络地址与路由”在“DNS 与名称解析”之前。
- **Challenge 图**维护有明确教学意义的局部路线。例如 SSH 密钥认证题在 ProxyJump 题之前；允许跨 Topic 连边。
- 初期关系只有两类：有向 `precedes`，表示 source 推荐先于 target 学习；无向 `related`，表示有直接的横向学习关联。`precedes` 图必须无环，`related` 以唯一规范化边存储。
- 所有关系只用于目录、学习路径和下一题推荐，绝不锁定题目、阻止用户开始题目，或规定 checkpoint 完成顺序。
- 题目详情将两张图分开展示：先经其唯一 Topic 找到 Topic 图的一跳邻居，并展示“推荐先学”“建议后续”和“相关主题”；Challenge 图另行展示直接的“推荐先学题目”“建议后续题目”和“相关题目”。两者都不得默认做传递闭包，否则远端节点会被误显示为直接相关。
- Topic 边不会自动展开成其题目之间的笛卡尔积。Challenge 边必须由 Roadmap Workflow 单独提出并说明理由；没有合理边是合法结果。
- Challenge 内容、唯一 Topic 或 Tags 发生发布变更时，Server 在同一事务中标记 Roadmap pending。每个新增 Topic/Challenge 由自己的 task 规划增量关系；Roadmap Planner 不能重算或改写历史节点之间的边。Curriculum Reviewer 与 SRE Reviewer 只审查当前 task 的题目递进、技术前置、关系理由和局部图一致性。
- 当前阶段不支持已发布 Challenge、Topic 或 Tag 的编辑。未来若引入这些 revision，它们同样只标记 Roadmap pending；历史关系图的全量重审属于后续显式设计，不混入日常增量 workflow。

Roadmap 是全局课程内容的不可变投影；用户完成记录属于运行时数据。Server 基于当前 roadmap 和用户进度计算推荐的下一题，不把用户特定的 `next` 写回 catalog。

### 内容模型

```text
Domain -> Topic -> Challenge
                  -> independent Checkpoints
```

- **Domain** 是稳定的学习与内容边界，有明确受众、范围、非范围、运行时适配性和完成标准。例如“Linux 系统与网络运维”是一个领域。
- **Topic** 是读者能自然理解的学习主题，例如“systemd 与服务管理”“DNS 与名称解析”或“SSH 与远程运维”。它用于组织内容、浏览题库和给出推荐学习路径，不是抽象的原子能力模型。
- **Challenge** 是一个可运行的故障场景。每题唯一归属一个 Topic；复合题的跨主题学习关系由 Roadmap 的 Topic 图和 Challenge 图表达，而不是给题目附加多个 Topic。
- **Tag** 只表达横向场景属性，例如 `linux`、`kubernetes`、`systemd`、`multi-node`、`incident`、`least-privilege` 和 `configuration-drift`。它可以与 Topic 使用相同技术词汇，但必须提供跨 Topic 的实际筛选价值，不能只是偶然出现的命令或底层实现细节。
- **Checkpoint** 只验证可观察的最终状态，彼此独立；它不代表 Topic，也不规定学习者的操作顺序。

Topic 不要求覆盖固定数量的题，也不要求每个 Topic 都拆成多个细小的“能力”。例如 DNS 可以自然包含 hosts、resolver、搜索域和权威记录相关的不同场景；若某个主题暂时只有一题有价值的场景，就保留一题或延后发布，不能为了凑数量制造变体题。

Topic 间的推荐顺序由 Roadmap Workflow 的有向 `precedes` 边维护，例如“网络地址与路由”在“DNS 与名称解析”之前。它们只用于导览和学习路径，不锁定题目。引用始终同时保留稳定 `id` 和可读 `title`。

### 迁移清单

P0 已无兼容地完成遗留 `Skill`、`Tag`、challenge mapping 和 `Skill requires` 模型的迁移。`RoadmapRevision` 是唯一的全局课程数据版本：Domain、Topic、Tag、Challenge 绑定和两张关系图始终由同一个 revision 一起读取、校验和发布，不能再并存另一份分类读模型。

- [x] 删除遗留 `Skill` 模型、`skills/` 布局、`entry_skills`、`outcomes`、`requires` 及相关 API/前端术语，不保留兼容读取路径。
- [x] 定义 immutable `RoadmapRevision` 的完整内容：Domain、Topic、Tag、`Challenge -> Topic`、`Challenge -> Tag`、`topic_edges` 与 `challenge_edges`。它是分类、目录、筛选和学习路径唯一的读模型；发布 Challenge 分类或路线关系时，均以 copy-on-write 生成下一份 revision。
- [x] Catalog Release 安装后，数据库中的 RoadmapRevision 是运行时唯一事实来源。`catalog/` 与 portable release 只作为 immutable import/export source，Server 不回写 Git 工作区，也不做双向同步。
- [x] portable source 使用可读、不可变的 `source_ref` 和 `title`，不携带平台 Challenge ID、镜像或发布时间。Domain `source_ref` 全局唯一；Topic 使用 `domain/topic`；Tag 全局唯一；Challenge 使用 `domain/topic/challenge`。对于作者确认发布的新 Topic、Tag 和 Challenge，Server 只在首次发布时从规范化英文 title 分配 `source_ref`：Topic 为 `<domain-source-ref>/<title>`，Tag 为 `<title>`，Challenge 为 `<topic-source-ref>/<title>`。新 Topic/Tag 的规范化冲突拒绝发布并回到分类调整；Challenge 的规范化冲突回到内容审核并形成新的 PlanRevision；两者都绝不追加随机后缀。成功后永久保存，title 或 source filename 改名也不得重算。source filename 可默认采用 source_ref 的末段，但不承担身份。安装时 Server 将 source_ref 解析为运行时稳定 ID；运行时绑定与边同时保留 ID 和 title 快照，保证机器可引用、人可审查。
- [x] 增加 immutable `Domain` 定义：`source_ref`、`title`、`definition`、`scope`、`non_goals` 与可读 source filename。Domain 不直接映射 Challenge，也不参与关系边。
- [x] 增加 immutable `Topic` 定义：`source_ref`、`title`、`domain: {source_ref, title}`、`definition`、`scope`、`non_goals`、`challenge_guidance` 与可读 source filename。Topic title 在同一 Domain 内规范化唯一；`challenge_guidance` 用自然语言说明什么样的题唯一归属该 Topic，避免分类只靠名称猜测。
- [x] 增加 immutable `Tag` 定义：`source_ref`、`title`、`description` 与可读 source filename。Tag title 在全局规范化唯一；没有跨 Topic 筛选价值的偶然命令或底层 Provider 细节不创建 Tag。
- [x] 将 GenerationWorkflow 收敛为业务状态：`Generating`、`Judging`、`Building`、`ArtifactPublishing`、`Verifying`、`NeedsAuthorReview`、`Classifying`、`NeedsClassificationReview`、`ChallengePublishing`、`Published`、`Failed`、`Cancelled`、`Superseded`。删除 `CleaningUp`、`CleanupIntent` 及所有把资源回收作为 workflow 阶段的协议/API；supersede、取消和 candidate 尚未验证的不可恢复失败直接进入各自终态，但不终止 AuthoringSession。
- [x] 为 CandidateRevision 持久化输入 PlanRevision、parent candidate、repair reason、AgentRun、archive digest、构建/验证结果和外部资源记录。每条资源记录至少有类型、外部身份、归属 workflow/candidate、创建时间、回收状态、最近错误和下次重试时间；保留 source archive、失败原因和报告。Controller 与 Generate Worker 基于这些记录执行幂等 reaper，回收 verification Environment、workspace、candidate OCI/Incus staging artifact 和 build intermediate，而不改变 workflow state。
- [x] 为 verified CandidateRevision 增加独立的 ClassificationProposal：结果为 `proposed` 或 `unclassifiable`。`proposed` 恰有一个 Topic 与零到多个 Tags，既有定义按稳定引用选择，新定义只保留作者可读的候选字段；`unclassifiable` 只带原因和内容调整建议，不能发布。提案固定 candidate 与 `roadmap_revision`，不改写已真实验证的 runtime archive；发布后才成为 Challenge 的最终分类事实。删除 `primary_topic`、`related_topics` 和所有由 Roadmap Workflow 生成的分类字段，Challenge 不重复保存 Domain。
- [x] 在 GenerationWorkflow 中增加 `Classifying -> NeedsClassificationReview`：内容审核确认后才由独立分类 Agent 读取冻结 candidate 和当前 RoadmapRevision。两个审核态均通过 typed `change_scope` 路由自然语言反馈：内容变更先形成私有 Plan 草稿并经作者确认后创建新 workflow，分类变更才只重跑 `Classifying`。分类变更不重新 Build 或 Verify。
- [x] 定义阶段敏感的失败恢复：candidate 尚未验证时，技术/资源预算耗尽进入 `Failed`；verified candidate 之后，`Classifying` 或 `ChallengePublishing` 的技术耗尽返回 `NeedsClassificationReview` 并保留 candidate、proposal 与 publication intent，以可重试的 `classification_unavailable` 或 `publication_unavailable` 表达，不重新 Build 或 Verify。
- [x] 所有内容、分类和发布确认 API 均使用乐观并发：请求绑定 `workflow_id`、Plan draft/revision、candidate revision 或 publication intent 的 expected revision，并带 idempotency key。旧标签页与重复点击不得创建第二个 workflow、Challenge 或 promote。
- [x] Server 只判断 ID/source_ref 引用、规范化 title 和关系图的确定性约束；无向 `related` 去重并优先于同对的 `precedes`，反向 `precedes` 规范化为 `related`，按稳定顺序跳过会成环的候选 `precedes`。这些归并结果必须留下审计原因；“语义是否重复”仍由 Agent 与作者审核，不能伪装为 Server 自动判断。
- [x] 发布时允许无关 RoadmapRevision 增量的确定性 rebase，只在已引用定义失效，或新 Topic/Tag 的 `source_ref`、规范化 title 冲突时重跑 `Classifying`。Challenge 由 title 派生的 `source_ref` 冲突则回到 `NeedsAuthorReview`，不得由分类调整掩盖内容冲突。Server 不判断语义重复。首次确认发布分配唯一 publication intent，staging/promote、数据库可见性提交和崩溃恢复都复用该 intent。
- [x] 让两个审核态暂停 Worker deadline；新 PlanRevision 和每次由审核态恢复的 `Classifying`/`ChallengePublishing` 都开始新的执行窗口。每个活动 state 最多十次连续技术重试，每个 workflow 最多十个 CandidateRevision（含首次）。
- [x] 实现 Server-owned、只读的 Roadmap Retrieval：按 immutable `roadmap_revision` 提供 Topic/Tag/Challenge 的 typed search/read 工具，以精确 ID/title 匹配加 BM25F 作为初始检索策略。分类 Agent 只经此接口读取定义，并通过 typed result 提出既有引用或新定义候选；Roadmap Planner 以单个 subject 为中心，在固定 revision 的全部其他已发布 Topic 或 Challenge 中按需搜索和读取关系候选，不提供快照遍历工具。作者对内容和分类的两次显式确认是这些候选进入公开 Roadmap 的唯一入口。
- [x] 增加 immutable `topic_edges` 和 `challenge_edges`：`precedes` 是有向无环边，`related` 是无向规范化边；每条边保存稳定 source/target 引用、title 快照和面向审查的理由。
- [x] 将现有 Taxonomy Workflow 迁移为 Server-owned 的增量 `RoadmapWorkflow`：不保留独立 deployment、通用 executor、slot 或全量关系规划。Server 在 PostgreSQL 中持久化 `roadmap_requested`、启动时全部 pending entry 的固定快照、TopicTask/ChallengeTask、每个 Agent 的调用次数、结果和 lease，并直接异步并发执行 task；Server 重启或多副本竞争时通过 lease 接管。每累计 20 道新发布 Challenge 只创建一个自动请求；20 只是触发阈值，请求存在时新题继续累积，workflow 启动时固定全部 pending entry 并消费请求。仅供调试的内部手工触发可在任意 pending entry 存在时创建同一种请求，不暴露给普通用户或 Catalog UI。两种触发均只在没有执行中的 GenerationWorkflow 时，才以同一事务创建唯一的 `Queued` RoadmapWorkflow；Roadmap 运行期间所有 Generation 执行阶段和 Catalog Release commit 都必须等待。每个 entry 只重建尚未完成的 TopicTask/ChallengeTask；两类 task 全部并发，每个 task 内 Planner 串行驱动、两位 Reviewer 并发审查，三种 Agent 各自最多五次调用；每次 Planner 修订后两位 Reviewer 都重新审查。task 只有 `Accepted` 或 `Failed`：失败 entry 自然保留为 pending，在下一次自动或调试手工 workflow 的完整快照中重试，不立即循环，也不产生额外重试项。Server 将 `related` 优先于同对的 `precedes`，将反向 `precedes` 合并为 `related`，再按稳定顺序忽略成环候选边，最后一次发布有效增量；这些合并结果不导致 task 失败。Catalog 安装以同一 RoadmapRevision 写锁建立已处理基线。它不产出或修改分类、Domain、Topic 或 Tag。
- [x] 将 portable Roadmap source、运行时 RoadmapRevision、内容校验、Catalog 安装、API/前端筛选和测试 fixture 全部升级为同一契约。当前没有正式题库，因此不保留旧 schema。
- [x] 增加只读、仅调试的 RoadmapRevision `.tar.gz` 导出接口：使用标准库流式生成完整 portable Catalog Release，固定归档顺序和元数据，不写 Git、不创建临时文件、不导出运行时资源，也不提供 UI 或子集导出。
- [x] 将 source 布局改为可读的领域分组，并让 reader/writer 保留相对 source 路径：

```text
roadmap/
  domains/
    linux-system-network-operations.yaml
    kubernetes-workload-service-operations.yaml
    sre-reliability-operations.yaml
  topics/
    linux-system-network-operations/
      systemd-and-services.yaml
      dns-and-name-resolution.yaml
    kubernetes-workload-service-operations/
      workloads-and-rollouts.yaml
  tags/
  challenge-bindings/
  topic-edges.yaml
  challenge-edges.yaml
```

- [x] Catalog 的领域和主题筛选只从 Challenge 的唯一 Topic 推导；跨 Topic 发现由 Topic 图和 Challenge 图提供，不使用多 Topic mapping。
- [x] 初期禁止 Generate Agent、Classifying Agent 或 Roadmap Workflow 创建或合并 Domain。Domain 是人工维护的课程边界；Classifying Agent 只能提出 Topic/Tag 候选并等待作者确认。全局领域调整属于后续 maintenance workflow。

### Catalog Release 契约

P0 只定义可打包、可安装的 `releases/<name>/` portable root；实际的内容工作区和 Domain/Topic 导读属于 P1：

```text
catalog/
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

- challenge 继续遵循 [`题目内容格式`](docs/reference/challenge-format.md)：`problem.md`、`solution.md`、`hints/`、`generate.sh`、`answer.sh` 和 `checks.sh`。目录使用可读 source 名称；发布后才获得与题意无关的 opaque challenge ID。
- Catalog Release 是基础题库的初始化输入，不进入 Generate、Judge、Classifying、作者审核或 Roadmap Workflow。它不是用户角色、用户提交入口、Agent 对话或 function-tool 场景；每个 Challenge 的唯一 Topic、Tags 和两张关系图都必须已经写在 portable Roadmap source 中，Server 只做确定性契约校验、构建、验证和安装。其 `Committing` 阶段与 Roadmap publish 竞争同一份 RoadmapRevision 写锁；成功安装的 Challenge entry 直接标记为已处理，不创建 Roadmap task 或自动请求。
- Catalog 不复用 GenerationWorkflow，但必须是可恢复的长期确定性任务。`CatalogRelease` 持久化 immutable source digest、release state、entry state/attempt、构建产物、验证报告、外部资源身份和一次性的 commit intent；Server 重启或 worker lease 过期后只恢复未完成的 release/entry 阶段，并以同一外部资源身份 create-or-get，不能重复分配 Challenge 或重新验证已通过 entry。
- 采用最小的 release 状态：`Pending -> Installing -> Committing -> Ready | Failed`。`Installing` 内每个 CatalogEntry 独立推进 `Pending -> Building -> ArtifactPublishing -> Verifying -> ReadyToCommit | Failed`；release 只在全部 entry 就绪后进入 `Committing`。资源回收由幂等 reaper 完成，不引入 `CleaningUp` 业务状态，也不阻塞 `Ready` 或 `Failed`。
- 原子安装只有在该 source digest 的结构校验、全部 entry 的构建、artifact publish 和真实验证均成功时才执行。确定性的 source/build/verify failure 直接使整份 release `Failed`，不发布部分题库，也不创建 Generator run 或自动修复；基础设施失败只在 release deadline 内重试当前阶段。source 一旦变化，旧 digest 的报告不能复用，必须针对新 digest 创建新的 CatalogRelease。
- P0 的 Catalog Release 是 immutable、追加式内容输入：同一 digest 的安装幂等；后续 release 只能添加未出现过的 source_ref，不能原地修改或删除已发布内容。内容替换、删除和 release upgrade 留作后续独立设计。
- P0 提供仅供调试的 `GET /internal/debug/roadmap-revisions/{revision_id}/export`。它按指定 immutable RoadmapRevision 直接流式返回 `breakfix-roadmap-r{revision_id}.tar.gz`，不创建临时文件、不写回 Git 工作区，也不进入 Catalog UI。归档根目录是一个完整 portable Catalog Release，包含 `release.yaml`、全部 `roadmap/` 定义、绑定和两张关系图，以及该 revision 中全部 Challenge 的 source；不包含数据库/运行时 ID、镜像、构建中间产物、验证报告或宿主机路径。
- 导出只支持整个 revision，不做单题、Domain 或任意子集导出，避免输出缺失 Topic/Tag/关系依赖的断裂 release。Server 使用 Go 标准库 `archive/tar` 与 `compress/gzip`；写入路径按字典序，文件 mode、owner、tar mtime 和 gzip mtime 固定，保证同一 revision 重复导出得到字节稳定的归档，并保留题目脚本的执行位。
- `test/fixtures/catalog-release/` 始终只是安装链路 fixture，不能作为正式基础题库内容。

### P0 提交计划

P0 按下面顺序实施，**每完成一个部分就立即提交**，不能把多个部分累积在同一个工作区。每项都定义了本次提交的交付目标和必须成立的验证结果；接口契约变化必须在所属提交内同步更新 OpenAPI、生成物与前端调用。每个提交都必须保持可编译、可测试，不保留旧 schema、兼容读取路径或迁移拒绝测试。Prompt 只通过 Agent 协议和应用行为验证，不写 prompt 文本断言。

1. **`refactor(roadmap): replace taxonomy contracts`**
   - **目标：** 用 `Domain`、`Topic`、`Tag`、immutable `RoadmapRevision`、Challenge 的唯一 Topic/Tag 绑定及两张关系图完整替换 Taxonomy；建立可读的 portable Roadmap source 布局和运行时读模型，同时删除 `Skill`、旧 mapping、`outcomes`、`requires` 及其 API、前端和部署术语。
   - **验证：** 一个最小 portable Roadmap source 能被解析为确定性的 revision，Domain/Topic/Tag/source_ref、唯一 Topic 绑定和 `related`/无环 `precedes` 均在同一契约中成立；Catalog 的读取投影只依赖该 revision；旧 Taxonomy runtime、worker、部署、内部接口和 OpenAPI 术语均已移除。此提交之后不保留双读、双写或兼容转换路径。
2. **`refactor(catalog): install portable releases into roadmap`**
   - **目标：** 将 Catalog Release 重建为可恢复的、追加式 portable 输入；Server 从配置指定的 immutable release reference 幂等初始化，不提供管理员安装 API。每个 entry 独立经历构建、artifact publish 和真实验证，全部就绪后与 `RoadmapRevision` 在同一写锁下原子安装，不创建 GenerationWorkflow 或 Roadmap task。
   - **验证：** 最小 release 只有在全部 entry 已真实验证后才公开其 Challenge 与完整 RoadmapRevision；重复启动或重复安装同一 digest 不重复分配 Challenge 或重新执行已完成阶段；中断后能用同一 entry/commit identity 恢复，任一确定性 entry 失败不会留下部分公开题库；安装成功的 entry 已建立 Roadmap 已处理基线，不会触发维护请求。
3. **`feat(generation): persist classification proposal lifecycle`**
   - **目标：** 将 GenerationWorkflow 收敛为业务状态，并为 CandidateRevision 持久化 Plan、parent candidate、AgentRun、archive、构建/验证结果和外部资源记录；资源回收改为幂等 reaper，不再是 workflow 阶段。为 verified Candidate 增加私有 `ClassificationProposal`，把内容审核后的分类、分类审核和公开提交纳入 GenerationWorkflow；实现 `Classifying`、`NeedsClassificationReview`、`ChallengePublishing`、publication intent、乐观并发和新 Topic/Tag/Challenge 的确定性 `source_ref` 分配。
   - **验证：** 内容确认后冻结同一 verified candidate 并进入分类链路，分类反馈不会重新 Build 或 Verify；作者的内容反馈仍只形成私有 Plan 草稿；审核态暂停 deadline，分类或发布技术耗尽回到分类审核并保留 candidate/proposal/intent；重复确认或旧标签页请求不会创建第二个 workflow、Challenge 或 promote；无关 revision 增量可确定性 rebase，而引用或规范化名称冲突会回到正确的内容或分类审核路径；分类发布成功时才以 copy-on-write 让定义、绑定和 Challenge 同时可见，取消、supersede 与失败后的资源由记录驱动回收且不改变业务终态。
4. **`feat(generation): add retrieval-backed classification role`**
   - **目标：** 实现独立的 Classifying role：初始运行只可对固定 `RoadmapRevision` 使用 Topic/Tag 的 typed `search`/`read`，产生 `proposed` 或 `unclassifiable`；分类调整运行只额外拥有私有 `set_topic`、`set_tags`，并按 typed `change_scope` 路由内容与分类反馈。
   - **验证：** 同一 Agent run 始终读取固定 revision，既有引用只能来自该 revision，新定义只停留在私有 proposal；setter 不会直接写入全局 Roadmap；`unclassifiable` 无法发布；工具、模型或 typed-result 失败只重试 Classifying；Roadmap barrier 活动时分类调整与确认发布均不会启动执行。
5. **`feat(authoring): add classification review workspace`**
   - **目标：** 将 `NeedsClassificationReview` 做成作者工作台的持久化审核界面：Topic 与 Tags 独立 Tab，展示 Markdown 定义、绑定理由和“已有/新建”状态；支持 `unclassifiable` 结果、对话式调整、确认发布及 barrier 只读状态。
   - **验证：** 作者重新打开会话仍能看到同一 candidate 和 proposal；Topic/Tags 作为一份 proposal 一次确认，不出现直接编辑全局定义或逐 Tag 提交；`unclassifiable` 只允许内容调整或取消；前端操作与 API 的 expected revision/idempotency 语义一致，Roadmap barrier 时界面与服务端均拒绝启动分类或发布。
6. **`refactor(roadmap): make roadmap maintenance server-owned`**
   - **目标：** 在已完成的 Roadmap 基础契约上，由 Server 持久化并执行增量 `RoadmapWorkflow`、pending entry、20 题自动请求、调试手工触发、task lease 与三 Agent 委员会。Planner 获得固定 revision 的 Topic/Challenge typed `search`/`read`，每个 task 串行 Planner、并行双 Reviewer，每个 Agent 最多五次调用；不重新引入 worker deployment、通用 executor 或第二套后台队列。
   - **验证：** 空闲窗口中 workflow 固定全部 pending entry 的快照，TopicTask 和 ChallengeTask 并发且只处理自己的 subject，Planner 无法遍历或写入全局快照；Server 重启或多副本竞争能通过 lease 接管；Accepted task 的边按稳定顺序合并，`related` 优先、反向 `precedes` 转为 `related`、成环候选被审计地忽略；Failed task 保持 pending 且不阻塞其他 task 或立即自旋重试；workflow 运行期间 Generation 执行阶段和 Catalog commit 被正确门控。
7. **`feat(catalog): add catalog navigation and debug export`**
   - **目标：** 让 Catalog 按 Challenge 的唯一 Topic 推导 Domain/Topic 筛选、按 Tag 横向筛选，并分别展示 Topic 图和 Challenge 图的一跳邻居；增加只读、仅调试的完整 RoadmapRevision `.tar.gz` 导出，不将该入口放入普通 Catalog UI，并同步正式文档契约。
   - **验证：** Catalog 不再依赖多 Topic mapping 或 Taxonomy 状态，筛选与详情均来自当前 immutable revision；两张图不会混合或做传递闭包；同一 revision 的重复导出字节稳定，导出内容可作为完整 portable Catalog Release 再次读取，且不包含运行时 ID、镜像、验证报告、临时文件或宿主机路径。

P0 完成标准是：旧 taxonomy 代码和部署已移除；一个最小 Catalog Release 能安装为 RoadmapRevision；一个 verified Candidate 能完成分类审核和发布；Roadmap 能在 Server 内增量维护两张关系图；Catalog 能筛选、展示邻居并导出可再次安装的 portable release。

## P1：题库内容建设

以下内容只服务于题目设计、讨论、审查和 portable release 制作，不属于 P0 迁移，也不直接显示在用户 UI 中。

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
