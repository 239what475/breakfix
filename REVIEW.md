# 当前架构审查

本文件只记录尚未定案的架构问题。已经确认的设计、实施步骤和验收要求只在 [`TODO.md`](TODO.md) 中维护，不能在这里重复拆成实现细节。

## 当前架构

```text
Browser / breakfix-mcp
        |
        v
      Server
        |
        +-- PostgreSQL
        |     AuthoringSession / AgentRun / GenerationWorkflow / CandidateRevision
        |
        +-- Authoring Agent
        |     通过 GeneratorService 操作生成 workspace
        |
        +-- Judge / Catalog publication finalizer
        |
        +-- Workspace lifecycle
        |     OpenSandbox + PVC + Server data PVC archive
        |
        +-- Runtime Worker API
              Build / Artifact publish / Verify

Runtime Worker ----> Registry / Kubernetes / Incus Environment
```

## 已确认的边界

- Authoring 是 Server 内的交互式 Agent；网页和 MCP 共用 GeneratorService，不再有独立的 Generator Agent deployment。
- 一个 Authoring AgentRun 只受 `DeadlineAt` 约束。模型传输故障在同一 Eino 回合内退避重试，不重建 AgentRun；下一条作者消息才开启新回合。
- workspace 文件和快照是跨回合、跨 Server 重启的恢复载体；不恢复模型内存，不重放半完成工具调用。
- Event 是作者可见但不进入模型输入的持久状态消息。原始 provider、HTTP 和执行器错误只能留在 AgentRun 内部字段和服务日志。
- workspace 快照只保存 `/workspace`，恢复优先级为 snapshot、最近 CandidateRevision、空 workspace。
- 网页消息、Plan 变更和 generation action 都需要持久化幂等身份；重复请求不能重复运行模型或重复改变状态。
- `submit_candidate` 是 candidate 确定性校验的唯一权威；无效 candidate 保留 workspace 并把完整校验报告交给 Agent。
- 副作用工具的超时或断连返回结构化“执行状态未知”，但不退休 Sandbox、不丢弃 workspace，也不阻止 Agent 发起任意检查命令。workspace turn 只隔离不同客户端回合，不是命令进程锁。
- 不提供 Authoring AgentRun 的显式取消；浏览器断开或关闭页面不取消正在运行的回合。

## 结论

当前没有新的架构决策阻塞本阶段。时间预算、Event、快照、archive 安全性、请求幂等和 E2E 靶场属于已确认的实施工作，应按 `TODO.md` 的提交计划完成和验证。
