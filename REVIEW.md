# 参考项目审查后的后续建议

本文件从 [`REVIEW/`](REVIEW/README.md) 的逐项目审查中收敛出后续可吸收的方向。已经落地的
基础能力不在这里重复；当前目标是先用真实题库验证产品，而不是继续扩展通用平台能力。

## 当前结论

不重构 Server、Controller、Agent Worker、Environment/VerifyTask CRD 或 Build/Publisher/
Verifier 的信任边界。它们已经覆盖参考项目中最关键的隔离、可信验证和可恢复学习状态能力，
并且比通用 workshop/评测平台更符合真实 SRE 故障题的需求。

## 后续可吸收清单

| 阶段 | 参考项目 | 借鉴方向 | 对 Breakfix 的具体调整 |
| --- | --- | --- | --- |
| **P1：首批题目上线后** | CTFd、PrairieLearn、Artemis、Open edX | 学习行为事件 | 记录 hint、solution、assistant 打开事件；不记录完整终端，不引入扣分。 |
| **P1：首批题目上线后** | Artemis、PrairieLearn | 检查点结果体验 | 工作台/完成页按 checkpoint 显示当前状态、首次通过、提示、解答和诊断；作者看到同一份结构化 VerifyTask 摘要。 |
| **P1：首批题目上线后** | Educates、Zero to JupyterHub | runtime 配置与边界 | 集中 Environment runtime profile 的默认值/上限；补 workspace/vcluster NetworkPolicy 的真实边界验收。 |
| **P1：首批题目上线后** | DOMjudge | 验证报告可重跑性 | 每次重新验证保留独立 VerifyTask/report；补基础镜像和 runner 变更后的 re-verification campaign 设计。 |
| **P2：一个领域有 12-20 道已验证题后** | Killercoda Groups、Open edX | 人工学习路径 | `data/paths/` 引用已发布的 Skill/Challenge ID；路径是 taxonomy 的视图，不强制解锁。 |
| **P2：有学习事件后** | Artemis、PrairieLearn、CTFd | 题目质量分析 | 聚合 checkpoint 流失、完成率、耗时、重置和提示依赖；按 revision 供作者修订题目。 |
| **P2：有内容需求后** | Play with Docker、Educates、Killercoda | 用户服务预览 | 仅提供按 Environment/端口授权的内部预览代理，环境回收即失效；不允许任意 Ingress。 |
| **P3：有并发数据后** | Zero to JupyterHub、Play with Docker、Educates | 容量与清理运营 | 指标化活跃环境、启动时延、checkpoint 延迟、清理吞吐；再决定 culling 调度和资源配额。 |
| **P3：证明确有瓶颈后** | Kubernetes/运行时实践 | checkpoint 传输通道 | 仅在 `pods/exec` 指标证明瓶颈后，按 `NEXT.md` 迁移到 workspace 内 checkpointd。 |

## 已确认正确，暂不改动

| 参考项目 | Breakfix 已有对应能力 | 结论 |
| --- | --- | --- |
| Educates | 一用户一套真实环境、运行时初始化、镜像供应链边界 | 不替换为 Workshop/Portal。 |
| JupyterHub | 可恢复环境状态、短期终端凭据、清理生命周期 | 不引入 Hub/Proxy 或长期 home。 |
| CTFd | 用户题目状态与提示展示 | 不引入 Submit、flag、积分或强制解锁。 |
| PrairieLearn、DOMjudge | 独立可信验证执行者和结构化结果 | 维持 VerifyTask 与 Builder/Publisher/Verifier 分离。 |
| Open edX | 发布内容与学习事实分离 | 保持文件系统 artifact、PostgreSQL 事件和 taxonomy snapshot 分层。 |
| Play with Docker | 临时终端和服务可达性问题 | 不采用 privileged DinD、文件状态或固定 session TTL。 |

## 明确不做

- 不恢复任何用户可见 Submit、上传 artifact 或 flag 验证。
- 不采用 CTF 积分、排行榜、题目强制解锁、隐藏 checkpoint 或提示惩罚。
- 不用 Educates/JupyterHub/Open edX 替换 Server、Controller、CRD 或文件系统题库。
- 不引入长期用户 workspace、Hub/Proxy、LMS、Git/CI 作业模型、通用题型插件或 privileged
  DinD。
- 不为检查点引入 sidecar、systemd、tmux 或 kubelet probe；当前 `pods/exec` 直到有指标
  证明瓶颈前保持不变。
