# 代码布局

目录表达所有权，而不是只表达语言。`cmd` 只负责进程生命周期；`internal` 的每个一级目录都有固定边界。不使用 `common`、`utils`、
`helpers`、`models` 或无领域前缀的万能包。

## 仓库根目录

```text
api/        HTTP 与 CRD 契约及其受控生成物
build/      镜像构建输入
cmd/        server、controller、runtime-worker、breakfix-mcp、catalog-release 的 main
config/     非密钥配置与 Secret 示例
deploy/     Kubernetes 清单和唯一的 Kind overlay
docs/       长期架构、运维、产品和参考文档
scripts/    人工运行的 dev、Kind、Incus 脚本
test/       黑盒、跨进程和真实运行时测试
web/        Vue 应用、Node 配置和 TypeScript 生成 client
```

根目录 `README.md` 只提供入口；`NEXT.md`、`REVIEW.md` 与 `TODO.md` 分别记录方向、持续审查和当前可执行工作，不复制 API 契约。

## Internal 边界

```text
internal/
  adapter/       Kubernetes、Incus、OCI、OpenSandbox、LLM、PostgreSQL、MCP connector 和内部 HTTP 的具体实现
  application/   Authoring、GeneratorService、Catalog、发布、Environment、学习和 Assistant 用例
  bootstrap/     进程装配、配置、运行时 snapshot 与 Catalog Release 打包入口
  content/       portable scenario、candidate archive、发布 materialization 文件契约
  domain/        Scenario、workflow、Environment、Catalog、Authoring 和 checkpoint 协议的不变量
  transport/     公开 HTTP、SSE、WebSocket 和内部 Worker HTTP 映射
  testkit/       测试共享 fixture 与 provider fake
```

`content` 是共享文件边界：它处理 portable source、已发布目录和 candidate archive，不拥有数据库状态、Kubernetes SDK 或 Worker lease。
`domain` 保持 SDK 无关；`adapter` 是唯一直接依赖外部 SDK 的层。`application` 以接口依赖 domain 和 adapter 提供的 port，
`transport` 只做身份、输入、错误与响应映射。

依赖方向：

- `domain` 不依赖 HTTP、PostgreSQL、Kubernetes、Incus、OCI、OpenSandbox 或模型 SDK。
- `content` 不依赖数据库、HTTP 或 provider SDK。
- `application` 不依赖 HTTP handler 或具体 SDK。
- `adapter` 实现具体 I/O；SDK 类型不得泄漏到 `domain` 的公开模型。
- `bootstrap` 负责具体实现的选择和后台服务启动；业务规则不写在 `main` 或启动代码中。

Go 领域和目录统一使用 `Scenario`。产品层将其显示为可运行场景；稳定 ID 前缀和历史 revision 语义保持不变。
