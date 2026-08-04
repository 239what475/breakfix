# API 契约

公开 HTTP 契约由 [`api/http/openapi.yaml`](../../api/http/openapi.yaml) 定义，并生成 Server 的 Go 类型和 Web 的
TypeScript client：

```bash
make generate
make verify-generated
```

## 公开 HTTP

- 认证和用户资料：注册、登录、TOTP、`/api/me/space`。
- Catalog 与题目：浏览当前 RoadmapRevision 中可见的 challenge、读取 problem/solution/hint、开始或停止学习环境。
- Authoring：创建和读取会话、发送自然语言消息、确认生成、读取只读候选、确认发布。
- Assistant：在活动学习环境中发送消息并读取持久对话与工具证据。
- 终端：先经 JWT 保护的 HTTP 接口签发一次性 ticket，再由 WebSocket 消费。

浏览器不提交 challenge artifact，也没有做题 Submit。检查点由 Controller 自动评估；作者发布通过
`GenerationWorkflow` 的作者审核状态触发，而不是上传任意文件。

## 调试 HTTP

下列接口同样不进入 OpenAPI，也不由浏览器调用：

```text
GET /internal/debug/roadmap-revisions/{revision_id}/export
```

它导出一个指定 immutable RoadmapRevision 对应的完整 portable Catalog Release `.tar.gz`，用于本地检查和
内容迁移调试；详细的内容边界与确定性归档规则见 [Catalog Release](catalog-release.md)。

## 内部 Worker HTTP

内部 API 不属于 OpenAPI 公开契约。当前它们只供 Generate Worker 调用，并使用独立 role key：

```text
POST /api/internal/generation-workflows/claim
POST /api/internal/generation-workflows/:id/renew
POST /api/internal/generation-workflows/:id/context
POST /api/internal/generation-workflows/:id/agent-runs
POST /api/internal/generation-workflows/:id/phase

```

Generate Worker 的 artifact/workspace 子接口同样要求 generation lease。所有请求使用严格 JSON 解码，
携带 Workflow ID、lease owner、state attempt 与 expected state；Server 只接受当前 lease 的 typed 结果。

## 终端与流传输

终端 WebSocket 使用一次性 ticket，不接受 URL JWT。Server 验证 ticket 的用户、Environment、challenge、
窗口和过期时间，并要求浏览器 Origin 与配置的 `ui_origin` 完全一致。terminal connection 与 usage
session 被持久化，因此 Server 重启或 WebSocket 断开后，环境清理仍能按活动状态收敛。
