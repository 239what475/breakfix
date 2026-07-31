# DOMjudge 对照审查

## 审查对象

- 本地路径：`/home/what/myproject/breakfix-similar-projects/domjudge`
- 审查提交：`aaa6508`
- 定位：面向 ICPC/IOI 竞赛的提交评测系统。

## 已确认的设计

1. `doc/manual/overview.rst` 将 DOMserver、judgehost、team 和 jury 的职责分开；judgehost
   可横向扩展，文档明确它应是专用执行机器。
2. `doc/manual/install-judgehost.rst` 说明 judgedaemon 在隔离 chroot 内编译/执行提交，
   通过 REST API 向 DOMserver 领取工作；`runguard` 负责资源隔离。
3. `doc/manual/problem-format.rst` 基于 problem package specification，支持测试数据、
   verdict 和 rejudge；`doc/manual/running.rst` 区分 pending、wrong、correct 等提交状态。

## 与 Breakfix 的对照

Breakfix 的 Build/Publisher/Verifier 分离对应更严格的 judgehost 边界：Builder 无凭据、
Publisher 才能推镜像、Verifier 在真实 Environment 上运行 answer/checkpoints。CandidateRevision
已经是“题目资产的可信评测流程”，Environment checkpoint 是“学习者环境的持续完成事实”。
两者不能合并为一个通用 judge 队列。

## 可以吸收

### 现在吸收

- **可重跑而不混淆结果的报告模型**：CandidateRevision 的 report 应保留 attempt 编号、任务阶段、
  题目 revision、镜像 digest 和结构化结论。重跑创建新 attempt，旧报告保持可读；不能
  用新日志覆写曾经验证过的结果。
- **验证执行者的资源证据**：对 Build、Publisher、Verifier 维持独立资源限制、deadline、
  可观察状态和专门的真实验收。DOMjudge 对执行机容量的关注支持这一点，但不要照搬其
  “每 20 team 一个 judgehost”的竞赛容量公式。

### 题库具备数据后再做

- 当基础镜像、runtime init 或 checkpoint runner 发生平台级变更时，设计显式的
  re-verification campaign：对已发布 artifact revision 创建新的验证记录，而不是悄悄
  改写“曾验证通过”的事实。只有完整报告成功后才更新该 revision 的平台兼容标记。

## 不采用

- 不采用用户提交、编译语言、测试数据和 verdict 作为学习完成模型。
- 不向学习者暴露隐藏 testcase/verdict 细节。Breakfix 的公开 checkpoint 应给出有教育
  意义的 summary，但不能通过暴露所有 verifier 内部输出来帮助绕过题目。
- 不以 chroot judgehost 替换固定 worker、Environment 与 NetworkPolicy 隔离。

## 结论

DOMjudge 验证了独立、可审计、可重跑评测执行者的价值，而 Breakfix 已有更贴合 Kubernetes
题目安全边界的实现。近期应加强 CandidateRevision 报告的不可变性和可读性，不应引入竞赛模型。
