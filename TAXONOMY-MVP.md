# 技能图 MVP 设计

## 目标

本阶段完成一个已验证题目进入公开 Catalog 前的 taxonomy 闭环。范围只使用现有
`data/challenges/cleanup-logs/`，不批量生产题目，不新增 Dream，也不实现全局技能图或
学习路径。

目标状态如下：

```text
作者确认发布 challenge
  -> Server 原子提升已验证 artifact
  -> 创建或收敛该 artifact revision 的 Mapping WorkItem
  -> Mapper + Curriculum Reviewer + SRE Reviewer
  -> Publisher 发布不可变 taxonomy revision
  -> Catalog 仅展示 exact mapping 对应的 challenge
```

题目真实可运行仍由 VerifyTask 证明；taxonomy 只证明它应如何被浏览和学习。两者都通过，
题目才是公开 Catalog 的一员。

## 不变量

- Challenge、Skill、Tag 的身份一律使用 opaque ID。目录和 YAML 文件名只提供人类可读的
  source slug，不能参与 API 查找、映射解析或用户学习记录。
- `cleanup-logs` 目录保留可读名称，但其 `challenge.yaml.id` 改为新生成的 `chal-...`。
  测试、Environment、学习记录和 taxonomy mapping 随之只引用新 ID。
- Challenge workflow 不创建、不修改 Skill、Tag 或 mapping。所有 taxonomy 变更都经由独立
  的 Mapping WorkItem、三 agent 委员会和无模型 Publisher。
- taxonomy 的权威内容是 Server data PVC 下的 `taxonomy/current` 指向的不可变快照；数据库
  只保存 WorkItem、候选、审查结论、重试与租约，不能成为 taxonomy 的第二份权威数据。
- 只有 mapping 的 challenge ID、title 和 artifact revision 都与当前发布目录精确相等时，
  才能被 Catalog 使用。不存在默认 Tag、旧 revision 回退或按标题模糊匹配。
- 技术失败不生成新的语义 round；当前 WorkItem 按既有退避重试。reviewer reject 才要求
  Mapper 基于两份正式意见进入下一 round。

## 发布与 Mapping

作者点击发布后，Server 先完成现有的 artifact promotion。成功后立即为该 challenge ID 与
artifact revision 创建或获取 Mapping WorkItem；同一 ID 与 revision 必须去重。

发布目录与 PostgreSQL 无法构成单一事务，因此顺序是：

1. 原子提升已验证 artifact 到 challenge 目录。
2. 创建 Mapping WorkItem。
3. taxonomy scanner 继续扫描所有已发布目录，作为第 2 步因 Server 崩溃未完成时的收敛机制。

Mapper 从 immutable artifact 和当前 taxonomy revision 构造完整 ChangeSet。它可以复用或提出
Skill、Tag、Challenge mapping 和 `Skill.requires`，但只能修改当前目标 challenge 的 Challenge
mapping。合法候选经 Curriculum 与 SRE 两个 reviewer 并行审查；两者 approve 后 Publisher 才
写入新的完整快照并原子替换 `taxonomy/current`。

已发布 challenge 的内容发生任何变化时，artifact revision 必然改变。旧 mapping 自动失效，
Server 创建新的 Mapping WorkItem；新 mapping 发布前，该 challenge 不再公开。

## Catalog 准入与作者可见状态

`publishedChallenges` 必须以当前 taxonomy snapshot 建立 CatalogIndex，并且只返回 index 中存在
exact mapping 的 challenge。没有 current snapshot、没有 mapping 或 mapping 已过期时，公开
Catalog 不显示该 challenge。

这不是删除题目：发布目录、作者记录、VerifyTask 结果和既有学习记录仍然保留。它只是尚未完成
公开分类。

作者的 My Space 必须能显示自己已发布题目的 taxonomy 状态，避免一个刚发布的题目在 Catalog
中暂时不可见却没有解释。状态由当前快照和最新同 revision WorkItem 投影，不单独保存：

| 状态 | 条件 | 作者看到的含义 |
| --- | --- | --- |
| `mapped` | current snapshot 存在 exact mapping | 已进入公开 Catalog。 |
| `mapping` | 无 exact mapping，WorkItem 正在排队、执行或等待发布 | 分类正在进行，题目暂不公开。 |
| `retrying` | 无 exact mapping，WorkItem 有下一次执行时间 | 分类遇到技术问题，系统将自动重试。 |
| `blocked` | 无 exact mapping，WorkItem 为 Failed 或 Cancelled | 分类无法继续，题目暂不公开。 |

作者不能直接编辑 YAML 或 ChangeSet；`blocked` 仅显示简短可理解的状态，不泄露模型 prompt、工具
参数或内部错误细节。后续再决定维护者处理入口。

## 公开读取模型

公开 API 只提供题目浏览所需的 taxonomy 投影，不直接暴露完整 snapshot、WorkItem 或委员会意见。
OpenAPI 是字段的唯一契约来源；以下是目标形状：

```yaml
ChallengeSummary:
  id: chal-...
  tags:
    - id: tag-...
      title: Linux
  primary_outcome:
    id: skill-...
    title: Repair permission-related service failures

ChallengeContent:
  taxonomy:
    outcomes:
      - id: skill-...
        title: Repair permission-related service failures
        primary: true
    entry_skills:
      - id: skill-...
        title: Inspect file ownership and permissions
        requires:
          - id: skill-...
            title: Read Linux file metadata
```

Tag、Skill 和 prerequisite 引用始终带 ID 和 title。前端筛选以 ID 为键，title 只用于显示。`requires`
只返回 entry skill 的直接前置能力，不返回整张 DAG，也不提供单独的公开 Skill/Tag CRUD 或全局图 API。

## UI 范围

Catalog 沿用现有按 Tag、难度、运行时和发布时间的浏览模式：

- Tag 筛选改用 Tag ID，卡片仍显示 Tag title。
- 卡片增加一个主要 outcome，说明这题主要练习什么能力。
- 题目详情和工作台 Problem 面板显示“建议具备”和“本题练习”。建议具备列出 entry skill 及其
  直接 prerequisite；本题练习突出 primary outcome，其他 outcome 作为补充。
- 不在本阶段绘制全局 DAG、不强制解锁题目、不新增学习路径或推荐算法。

已完成或正在做题的用户仍可通过历史记录和 Environment 恢复访问原 challenge，即使该 challenge
因新 revision 尚未重新 mapping 而暂时不在公开 Catalog 中。

## 验收

确定性测试必须证明：

1. opaque challenge ID 与 source slug 解耦，按 ID 查找不依赖目录名。
2. 无 mapping、stale mapping、ID/title 不匹配 mapping 都不会进入公开 Catalog。
3. 只有 exact mapping 才返回结构化 Tag、entry skill、outcome 和直接 prerequisite。
4. promotion 后立即创建或去重 Mapping WorkItem；scanner 能恢复 promotion 后崩溃前遗漏的
   enqueue。
5. My Space 正确投影 `mapped`、`mapping`、`retrying`、`blocked`，且不暴露委员会内部内容。
6. OpenAPI、Go、TypeScript 生成物和前端筛选使用结构化 taxonomy 引用，不能保留按 title 关联的
   兼容路径。

额外提供一个显式、单次的真实 E2E：在隔离 data directory 中仅放入已验证的 `cleanup-logs`，从无
taxonomy snapshot 开始，使用真实 Mapper、Reviewer、Publisher 和 Agent Worker，等待 taxonomy
发布后确认 Catalog 与浏览器展示。该测试有单一总 deadline，失败立即输出 WorkItem、Run 和
Publisher 状态并清理临时数据；它不属于日常 Playwright 套件，也不与其他真实 Agent 验收并发。

## 非目标

- 不生成更多 challenge，不建立题目批量生产流水线。
- 不实现 Dream、周期性 taxonomy review 或自动修改既有 taxonomy。
- 不实现学习路径、每日推荐、全局技能图、强制解锁、排行榜或题目质量分析。
- 不改变 VerifyTask、Environment、OpenSandbox、Agent Runtime 的职责或生命周期。
