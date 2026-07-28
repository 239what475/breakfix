# 参考项目对照审查

本目录逐个审查 `/home/what/myproject/breakfix-similar-projects/` 中下载的仓库。审查
基线是当前 Breakfix 的文件化 challenge、Environment/VerifyTask CRD、Controller 自动
检查点、作者验证发布、taxonomy snapshot 和 PostgreSQL 学习记录，而不是假设平台尚未
实现这些能力。

每份文档都区分三类结论：

- **现在吸收**：能直接降低真实题目生产或学习反馈成本，且不改变既有所有权边界。
- **题库具备数据后再做**：依赖多题内容、真实用户行为或容量数据。
- **不采用**：与“真实环境中的自动检查点”或现有安全边界冲突，或解决的是不同产品。

| 项目 | 文档 | 主要参考面 |
| --- | --- | --- |
| Educates Training Platform | [educates-training-platform.md](educates-training-platform.md) | Kubernetes workshop 生命周期、内容动作、生产运行经验 |
| Killercoda Scenario Examples | [killercoda-scenario-examples.md](killercoda-scenario-examples.md) | 可读场景目录、分步教学资产 |
| Killercoda Scenario Groups | [killercoda-scenario-examples-groups.md](killercoda-scenario-examples-groups.md) | 课程/路径静态编排 |
| JupyterHub | [jupyterhub.md](jupyterhub.md) | 多用户环境恢复、Spawner 生命周期和权限 |
| Zero to JupyterHub | [zero-to-jupyterhub-k8s.md](zero-to-jupyterhub-k8s.md) | Kubernetes 部署、网络策略、存储和 idle culling |
| CTFd | [ctfd.md](ctfd.md) | 题目扩展点、提示与前置条件呈现 |
| PrairieLearn | [prairielearn.md](prairielearn.md) | 评测记录、反馈和题目版本语义 |
| Artemis | [artemis.md](artemis.md) | 测试级反馈、作者分析和练习体验 |
| DOMjudge | [domjudge.md](domjudge.md) | 独立评测执行者和可重判结果 |
| Open edX Platform | [openedx-platform.md](openedx-platform.md) | 课程内容版本、完成事件和长期学习产品边界 |
| Play with Docker | [play-with-docker.md](play-with-docker.md) | 临时浏览器终端的历史实现与反例 |

这些文档不构成迁移计划，也不意味着引入对应依赖。实施前仍需以当前 Breakfix 的真实
challenge、CRD 和端到端验收证明改动必要。

## 跨项目结论

多个项目的共同经验不要求重写 Breakfix。现有的 Environment/VerifyTask CRD、自动
checkpoint、真实 VerifyTask、文件系统 artifact 和独立可信 Job 已经是正确的平台骨架。
近期只应评估以下四项，按顺序实施并用真实题目验证：

1. **单题作者验收与内容 fixture**：提供复用真实 runtime 初始化、`answer.sh` 和全部
   checkpoint 的单题验收入口；为题目格式和工作台保留小型固定样例矩阵。
2. **checkpoint 学习事件**：由 Server 从 Controller status 幂等投影每个 checkpoint 的
   首次通过；继续把最近检查结果留在 CRD，避免将四秒轮询日志写入数据库。
3. **教学资产 lint 与结构**：保证每个 checkpoint 的题面术语、hint、solution 章节和
   静态资源引用一致；不向用户自动执行命令。
4. **VerifyTask 报告可读性与运行时边界测试**：保留 immutable attempt/report，按 build、
   init、answer、checkpoint 展示；扩大 container/vcluster 的恢复和 NetworkPolicy 测试。

学习路径、质量看板、受控服务预览、再验证 campaign 和容量调度均需要多题或并发数据，
不应抢在上述四项和首批真实题目之前实现。Hub/Proxy、LMS、竞赛积分、长期用户 workspace、
用户提交和 privileged DinD 均不在采用范围内。
