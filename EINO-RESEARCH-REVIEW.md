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
