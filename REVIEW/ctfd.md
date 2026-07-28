# CTFd 对照审查

## 审查对象

- 本地路径：`/home/what/myproject/breakfix-similar-projects/ctfd`
- 审查提交：`dd9b996`
- 定位：Apache-2.0 的 CTF 题目、提交、积分和赛事运营平台。

## 已确认的设计

1. `CTFd/models/__init__.py` 将 Challenge、Hints、Solves 和 Unlocks 建模为独立对象；
   Challenge/Hints 的 `requirements` 可表达 prerequisites。
2. `CTFd/api/v1/challenges.py` 根据已解题集合过滤前置条件，并投影 `solved_by_me`、解题数
   和可见 hints；`api/v1/hints.py` 负责提示解锁、前置提示和可选积分成本。
3. `CTFd/plugins/challenges/` 为题型提供可插拔 challenge class；题目提交和 flag 判断是
   它的主要完成语义。

## 与 Breakfix 的对照

CTFd 的“题目依赖题目”适合竞赛解锁。Breakfix 已有更合适的分层：Skill.requires 表示
能力前置 DAG，Challenge mapping 表示 entry/outcome，用户仍可直接开始任何题。CTFd 的
Solves 是一次提交事实；Breakfix 的完成事实来自 Environment checkpoint 全通过，不能
引入第二个 Submit 或人工完成记录。

## 可以吸收

### 现在吸收

- **可见性和状态投影分离**：保持 Catalog 只读取发布题目与 taxonomy，而个人空间读取用户
  学习投影。为每道题明确投影 `not started`、`in progress`、`completed` 和当前 checkpoint
  进度，避免将 Environment CRD 内部 phase 直接当成长期用户状态。当前 API 已有基础；
  量产题目时应为这组投影补目录/个人空间 fixture。
- **提示作为独立学习事件**：提示内容继续保存在文件系统，但当用户打开 checkpoint hint
  或 solution 时，Server 应记录轻量、幂等的学习事件（用户、challenge revision、checkpoint
  ID、时间）。它不是扣分或解锁条件；它为后续题目质量分析提供“何处卡住”的证据。

### 题库具备数据后再做

- Catalog 可以展示同题完成量和作者可见的聚合完成率，但不做排行榜、积分或实时解题流。
  这些只有在题库和稳定用户群出现后才有解释价值。
- 可按 Skill.requires 展示“建议先练习”的题目，但永远不锁住挑战。它应使用 taxonomy
  mapping，不能复用 CTFd 的 challenge prerequisite 表。

## 不采用

- 不采用 flag 提交、Submit 按钮、积分解锁或排行榜。它们鼓励猜测结果，直接冲突于真实环境
  和自动检查点。
- 不采用通用题型插件，让 challenge 包运行任意平台扩展。题目输入必须继续局限于已验证的
  文件格式和受控 runtime。
- 不把 hint 设置为成本或尝试次数阈值。Breakfix 的目标是学习和诊断，提示应由学习者自主
  打开且可被后续分析，而不是惩罚。

## 结论

CTFd 值得借鉴的是用户状态投影和提示使用事件，不是竞赛机制。应保留 Skill 图而非题目
强制解锁，并保持完成事实只来自 Controller 检查点。
