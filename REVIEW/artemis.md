# Artemis 对照审查

## 审查对象

- 本地路径：`/home/what/myproject/breakfix-similar-projects/artemis`
- 审查提交：`131f26e`
- 定位：MIT 许可的高校互动学习平台，支持编程、建模、文本和测验练习，并以 build agent
  和测试反馈支持多次练习。

## 已确认的设计

1. `README.md` 明确区分练习、参与、提交、结果和测试级反馈；编程练习可展示自动测试、
   静态分析和个体反馈。
2. README 还将 build agents 定义为独立执行构建、测试和分析的基础设施，并强调作者可配置
   feedback、hidden tests、提示、重评和统计。
3. 前端和后端存在练习/参与/提交/结果的独立模型，而不是只存一个总完成标志。

## 与 Breakfix 的对照

Breakfix 的公开 checkpoints 本来就是 SRE 题的“测试级结果”，并且比代码测试更接近用户
可观察的最终状态。当前缺少的是这些状态随 attempt 的持久学习历史；只保存最近 CRD status
会使个人复盘和作者分析失去“哪一步首先完成”的信息。Breakfix 的 VerifyTask 已承担可信
构建与验证，不能引入 Artemis 的 Git/CI 作业作为第二条题目执行路径。

## 可以吸收

### 现在吸收

- **结果层级的用户体验**：工作台应始终按 checkpoint 展示 title、依赖、当前状态、summary、
  可选 details、提示和首次通过时间；完成页按相同顺序复盘，而不是只显示“挑战完成”。这
  直接建立在 PrairieLearn 文档建议的 checkpoint 事件投影上。
- **作者看到的可解释验证摘要**：发布前展示 build、初始化、answer 和每个 checkpoint 的
  结果，而不是只给 VerifyTask 成功/失败。失败信息应按受信任的结构化 report 呈现，不暴露
  可能含敏感内容的任意 job log。
- **题目质量分析的事件字典**：为未来预留稳定事件名：attempt started、checkpoint first
  passed、completed、hint opened、solution opened、assistant asked、reset。现在只采集
  必需字段，不实现看板或评分。

### 题库具备数据后再做

- 作者视图按 challenge revision 聚合 checkpoint 流失、首次通过时间和提示依赖；发布新
  revision 后不混淆旧数据。
- 为题目提供人工“问题报告”流，但它是内容反馈，不是 Artemis 式作业批改流程。

## 不采用

- 不引入 Git 仓库、提交次数处罚、CI 平台或隐藏 checkpoint。SRE 修复题不是编程作业，公开
  目标和多种有效修复路径更重要。
- 不把单次 checkpoint 当作可累加分数，或让部分通过终结 challenge；全部检查点通过才是
  完成事实。
- 不引入完整课程、考试、班级和助教模型。当前题库仍处于内容生产阶段。

## 结论

Artemis 最重要的启发是让每个可评判步骤成为可解释、可回顾的学习结果。Breakfix 应补足
checkpoint 事件和结果呈现，但不应复制其教育管理或 CI 体系。
