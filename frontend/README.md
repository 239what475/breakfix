# Breakfix Frontend

Breakfix 前端是一个 Vue 单页应用，构建产物由 Server 内嵌并提供给浏览器。它包含 Catalog、Challenge Workspace、Assistant、Authoring 与 My space；路由状态由应用外壳管理，不依赖 vue-router。

```bash
npm ci
npm run dev
make generate-api
npm run build
```

从仓库根目录运行 `make generate-api`，它会从 [`api/openapi.yaml`](../api/openapi.yaml) 同时生成 Go 路由模型和 `src/api/generated/` 中的 JSON API 类型。不要手改生成物；`src/api/client.ts` 只负责请求和 SSE，`src/api/types.ts` 仅保留生成类型别名与客户端流事件状态。`npm run generate:api` 仅供前端生成器本身使用。

完整的产品、接口和端到端测试说明见 [`docs/README.md`](../docs/README.md) 与 [`test/`](../test/)。
