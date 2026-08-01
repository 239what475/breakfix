# 参考项目审查后的后续建议

本文件从 `REVIEW/` 的逐项目审查中收敛出后续可吸收的方向。当前平台已经采用
`GenerationWorkflow`、`TaxonomyWorkflow`、`NodeEnvironment` 和 `VK8sEnvironment` 的明确
边界；先用真实题库验证产品，再按真实使用数据扩展能力。

## 后续可吸收清单

| 阶段 | 参考项目 | 借鉴方向 | 对 Breakfix 的具体调整 |
| --- | --- | --- | --- |
| **P1：首批题目上线后** | CTFd、PrairieLearn、Artemis、Open edX | 学习行为事件 | 记录 hint、solution、assistant 打开事件；不记录完整终端，不引入扣分。 |
| **P1：首批题目上线后** | Artemis、PrairieLearn | 检查点结果体验 | 工作台和完成页按 checkpoint 显示当前状态、首次通过、提示、解答和诊断；作者看到同一份结构化验证报告。 |
| **P1：首批题目上线后** | Educates、Zero to JupyterHub | runtime 配置与边界 | 集中 Node/VK8s Environment runtime profile 的默认值与上限；补两类环境 NetworkPolicy 的真实边界验收。 |
| **P1：首批题目上线后** | DOMjudge | 验证报告可重跑性 | 设计基础镜像和 runner 变更后的 re-verification campaign，不混入作者生成工作流。 |
| **P2：一个领域有 12-20 道已验证题后** | Killercoda Groups、Open edX | 人工学习路径 | `data/paths/` 引用已发布的 Skill/Challenge ID；路径是 taxonomy 的视图，不强制解锁。 |
| **P2：有学习事件后** | Artemis、PrairieLearn、CTFd | 题目质量分析 | 聚合 checkpoint 流失、完成率、耗时、重置和提示依赖；按 revision 供作者修订题目。 |
| **P2：有内容需求后** | Play with Docker、Educates、Killercoda | 用户服务预览 | 仅提供按 Environment/端口授权的内部预览代理，环境回收即失效；不允许任意 Ingress。 |
| **P3：有并发数据后** | Zero to JupyterHub、Play with Docker、Educates | 容量与清理运营 | 指标化活跃环境、启动时延、checkpoint 延迟、清理吞吐；再决定 culling 调度和资源配额。 |

## 明确不做

- 不恢复用户可见 Submit、上传 artifact 或 flag 验证。
- 不采用 CTF 积分、排行榜、题目强制解锁、隐藏 checkpoint 或提示惩罚。
- 不用 Educates、JupyterHub 或 Open edX 替换 Server、Controller、CRD、文件系统题库或
  两类持久 Workflow。
- 不引入长期用户 workspace、Hub/Proxy、LMS、Git/CI 作业模型、通用题型插件或 privileged DinD。
- 不为检查点引入 sidecar、systemd、tmux 或 kubelet probe；当前 `pods/exec` 直到有指标
  证明瓶颈前保持不变。
