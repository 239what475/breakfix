# HTTP 与终端接口

[`api/openapi.yaml`](../../api/openapi.yaml) 是 HTTP JSON 契约的唯一来源。Server 的 Go 路由、前端生成类型和 CI 校验都以它为准；本文只说明接口边界和认证规则，不维护逐字段副本。

## 认证与会话

注册和登录使用用户名、密码与 TOTP。登录成功后 Server 签发 JWT，受保护的 JSON 接口使用 `Authorization: Bearer <token>`。

终端不将 JWT 放入 WebSocket URL。浏览器先通过带 `Authorization` 的
`POST /api/challenges/{id}/terminal-ticket` 请求一张一分钟有效、仅能消费一次的
ticket；ticket 绑定用户、Environment UID、挑战和 tmux window，PostgreSQL 只保存其
SHA-256 hash。随后 WebSocket 只携带 ticket 与 window。升级前和 upgrader 都会将
`Origin` 与运行时配置的精确 `ui_origin` 比较，缺失或不同 Origin 不会消费 ticket。

题库摘要是公开只读接口；完整题目内容、挑战环境、作者会话、个人空间和助手均需要登录。

## 接口分组

| 范围 | 用途 |
| --- | --- |
| `/api/auth/*` | 注册与登录。 |
| `/api/challenges` | 公开题库摘要；携带 JWT 时附加当前用户的完成、活动与检查点进度。 |
| `/api/challenges/{id}/*` | 读取题面、启动、重置、停止、查询检查点、终端 ticket/窗口和挑战助手。 |
| `/api/me/space*` | 当前用户的学习、环境和作者聚合视图。 |
| `/api/authoring/sessions*` | 作者讨论、生成确认、已验证 revision 查看和发布。 |
| `/api/internal/*` | 仅 Agent Worker/Verifier 使用的围栏领域工具、Generator 工作区和 artifact 下载；必须携带内部密钥，不能当作公开 API。 |

终端使用 `/api/challenges/{id}/terminal` WebSocket。字节流不通过 OpenAPI JSON schema 表达；其 ticket 签发 JSON 接口在 OpenAPI 中定义，WebSocket 的一次性 ticket 和 Origin 约束由 Server 负责执行。

挑战助手消息接口会返回 Server-Sent Events。前端的 SSE envelope 是本地客户端状态，故不与普通 JSON model 混为一谈；`make generate-api` 从 OpenAPI 同时生成 Go 路由模型和前端 API 类型，见 [`internal/api/server.gen.go`](../../internal/api/server.gen.go) 与 [`frontend/src/api/generated/`](../../frontend/src/api/generated/)。

## 兼容性规则

- 修改 HTTP JSON 先更新 `api/openapi.yaml`，再运行 `make generate-api`。
- `make verify-api-generated` 与 CI 会在临时目录重建并比较 Go 和前端生成物。
- CRD 不是 HTTP API 的附属模型；其权威来源独立于 OpenAPI，见[运行环境](runtime-environments.md)。
- 接口不提供用户上传任意 challenge artifact 或手动提交挑战完成状态。题目发布和挑战完成分别由作者工作流和检查点状态机驱动。
