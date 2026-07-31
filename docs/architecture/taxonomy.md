# Taxonomy 与 Catalog 发布

Taxonomy 将已验证 challenge 关联到可复用的 Skill、浏览 Tag 和学习前置关系。它不参与题目生成、构建、产物发布或验证；`Build -> ArtifactPublish -> Verify` 证明题目可运行，taxonomy 证明题目应如何被浏览和学习。只有两者均完成，题目才进入公开 Catalog。

## 独立工作流

```text
Authoring -> Generator/Judge -> CandidateRevision -> Build -> ArtifactPublish -> Verify -> 作者发布 -> 已发布 challenge
                                                       |
                                                       v
                                    Mapping WorkItem -> Mapper + 两名 reviewer
                                                       |
                                                       v
                                            Publisher -> immutable taxonomy revision
                                                       |
                                                       v
                                               public Catalog
```

Challenge workflow 不创建、修改或依赖 Skill、Tag 与 mapping。Taxonomy workflow 读取最终已发布 artifact 的 `problem.md`、`solution.md`、检查点、答案和 revision，独立给出分类。challenge manifest 不再保存结构化 Tag、Skill 或跨题关系。

## 内容权威与身份

发布 challenge 的权威来源是 `data_dir/challenges/<source_slug>/`；taxonomy 的权威来源是 Server data PVC 中 `data_dir/taxonomy/current` 指向的不可变 revision。PostgreSQL 只保存 WorkItem、候选、审查结论、重试和租约，不能成为 taxonomy 的第二份内容副本。

```text
data/
  challenges/<source_slug>/
  taxonomy/
    revisions/<sha256>/
      skills/*.yaml
      tags/*.yaml
      mappings/challenges/*.yaml
      mappings/skills/*.yaml
    current -> revisions/<sha256>
```

Challenge、Skill 和 Tag 一律使用 opaque ID。challenge 目录名、Skill/Tag/mapping YAML 文件名都是可读 source slug，不能作为 API、关系解析、数据库记录或用户学习记录的键。关系中同时保存 ID 与 title：ID 是唯一关系键，title 是可读显示快照，发布校验要求二者与当前对象精确一致。

`Skill` 定义可复用能力，并含有自然语言 mapping guidance；`Tag` 只服务稳定的 Catalog 浏览维度，也含有 include/exclude guidance。二者的定义文件不保存关联对象。`mappings/challenges/` 表示 challenge 的 Tag、entry skill 和 outcome；`mappings/skills/` 表示唯一允许跨内容的 `Skill.requires` DAG。

## Mapping 委员会

发布完成后，Server 为 `(challenge ID, artifact revision)` 原子创建或获取一个 Mapping WorkItem。文件系统提升和数据库 enqueue 无法构成单一事务，因此 scanner 会扫描所有已发布目录，补回 Server 崩溃后未完成的 enqueue。

Mapper 基于固定 taxonomy revision 产生完整 ChangeSet。它只能修改目标 challenge 的 ChallengeMapping，但可复用或提出 Skill、Tag 和 `Skill.requires` 变更。候选先通过严格 JSON 与静态校验，再由 Curriculum Reviewer 和 SRE Reviewer 并行审核：

- 两份合法 reviewer 结论同时持久化，任一 reviewer 调用或结果无效时，下一 attempt 重新执行整个 reviewer pair。
- 两者 approve 后进入无模型 Publisher。
- 任一 reject 后，Mapper 读取候选和两份正式意见，进入新的语义 round。
- 模型传输、超时、typed result 缺失、无效 JSON 或静态校验失败都是技术失败，不创建新 round。
- 同一 round 累计十次技术失败后释放 WorkItem 并带退避重新进入 Work List；运行中的调用不会因达到预算被提前中断。只有 artifact 消失或 revision 变化才会 Cancelled。

Mapper、reviewer 和 Publisher 的并发规则以 [`internal/taxonomy/`](../../internal/taxonomy/) 为准。多个 WorkItem 可以并行分析；Publisher 用 PostgreSQL lease 串行发布。仅修改一个 challenge mapping 且引用定义未改变的过期候选可以在最新 revision 上重新确定性校验；新增或修改 Skill、Tag、`Skill.requires` 的过期候选必须基于最新 revision 重新经过完整委员会。

## 静态发布规则

Publisher 在临时目录构造完整 snapshot、校验、按内容计算 revision、写入 `revisions/<sha256>`，再原子替换 `current` 指针。它不运行模型，也不修改 challenge artifact。

至少要求：

- 每个 mapping 引用的 challenge ID、title、artifact revision 与发布目录精确一致。
- 每个 ChallengeMapping 至少一个 Tag、至少一个 outcome，并且恰有一个 primary outcome。
- Skill、Tag、entry/outcome、requires 引用存在且 title 精确匹配；同一列表不得重复，entry 与 outcome 不能重叠。
- 每个 source Skill 最多一个 Skill mapping；`requires` 不能自指或成环。
- 新增 Skill 是可独立解释、可多题复用的能力；新增 Tag 是稳定浏览维度，不能用同义词或细粒度 Skill 伪装。

challenge 内容任何变化都会改变 artifact revision。旧 mapping 因而自动失效，题目在新的 mapping 发布前不会公开；只修改 taxonomy 关系不需要重新构建镜像或重新验证题目。

## Catalog、工作台与作者状态

Server 以当前 snapshot 构造 CatalogIndex。只有 current mapping 的 challenge ID、title 和 revision 都精确匹配当前发布目录时，才返回该题；不存在默认 Tag、旧 revision 回退、标题模糊匹配或未分类公开路径。已开始或已完成的用户仍能通过历史和 Environment 恢复原题，不受 Catalog 准入变化影响。

公开 API 只投影浏览所需的结构化信息：Tag、主要 outcome、全部 outcome，以及 entry skill 的直接 prerequisites。前端以 ID 筛选，以 title 显示；不在当前阶段开放完整 taxonomy、委员会意见、Skill/Tag CRUD 或全局图 API。

My Space 根据 current snapshot 与当前 revision 的 WorkItem 投影作者可见状态：

| 状态 | 含义 |
| --- | --- |
| `mapped` | 已完成 exact mapping，题目在公开 Catalog。 |
| `mapping` | 正在排队、执行或等待发布，暂不公开。 |
| `retrying` | 遇到技术错误，系统将在 `next_run_at` 后重试。 |
| `blocked` | WorkItem Failed 或 Cancelled，无法自动继续。 |

状态不会暴露模型 prompt、工具参数或内部错误详情。

## 当前非目标

当前不实现 Dream、周期 taxonomy review、自动修改既有 taxonomy、全局技能图、强制解锁、学习路径、推荐或排行榜。它们依赖稳定的多题 taxonomy 与真实学习数据；后续方向见根目录 [`NEXT.md`](../../NEXT.md)。真实模型验收的边界见[测试与真实验收](../operations/testing.md)。
