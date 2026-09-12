# API 契约

公开 HTTP 契约由 [`api/http/openapi.yaml`](../../api/http/openapi.yaml) 定义，并生成 Server 的 Go 类型和 Web 的 TypeScript client：

```bash
make generate
make verify-generated
```

## 公开 HTTP

- 认证和用户资料：注册、登录、TOTP、`/api/me/space`。
- 运维场景：`/api/operations/scenarios` 列出当前 active 的运维场景 revision，按稳定 ID 读取内容，并开始、重置或停止环境。
  Catalog 摘要包含 `scenario_tags`、runtime、发布时间和可用状态；没有课程图、难度或关系边。文档实践化将使用独立的
  `/api/documentation` namespace。
- Authoring：创建和读取会话，通过 SSE 发送自然语言消息并观察 Server-owned `AgentRun`。
- Generator application API：保存或修订 Plan、确认生成、操作 workspace、提交 candidate、读取内容审核、确认内容发布、请求
  修改或取消。确认内容后直接进入发布，不存在分类审核步骤。
- Assistant：在活动学习环境中发送消息并读取持久对话与工具证据。
- 终端：先经 JWT 保护的 HTTP 接口签发一次性 ticket，再由 WebSocket 消费。

浏览器不提交场景 artifact，也没有手动完成提交。检查点由 Controller 自动评估；作者发布通过 `GenerationWorkflow` 的内容审核决定触发，
而不是上传任意文件。

## Generator application API 与 breakfix-mcp

Generator 工具面有两个入口：网页 Authoring Agent 直接作为 Server 内 Eino function tools 调用，本机 `breakfix-mcp` 通过
HTTPS 和用户 Token 调用同一组 `/generator/...` 端点。所有有副作用的请求都携带 workflow、candidate revision 与幂等键；Server
原子校验用户所有权、当前状态和版本，重复请求返回同一结果，版本变化的请求被拒绝。`confirm_generation` 是创建 workflow 的唯一
对外工具，不存在绕过对话确认的创建接口。

`breakfix-mcp` 将当前不可变内容审核包投影到本机可丢弃目录。连接器校验摘要、写入同级临时目录并原子 rename，只返回只读
`review_path`。本地目录不是编辑输入，删除后可从 Server 重新同步，也绝不参与 workflow 恢复。Token、Sandbox ID、PVC、内部地址
或凭据不进入审核包、工具结果或日志；远程连接必须使用 HTTPS。

## 内部 Worker HTTP

内部 API 不属于 OpenAPI 公开契约，只供 Runtime Worker 以独立 role key 调用：

```text
POST /api/internal/runtime-actions/claim
POST /api/internal/runtime-actions/:id/renew
POST /api/internal/runtime-actions/:id/source/archive
POST /api/internal/runtime-actions/:id/build/complete
POST /api/internal/runtime-actions/:id/artifact-publish/complete
POST /api/internal/runtime-actions/:id/verification/environment
POST /api/internal/runtime-actions/:id/verification/complete
POST /api/internal/runtime-actions/:id/scenario-publish/complete
POST /api/internal/runtime-actions/:id/failure/infrastructure
POST /api/internal/runtime-actions/:id/failure/artifact
POST /api/internal/runtime-resource-reaps/claim
POST /api/internal/runtime-resource-reaps/complete
```

没有 AgentRun、Generator workspace proxy、Kubernetes base image 或 build archive 下载接口。每个 Runtime Action 使用稳定的
`scope + parent_id + owner_id + candidate_id + state + state_version` identity 和 lease owner；`runtime_attempt` 不进入资源
identity。`scope` 只允许 GenerationWorkflow、Catalog Entry 与 Catalog Commit，Server 只接受当前 lease 的对应 typed result。

## 终端与流传输

终端 WebSocket 使用一次性 ticket，不接受 URL JWT。Server 验证 ticket 的用户、Environment、场景、窗口和过期时间，并要求浏览器
Origin 与配置的 `ui_origin` 完全一致。terminal connection 与 usage session 被持久化，因此 Server 重启或 WebSocket 断开后，环境
清理仍能按活动状态收敛。

Authoring 与 Assistant 的 SSE 只订阅已持久化的 Server AgentRun。浏览器断开不会取消该 Run；完成结果原子写入会话后，浏览器可通过
普通读取接口重新取得对话或现场说明更新，不回放未完成 token。
