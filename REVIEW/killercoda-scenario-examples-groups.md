# Killercoda Scenario Examples Groups 对照审查

## 审查对象

- 本地路径：`/home/what/myproject/breakfix-similar-projects/killercoda-scenario-examples-groups`
- 审查提交：`9fa8cd7`
- 定位：Killercoda 的公开课程分组内容仓库。

## 已确认的设计

`README.md` 和根 `structure.json` 表明，一个目录可被当作 course/group；一旦存在
`structure.json`，只有显式列出的 scenario/目录被纳入。该模型的核心是人工维护的静态
顺序和显式包含集，而不是自动推导知识关系。

## 与 Breakfix 的对照

Breakfix 已将 Challenge、Tag、Skill 和 mapping 分离：Skill.requires 是能力 DAG，题目只
通过 entry/outcome 映射接入。`NEXT.md` 已明确学习路径应是技能图上的人工编排视图，不能
成为另一套 challenge 依赖事实。因此 Killercoda 的 group 是未来“路径展示层”的参考，不能
替代 taxonomy。

## 可以吸收

### 题库具备数据后再做

- 题目达到一个领域的 12 到 20 道后，引入文件化 `data/paths/<source-slug>/path.yaml`。
  路径显式引用已发布 challenge ID 和 Skill ID，保存标题、简介、顺序和可选分支；用户在
  路径上的进度仍保存 PostgreSQL。
- 发布时校验引用存在、challenge revision 已被 Catalog 准入、标题显示快照一致；路径不
  得创建新的 Skill/Tag，不得绕过 taxonomy snapshot。
- 将 “structure.json 存在时拒绝未列出内容” 的显式性用于路径 lint，避免目录扫描悄悄把
  实验性题目加入公开学习路径。

## 不采用

- 不把 challenge 目录层级解释为题目依赖或技能层级。目录只是阅读友好的 source slug；真实
  关系必须使用 opaque ID 和 mapping。
- 不在当前只有一道公开题目时实现 course 系统、强制解锁或路径进度 UI。没有内容覆盖时，
  静态顺序只会固化猜测。

## 结论

这是一个明确的后续内容组织参考，而非当前平台缺口。先生成一个领域的高质量题目，再以
Skill DAG 为约束创建人工路径；不能让课程分组取代现有 taxonomy 设计。
