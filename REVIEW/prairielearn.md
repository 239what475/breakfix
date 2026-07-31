# PrairieLearn 对照审查

## 审查对象

- 本地路径：`/home/what/myproject/breakfix-similar-projects/prairielearn`
- 审查提交：`cac6c0f`
- 定位：题目可复用、支持变体、外部 grading job 和 workspace 的在线评测系统。

## 已确认的设计

1. `docs/concepts/index.md` 将 Question、Assessment、Variant、Submission 和 Grade 分开；
   同一题可在多份 assessment 中复用，学生看到的是固定 variant 的提交历史。
2. `docs/externalGrading.md`、`apps/grader-host/` 将不可信外部评测放进独立 Docker job，
   对超时、队列状态、结构化结果和显示结果做了明确处理。
3. `docs/workspaces/index.md` 将 workspace 与 variant 关联，区分 workspace 中的可变文件
   和要保存给 grader 的显式 `gradedFiles`。
4. `docs/elements/pl-hidden-hints.md` 说明渐进提示依赖当前 variant 的有效提交次数；
   `docs/faq.md` 说明已开始 variant 的内容变更和 reset 必须有显式语义。

## 与 Breakfix 的对照

Breakfix 的 Challenge revision 相当于不可变题目版本，Environment 相当于一次真实运行时
attempt；不同点是没有用户提交和随机 variant。Builder、Publisher、Verifier 的分离已经
比 PrairieLearn 外部 grader 更严格：不可信构建不持有 Kubernetes/Registry 权限，真实
验证复用最终 runtime。不能把用户环境的周期 checkpoint 退化成每次提交起一个 grading job。

## 可以吸收

### 现在吸收

- **检查点通过的持久事件**：当前 CRD `status.checkpoints` 仅保存最近一次报告，学习记录
  保存 attempt/完成，但没有每个 checkpoint 的首次通过事实。Server 应从 Controller status
  做幂等投影，至少持久化 `(environment UID, challenge revision, checkpoint ID,
  first_passed_at, summary)`。Controller 仍是唯一 status 写者；数据库只保存学习事件。
  不记录每四秒的失败输出，避免把轮询日志误当学习数据。
- **版本变化的显式规则**：已启动 Environment 使用 immutable revision 已经正确。应为题目
  编辑/重新发布补文档和测试：新 revision 不改变旧 attempt；旧 revision 的 checkpoint
  通过事件仍可用于历史展示；Catalog 只显示 current mapping 精确匹配的新 revision。
- **作者验收结果结构化**：单题真实验收输出应按 build、runtime init、answer、每个
  checkpoint 组织，既给作者看，也可直接作为 CandidateRevision report 的稳定 UI 投影。不得把
  verifier 的原始日志当作唯一反馈。

### 题库具备数据后再做

- 用 checkpoint 首次通过、hint/solution 打开和完成事件计算漏斗、典型耗时与提示依赖度。
  聚合数据用于修改题意和 checkpoint，不用来给用户打分。
- 若出现真正需要随机化的纯知识题，再单独设计 variant；不能让随机化破坏 SRE 故障题的
  可复现性、answer.sh 和 taxonomy mapping。

## 不采用

- 不采用用户提交驱动的 grading job、分数或 partial credit。每个公开 checkpoint 是完成
  条件，不是可通过反复提交刷出的分数。
- 不复制 `gradedFiles` 抽取模型。Breakfix 验证的是整个真实环境的状态，白名单文件会漏掉
  service、process、network 和 k8s 状态。
- 不因参考其 workspace 而将用户环境持久化为长期目录；Environment 的生命周期由 CRD
  和清理策略决定。

## 结论

PrairieLearn 强化了一个真实缺口：应把 checkpoint 的首次完成从最近状态中投影为稳定学习
事件，并明确 revision 历史。其外部 grading 与变体模型不适合当前真实故障练习。
