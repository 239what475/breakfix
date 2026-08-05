# 代码布局

目录必须表达所有权，而不是只表达实现语言。`cmd` 只负责进程生命周期，`internal` 中的每个一级目录都有固定边界；不使用
`common`、`utils`、`helpers`、`models` 或无领域前缀的万能包。

## 仓库根目录

```text
api/        HTTP 与 CRD 契约及其受控生成物
build/      镜像构建输入
catalog/    可选：与真实基础题库一起提交的 portable Catalog Release source
cmd/        可执行进程的 main
config/     非密钥配置与 Secret 示例
deploy/     Kubernetes 清单和唯一的 Kind overlay
docs/       长期架构、运维、产品和参考文档
scripts/    人工运行的 dev、Kind、Incus 脚本
test/       黑盒、跨进程和真实运行时测试
web/        Vue 应用、Node 配置和 TypeScript 生成 client
```

根目录的 `README.md` 只提供入口；`NEXT.md`、`REVIEW.md` 与 `TODO.md` 分别记录路线、持续审查和当前可执行工作，
不能复制架构或 API 契约。

## Internal 边界

```text
internal/
  adapter/       Kubernetes、Incus、OCI、OpenSandbox、LLM、PostgreSQL 和内部 HTTP 的具体实现
  application/   作者、生成、catalog、学习和执行快照用例
  bootstrap/     各进程的配置加载、依赖装配和生命周期
  buildinfo/     由 ldflags 写入的版本信息
  content/       portable challenge、发布 materialization 和 Roadmap source 文件契约
  controller/    NodeEnvironment 与 VK8sEnvironment reconciler
  domain/        Workflow、Environment、Catalog、Authoring、Roadmap 的状态与不变量
  testkit/       仅供测试使用的 PostgreSQL 等基础设施
  transport/     HTTP API、WebSocket/SSE、嵌入式 UI 和健康检查
  worker/        Runtime Action 的确定性外部操作执行器
```

`content` 是共享的文件内容边界：它处理 portable challenge source、已发布题目目录、candidate archive 和 Roadmap
source，但不拥有数据库状态、Kubernetes SDK 或 Worker lease。`domain` 保持 SDK 无关；`adapter` 是唯一直接依赖外部
服务 SDK 的层。`application` 编排用例，`transport` 只做认证、输入输出和流传输，`bootstrap` 是唯一可以同时连接多个
层的位置。

## 依赖方向

- `domain` 不依赖 HTTP、PostgreSQL、Kubernetes、Incus、OCI、OpenSandbox 或模型 SDK。
- `adapter` 实现具体 I/O；SDK 类型不得泄漏到 `domain` 的公开模型。
- `worker` 只通过 `adapter/internalapi` 与 Server 领取 Runtime Action lease、读取不可变上下文和报告结果；它不持有
  PostgreSQL DSN、模型凭据、OpenSandbox 凭据或 Server data PVC。
- `controller` 只调和 Environment CRD 与 provider，不读取 Workflow、HTTP transport 或数据库 repository。
- `cmd/*/main.go` 只解析 flag、安装信号处理并调用相应 bootstrap；业务装配不回流到 `cmd`。

跨进程和浏览器测试放在 `test/`；与 package 同生命周期的单元和集成测试留在被测 package 旁边。生成物只能由
`make generate` 创建，并由 `make verify-generated` 复验。
