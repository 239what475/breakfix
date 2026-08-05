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
- Authoring：创建和读取会话、发送自然语言消息、确认生成、显式取消未完成 workflow、读取只读候选、确认发布。
- Assistant：在活动学习环境中发送消息并读取持久对话与工具证据。
- 终端：先经 JWT 保护的 HTTP 接口签发一次性 ticket，再由 WebSocket 消费。

浏览器不提交 challenge artifact，也没有做题 Submit。检查点由 Controller 自动评估；作者发布通过
`GenerationWorkflow` 的作者审核状态触发，而不是上传任意文件。

## 调试 HTTP

下列接口不进入 OpenAPI，也不由浏览器调用：

```text
POST /internal/debug/roadmap-maintenance
GET /internal/debug/roadmap-revisions/{revision_id}/export
```

默认 `debug.enabled=false` 时这两个路由根本不会注册。部署者只有在显式启用该开关、并由单独的
`breakfix-debug` Secret 提供 `credential` 后才能远程调用；请求必须携带 `X-Breakfix-Debug-Key`。用户 JWT、
Runtime Worker identity 和其他业务凭据都不能作为该 credential。

maintenance 用于请求一次仍受 idle-window 约束的 Roadmap maintenance；export 导出指定 immutable
RoadmapRevision 对应的完整 portable Catalog Release `.tar.gz`，用于本地检查和内容迁移调试。详细的内容边界与
确定性归档规则见 [Catalog Release](catalog-release.md)。

## 内部 Worker HTTP

内部 API 不属于 OpenAPI 公开契约。当前它们只供 Runtime Worker 调用，并使用独立 role key：

```text
POST /api/internal/runtime-actions/claim
POST /api/internal/runtime-actions/:id/renew
POST /api/internal/runtime-actions/:id/source/archive
POST /api/internal/runtime-actions/:id/build/complete
POST /api/internal/runtime-actions/:id/artifact-publish/complete
POST /api/internal/runtime-actions/:id/verification/environment
POST /api/internal/runtime-actions/:id/verification/complete
POST /api/internal/runtime-actions/:id/challenge-publish/complete
POST /api/internal/runtime-actions/:id/failure/infrastructure
POST /api/internal/runtime-actions/:id/failure/artifact
POST /api/internal/runtime-resource-reaps/claim
POST /api/internal/runtime-resource-reaps/complete

```

没有 AgentRun、Classifier retrieval、Generator workspace proxy、Kubernetes base image 或 build archive
下载接口。所有 Runtime Action 请求使用严格 JSON 解码，携带稳定的
`scope + parent_id + owner_id + candidate_id + state + state_version` identity 和 lease owner；`runtime_attempt`
不会出现在资源 identity 中。`scope` 只允许 GenerationWorkflow、Catalog Entry 与 Catalog Commit，Server 只接受当前
lease 的对应 typed result。Kubernetes 构建在 Worker
直接写入 Registry 的 build-scoped immutable OCI reference，Server 只保存引用。

## 终端与流传输

终端 WebSocket 使用一次性 ticket，不接受 URL JWT。Server 验证 ticket 的用户、Environment、challenge、
窗口和过期时间，并要求浏览器 Origin 与配置的 `ui_origin` 完全一致。terminal connection 与 usage
session 被持久化，因此 Server 重启或 WebSocket 断开后，环境清理仍能按活动状态收敛。

Authoring 与 Assistant 的 SSE 只订阅已持久化的 Server AgentRun。浏览器断开不会取消该 Run；完成结果
原子写入会话后，浏览器可通过普通读取接口重新取得对话或题意更新，不回放未完成 token。
