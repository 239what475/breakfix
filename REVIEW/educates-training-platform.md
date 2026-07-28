# Educates Training Platform 对照审查

## 审查对象

- 本地路径：`/home/what/myproject/breakfix-similar-projects/educates-training-platform`
- 审查提交：`25dd76f6`
- 定位：Apache-2.0 的 Kubernetes hands-on workshop 平台。它以 `Workshop`、Training
  Portal 和会话资源组合出“一人一套 workshop 环境”。

## 已确认的设计

1. `project-docs/custom-resources/workshop-session.md` 说明 `WorkshopSession` 负责会话
   URL、ingress 和会话环境；文档也明确它通常是 Training Portal 管理的内部实现细节。
2. `workshop-samples/*/resources/workshop.yaml` 将 workshop 元数据、环境和内容来源声明
   分开；教学内容由 Markdown 文件组成。
3. `project-docs/workshop-content/workshop-instructions.md` 支持 terminal、editor、dashboard
   和 section 动作；`examiner:execute-test` 可调用测试并显示结果，也可由页面加载触发。
4. 生产指南明确讨论镜像拉取限流、共享 OCI cache、每 session registry、资源请求和
   workshop container 内存。这些是大量并发实验环境的实际运维问题。

## 与 Breakfix 的对照

| 维度 | Educates | Breakfix | 判断 |
| --- | --- | --- | --- |
| 环境单位 | Workshop session | Challenge revision 的 ContainerEnvironment/VClusterEnvironment | Breakfix 的不可变题目快照和 vcluster 语义更贴合做题。 |
| 内容 | Markdown 页面和可点击动作 | `problem.md`、`solution.md`、checkpoint hint 与运行时脚本 | Breakfix 更强调故障修复的自由操作路径。 |
| 完成判断 | 可由页面动作触发 examiner 测试 | Controller 每 4 秒运行同一组检查点，状态写入 CRD | 自动轮询更符合无需 Submit 的产品设计。 |
| 状态所有权 | Portal/Operator 管理 session | Server 只写 spec，Controller 唯一写 status | Breakfix 的边界更明确，不能倒退。 |

## 可以吸收

### 现在吸收

- **内容能力的 fixture 套件**：Educates 将 terminal、editor、section 和 Markdown 动作放进
  可运行 sample workshop。Breakfix 应为题目格式建立同类的固定 fixture：单检查点、多
  检查点、依赖检查点、container、vcluster、提示、解答 Markdown 和初始化失败。目标是
  让 UI/内容格式的变更有稳定的真实样本，而不是拿唯一的 `cleanup-logs` 覆盖一切。
- **内容作者验收入口**：Educates 的 workshop 定义和 sample 可被单独部署验证。Breakfix
  已有完整 VerifyTask，但 `make docker-challenge` 只做 build。题库生产前应提供一个
  单题命令，使用真实 runtime 初始化，执行 `answer.sh` 和 checkpoint 协议，并输出各
  checkpoint 结果。它必须复用 VerifyTask 的语义，不能另造一套本地 verifier。
- **基础镜像与内容依赖清单**：生产指南对每个外部镜像/下载的可用性有显式约束。Breakfix
  已禁止 build-time 联网；应把题目运行时允许依赖的预置镜像、二进制和网络假设也写成
  内容审查清单，避免量产后才发现某题依赖偶然的外网或 node cache。

### 题库具备数据后再做

- 教学页面内的 editor/dashboard 动作可作为未来的**可选辅助**。例如让题目链接到指定
  文件或展示环境内服务。但它必须只做导航/展示，不能将修复命令自动注入终端，否则会
  破坏学习过程和助手边界。
- 外部服务访问和每环境镜像 cache 只在真实题目需要用户启动可访问服务、且镜像拉取成为
  容量瓶颈时评估。当前全局受控 Registry 已是正确起点。

## 不采用

- 不采用用户点击 examiner 测试作为完成协议。Controller 自动检查点和 CRD status 是
  Breakfix 的权威事实，用户点击只会制造两个不同步的判断路径。
- 不采用每 session Registry。它会增加 PVC、凭据、垃圾回收和镜像地址复杂度；当前
  challenge 镜像由可信 Publisher/Server 管理，用户环境不应拥有发布能力。
- 不以 Educates 的 Portal/Operator 替换 Server、Controller 或 Environment CRD。它的
  通用 workshop 模型无法表达 Breakfix 的 VerifyTask、vcluster 与作者发布流程。

## 结论

Educates 最值得借鉴的是内容 sample/作者验收纪律和规模化运维经验，而不是其运行时
架构。优先完成单题真实验收和内容 fixture；其余能力等题库和并发数据出现后再验证。
