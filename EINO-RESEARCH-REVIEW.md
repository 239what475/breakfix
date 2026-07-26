# Agent Runtime 迁移设计审查

本文只记录实现过程中确认的设计缺口及已采用的调整；它不是兼容层，也不改变 `EINO-RESEARCH.md` 的总体边界。

## 2026-07-26：Assistant Run 缺少可恢复的工作区输入

原设计的 `agent_runs` 列出了 `input_revision`，但没有记录一次 Assistant 请求中的 `current_window` 与 `open_windows`。这两个值决定
`get_terminal_scrollback` 可以读取的 tmux 窗口。若 Worker 在首次执行前或执行中退出，只依靠环境 UID 无法无损恢复这个工具边界，只能错误地退化为默认窗口。

调整：在 `agent_runs` 增加不可变的 `input_json`，仅保存该 Run 的结构化输入。Assistant 当前保存：

```json
{"current_window":"shell-1","open_windows":["shell-1","shell-2"]}
```

它不保存终端输出、工具结果、模型 prompt、模型 reasoning、Eino checkpoint 或逐 token event；这些仍分别遵守原设计的数据所有权和瞬时流边界。

同一问题也影响 Assistant 已有的 evidence 标签。通用 `agent_messages` 因此增加 `metadata_json`，仅保存小型、用户可见的结构化注解。Assistant 最终消息写入实际调用过的只读工具标签，不写工具参数、工具内容或模型内部状态。

## 2026-07-26：Eino 内建流式重试不能满足废弃 partial draft 的语义

Eino `v0.9.13` 的 `ModelRetryConfig` 在流式调用中会并行消费一份完整流来决定是否重试，同时让另一份流向下游发送 chunk。重试判定发生在 stream 结束后，因此浏览器可能已经看到了随后会被废弃的文字。

调整：Assistant 不使用 Eino 内建流式重试。一次完整 Agent 调用遇到明确的传输错误时，最多重新执行三次；每次重新执行前通过瞬时 Server event 发布 `reset`，浏览器清空旧草稿。Assistant 没有写工具，重复该调用不会重复领域副作用。最终消息仍只在成功后写入 `agent_messages`，且只写一次。

## 2026-07-26：OpenSandbox Go SDK 的模块路径与 Worker 授权边界

`opensandbox-group/OpenSandbox` 的 SDK tag `sdks/sandbox/go/v1.0.5` 实际声明的 Go module 仍为
`github.com/alibaba/OpenSandbox/sdks/sandbox/go`。以仓库新路径执行 `go get` 会因 module path 不匹配失败；接入时必须使用该声明路径并固定 `v1.0.5`，不能通过 `replace` 伪造第二份 SDK。

同时，SDK 的 `ConnectSandbox` 会以 lifecycle API key 调用 `GetEndpoint`，而 endpoint headers 是按 Sandbox 返回的访问材料，并不携带
Run attempt、过期时间或撤销接口。它们不能满足 Worker 失去租约后立即失效的 fencing 要求，也不能作为全局 lifecycle key 的安全替代物交给 Worker。

调整：Server 是唯一 OpenSandbox SDK/lifecycle credential 持有者。Generator Worker 的 files、exec 和 archive 操作均通过受内部 API 保护的
Server proxy；每个请求由 Server 使用 `run_id`、`attempt` 和 `lease_owner` 校验当前租约后才转发到绑定的 Sandbox。这个选择使用
`EINO-RESEARCH.md` 已规定的“没有官方可撤销 scoped credential 时使用 Server 代理”分支，不建立双 backend 或向 Worker 注入 OpenSandbox key。

## 2026-07-26：OpenSandbox Helm 0.2.0 与 Server 0.2.2 的 POC 阻断项

`helm/opensandbox/0.2.0` 的 `Chart.lock` 声明 controller dependency 为 `0.1.0`，而同一 tag 的 `Chart.yaml` 声明为 `0.2.0`。
因此 `helm dependency build` 直接失败，必须在 POC worktree 中以同 tag 的本地 dependency 运行 `helm dependency update` 才能渲染。该
tag 的默认 Server 镜像还是 `v0.1.13`，与 SDK `v1.0.5` 的源码版本不对应；POC 显式固定为 Controller `v0.2.0`、Server `v0.2.2`、
`execd v1.0.21` 和 egress `v1.1.4`，不能直接把 chart default 当作已验证组合。

真实 Kubernetes POC 中，Server `v0.2.2` 成功创建 `BatchSandbox` 和 server-managed PVC，但在给 PVC 写 ownerReference 时记录
`Got an unexpected keyword argument '_content_type' to method patch_namespaced_persistent_volume_claim`。OpenSandbox 自身也明确说明：
controller 驱动的 TTL 删除依赖该 ownerReference；失败后 lifecycle `DELETE` 的 label sweep 只能作为回退，因此 TTL 路径可能遗留 PVC。

`helm/opensandbox/0.2.0` 还实际嵌入旧 `opensandbox-server` chart `0.1.0`。该模板对 PVC 仅授予 `create/get`，缺少
`list/delete/patch`。所以 POC 中显式 `DELETE /sandboxes/{id}` 虽返回 `204` 并删除 `BatchSandbox`，同样无法列出或删除带有
`opensandbox.io/volume-managed-by=server` 标签的 PVC。SDK `v1.0.5` 对应的上游源码 commit 已在 chart 模板中补全这三个权限，
但尚未发布与该源码匹配的 Helm tag。即使补全 RBAC，`v0.2.2` 的 `_content_type` 调用仍会在本地 Kubernetes Python client `35.0.0`
上先于 API 请求失败，ownerReference 依然不能创建。

调整：不能使用 `v0.2.2` 作为固定生产 provider，也不能用 `latest` 或本地 patch 掩盖该缺陷。上游若发布修复版本，必须以固定
Server/Chart revision 重跑 POC；若在本阶段没有官方修复，则需要先把 workspace PVC 的权威生命周期明确改由 Breakfix Server 管理，
并用独立设计说明替换“依赖 OpenSandbox TTL 回收 PVC”的前提，不能在实现中悄悄增加扫尾逻辑。在这两者之一完成前，不开始
`OpenSandboxBackend` 或 Generator 迁移。

## 2026-07-26：Taxonomy reviewer 的持久化边界

`EINO-RESEARCH.md` 原先将 Curriculum Reviewer 和 SRE Reviewer 描述为两个可并行的独立 Agent Run；但这会与既有
Taxonomy 委员会约束冲突：一名 reviewer 的结论不能在另一名 reviewer 因模型或协议错误失败时成为可恢复的半成品。若把两份
结论分别写入领域表，下一次调度必须维护并恢复不对称的 reviewer 状态，既扩大 WorkItem 状态机，也让一次不完整审查看起来像
领域事实。

调整：Mapper 保持无 Session 的 `taxonomy-mapper` Run。两名 reviewer 由一个 `taxonomy-review` Run 负责，在同一 Worker
attempt 内并行执行两个独立的 Eino typed-result 调用；只有两者都正常返回并通过严格领域校验时，Server 才在一个事务中持久化
完整 review pair、完成该 Run 并推进 Round。任一调用失败时，review pair 不写入领域表；该 Run 在模型层最多完成三次传输重试后
直接进入终态，由 Server 对当前 Round 计入一次 Taxonomy 技术失败预算并安排下一 Run。这样没有供应商 session、没有 partial review
事实，也不会在运行中的调用被十次预算提前中断。该调整不改变 Mapper 与两名 reviewer 的职责分工或并行模型调用，只把 reviewer
pair 作为不可分割的持久化边界。
