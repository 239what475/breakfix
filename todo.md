# 仓库结构彻底重构

## 目标

当前仓库的核心问题不是文件数量，而是所有权不清：`internal/server` 同时承担 HTTP、领域编排、文件系统、
数据库调用和运行时装配；`internal/db` 同时承担 schema、所有领域 repository 和状态机；`internal/k8s`、
`internal/incusprovider` 又混合了 Provider、CRD、终端协议和应用逻辑。`generator`、`builder`、`publisher`、
`verifier` 保留了已经取消的部署边界，名称和实际运行模型不一致。

这次重构的目标是让目录直接表达系统边界。完成后，任何文件的归属可以只从路径判断：它是领域规则、应用用例、
传输协议、基础设施适配、Worker 执行器、Controller reconcile，还是进程装配。不能再出现“先放进
`server`/`db`，以后再整理”的兜底位置。

这是一次开发阶段的彻底迁移：不保留旧 package path、type alias、re-export shim、旧 Kustomization 入口、旧脚本名或
旧配置键。数据库仍按当前开发约定重建，不写兼容迁移。

## 最终根目录

```text
api/                         外部契约的唯一来源
  http/openapi.yaml          HTTP OpenAPI 源文件
  http/oapi-codegen.yaml     OpenAPI Go 生成配置
  v1/                        CRD Go 类型源文件和 controller-gen deepcopy 生成物

build/                       镜像和发布构建输入，不含 Kubernetes 清单
  images/

cmd/                         只有进程 main；不含业务装配细节
  server/
  controller/
  generate-worker/
  taxonomy-worker/

config/                      所有非密钥配置源和示例
  app/
  examples/

catalog/                     Git 管理的可复用题库 release，不是运行时 data
  release.yaml               release identity、source path 与 contentRevision 映射
  challenges/                题目 candidate source，不含环境发布字段
  taxonomy/                  已审查的 Skill、Tag、mapping snapshot source

deploy/                      只保存 Kubernetes 部署资源
  manifests/                 生产共同资源，均为普通 YAML
  crds/                      controller-gen 生成的 CRD YAML
  overlays/kind/             唯一开发 overlay

docs/                        当前系统的长期文档
  architecture/
  operations/
  product/
  reference/

internal/                    不对外暴露的应用实现，按下文分层

scripts/                     人可执行的开发、Kind、Incus 和生成脚本
  dev/
  kind/
  incus/

test/                        黑盒和跨进程测试；不保存业务单元测试
  e2e/
  runtime/
  agent/
  support/

web/                          Vue 应用、Node 配置和生成的 TypeScript client
```

顶层保留 `README.md`、`Makefile`、`kustomization.yaml`、`go.mod`、`go.sum`、`NEXT.md`、`REVIEW.md`、`TODO.md` 与
`package` 所需锁文件；CI 与工具元数据仍保留在 `.github/` 和根 dotfile。现有 `todo.md` 在本次迁移中统一为 `TODO.md`。
`data/` 是 Server 的本地运行时目录，生产对应 Server PVC，不属于仓库内容；构建产物、嵌入前端 dist、本地配置、测试报告和
全部运行数据一律 gitignore。

## Internal 分层

```text
internal/
  domain/
    agent/                   AgentSession、AgentRun、消息与其不变量
    authoring/               作者题意、revision、可见方案
    catalog/                 CatalogRelease、source identity、安装状态与 content revision
    challenge/               Challenge 格式、资产、发布 identity
    environment/             Node/VK8s 环境、终端和检查点的 Provider 无关模型
    generation/              GenerationWorkflow、CandidateRevision、阶段协议
    learning/                用户学习进度、attempt 和统计
    taxonomy/                Skill、Tag、mapping、snapshot、TaxonomyWorkflow

  application/
    assistant/               做题助手用例
    authoring/               作者对话、方案 revision 用例
    catalog/                 Catalog Release 安装、catalog 扫描和可见性
    environment/             启动、停止、状态投影和学习完成用例
    generation/              Server 侧 Workflow 创建、阶段提交、作者审核和发布
    taxonomy/                taxonomy 维护、snapshot 串行发布

  adapter/
    internalapi/             Worker 到 Server 的受认证 HTTP client
    kubernetes/              Kubernetes API、CRD client、Pod exec 和终端实现
    incus/                   Incus client、project、image、instance 和网络实现
    llm/                     Eino/模型调用、重试和 typed tool 适配
    oci/                     Registry client、OCI archive 与镜像操作
    opensandbox/             OpenSandbox SDK 与连接实现
    postgres/                schema 初始化及按领域拆分的 repository 实现
    vcluster/                vcluster CLI/SDK 适配

  controller/
    nodeenvironment/         NodeEnvironment Reconciler
    vk8senvironment/         VK8sEnvironment Reconciler
    manager.go                controller-runtime 装配

  worker/
    generate/                GenerationWorkflow 与 CatalogRelease 共用的构建、发布、验证执行器
      agent/                 Generator/Judge 与其 sandbox backend
      build/                 Node/K8s candidate build
      publish/               artifact staging 与作者题目的 final publish
      verify/                真实环境 answer/checkpoint 验证
    taxonomy/                Mapper、reviewer pair 和 taxonomy workflow 执行器

  transport/
    health/                  Server/Worker health 与 metrics HTTP surface
    httpapi/
      public/                浏览器 API handler 和 request/response 映射
      worker/                内部 Worker API handler 和 lease 认证
      middleware/            JWT、CORS、错误映射
      stream/                SSE 与 WebSocket 共用传输辅助代码
      ui/                    前端嵌入入口；只包含 embed 声明，不存放 dist

  bootstrap/
    config/                  配置加载、环境变量覆盖和进程级校验
    server/                  Server 依赖装配
    controller/              Controller 依赖装配
    generateworker/          Generate Worker 依赖装配
    taxonomyworker/          Taxonomy Worker 依赖装配

  buildinfo/                 版本、commit、构建时间
  testkit/                   PostgreSQL、时钟、HTTP、Kubernetes 等测试 fixture
```

### 依赖规则

1. `domain` 不依赖 Gin、`database/sql`、Kubernetes、Incus、OCI、OpenSandbox、Eino 或配置；只包含模型、验证、
   状态转移和由领域拥有的端口接口。
2. `application` 只依赖 `domain` 和其消费端定义的接口。它不能 import `adapter`、`transport`、Gin 或具体数据库类型。
3. `adapter` 实现领域或应用端口；一个 adapter 可以依赖 SDK，但不能把 SDK 类型泄漏到 `domain`、`application` 的
   public API 中。
4. `transport/httpapi` 只做认证、请求解码、调用 application、响应编码和流传输。它不能直接访问 PostgreSQL、文件系统、
   Registry、Kubernetes 或 Incus。
5. `worker/generate`、`worker/taxonomy` 通过 `adapter/internalapi` 与 Server 交互，绝不持有 PostgreSQL DSN。
   Generate Worker 内部的 build/publish/verify 是一个 Workflow 的阶段执行器，不再是独立“服务”或通用队列 worker。
6. `controller` 只调和 Environment CRD 与真实 Provider 资源，不导入 HTTP transport、数据库 repository 或 Workflow
   用例。
7. `bootstrap` 与 `cmd` 是唯一允许同时引用多个层的地方。`cmd/*/main.go` 只解析退出码、信号和调用对应 bootstrap。
8. 不创建 `common`、`utils`、`helpers`、`models`、`service` 或无领域前缀的万能包。复用代码必须属于明确的领域或平台边界。

## 当前目录到最终目录

| 当前位置 | 最终归属 | 处理原则 |
| --- | --- | --- |
| `internal/server` | `transport/httpapi`、`application/*`、`bootstrap/server` | 按 handler、用例、装配拆分，目录本身删除。 |
| `internal/db` | `adapter/postgres` | schema、连接和每个领域 repository 分文件；application 不再接触 `*db.DB`。 |
| `internal/k8s` | `adapter/kubernetes`，CRD 类型移至 `api/v1` | API 类型、client、exec、RBAC 辅助和 Provider 实现不再混放。 |
| `internal/incusprovider` | `adapter/incus` | 将 Incus SDK、镜像、project、网络、terminal 实现聚合为一个 adapter。 |
| `internal/vclustercli` | `adapter/vcluster` | 只保留 vcluster API/CLI 包装，不夹带 Controller 策略。 |
| `internal/registry` | `adapter/oci` | Registry endpoint、认证、archive、manifest 逻辑集中。 |
| `internal/opensandbox` | `adapter/opensandbox` | 只保留 OpenSandbox SDK、请求映射和连接实现。 |
| `internal/workspace` | `domain/generation` 与 `application/generation` | workspace record 是 GenerationWorkflow 的领域状态；PVC/Sandbox 的 ensure/cleanup 是 Server 用例，依赖由 Kubernetes 和 OpenSandbox adapter 实现。 |
| `internal/agentmodel` | `adapter/llm` | 模型构造、重试、typed tool 适配不作为领域模型。 |
| `internal/agentserver` | `adapter/internalapi` | 统一 Server 内部 API client；不按旧 worker 命名。 |
| `internal/generator`、`builder`、`publisher`、`verifier`、`generateworker` | `worker/generate` | 移除旧 Deployment 语义的顶层包名。 |
| `internal/taxonomyworker` | `worker/taxonomy` | taxonomy 工作流执行器与委员会实现放在同一边界。 |
| `internal/candidate` | `domain/generation` 与 `domain/catalog` | CandidateRevision 是生成领域对象；Catalog source 的 canonical content revision 由 catalog/challenge 领域共同定义。 |
| `internal/runtimeprofile`、`terminal`、`verification` | `domain/environment` 或 `worker/generate/verify` | 按“环境模型”与“验证执行”重新归属。 |
| `internal/catalogseed`、`cmd/catalog-seed` | 删除；Catalog Release 用例归入 `application/catalog` | 历史单题镜像初始化旁路。新的 release 安装不直接写已发布目录，也不复制 builder/publisher 实现。 |
| `internal/config`、`build`、`workerhealth`、`testpostgres` | `bootstrap/config`、`buildinfo`、`transport/health`、`testkit/postgres` | 消除没有层次的顶层技术包。 |
| `internal/api/server.gen.go` | `transport/httpapi/generated` | OpenAPI Go 生成物不能与业务实现混放。 |

迁移后旧目录必须直接删除，不能通过导入转发让两套结构并存。

## API、前端与生成物

1. 将 `api/openapi.yaml` 移到 `api/http/openapi.yaml`；Go HTTP 生成物输出到
   `internal/transport/httpapi/generated`，TypeScript 生成物输出到 `web/src/api/generated`。
2. 将 `internal/k8s/apis/breakfix/v1` 移到 `api/v1`。这里的手写 Go 类型是 CRD 契约源；
   deepcopy 与 `deploy/crds/*.yaml` 是唯一允许提交的生成物。
3. `make generate` 统一生成 CRD、OpenAPI Go 和 TypeScript；`make verify-generated` 在临时目录重跑并 diff。不存在各自
   隐藏的生成命令或手改生成文件。
4. 前端从 `frontend/` 改为 `web/`。Vite 输出只进入 `web/dist`，由 `transport/httpapi/ui` 的嵌入构建步骤消费；
   不再在 `cmd/server/frontend/dist` 留第二份目录。
5. `api` 只保存契约源和契约生成物，不能存 Handler、数据库模型或业务类型。

## 配置、脚本与部署

### 配置

```text
config/
  app/in-cluster.yaml
  app/local.example.yaml
  examples/runtime.env
  examples/worker-identity.env
```

- 非密钥的部署配置由根 `kustomization.yaml` 直接生成 `breakfix-config`；不再有 `config/kustomization.yaml`。
- Secret 只由管理员创建；仓库只保存无值 example，生产 Registry、内部 CA、外部 Registry 仍保持当前职责边界。
- 配置 schema、默认值和环境变量覆盖在 `bootstrap/config`；每个进程只调用一个对应的 `Validate*` 函数。
- `data_dir` 是 Server PVC 上的独立工作目录；本地开发可使用被 gitignore 的 `./data`。Server 只扫描已提交的
  `data_dir/challenges` 与 `data_dir/taxonomy`，不把构建产物或作者临时文件写回 Git 工作树。Catalog Release 安装期间的
  source materialization 位于 `data_dir/.staging/releases/<release-id>/`；Catalog API 只在数据库中该 release 为 `Ready` 后
  才读取其最终目录，因此 staging 或提交中断不会形成可见题库。

### Catalog Release 初始化

`CatalogRelease` 是题库的唯一初始化来源，不是持续同步器、用户可见提交入口或 Kubernetes CRD。它是 Server 持久化的
管理员级聚合，安装一个 Git 管理、OCI 分发、按 digest 固定的 `catalog/` source bundle 到一个尚未初始化的平台。

```text
CatalogRelease
  Pending -> Installing -> Committing -> Ready
                  |              |
                  +-------> CleaningUp -> Failed
```

`release.yaml` 只描述 portable source，不承担平台身份：

```yaml
apiVersion: breakfix.dev/catalog/v1
kind: CatalogRelease
metadata:
  name: foundation
  version: 2026.08.01
entries:
  - path: challenges/linux/cleanup-logs
    contentRevision: sha256:...
taxonomy:
  contentRevision: sha256:...
```

- 新平台只有在没有已安装 Catalog Release、`data_dir/challenges` 为空且没有 taxonomy `current` 时接受安装；不会覆盖或
  合并后来由用户工作流产生的 challenge、learning 事实或 taxonomy 变化。
- source bundle 由 CI 从 `catalog/` 打包为 OCI artifact。管理员只提交不可变 digest；Server 下载后先校验 manifest、
  每个 source path 对应的 canonical `contentRevision` 和 taxonomy source。release source 不包含或引用平台 challenge ID、
  slug、镜像或发布时间；集群中的进程不读取 Git 工作树，也不使用 ConfigMap 承载题库。
- 每个 catalog challenge 形成普通 CandidateRevision，并复用 Generate Worker 的 build、artifact staging、真实 verification
  与 cleanup 执行器。验证成功的 entry 进入 `ReadyToCommit`，不执行逐题 final publish；它不调用 Generator、Judge 或
  Taxonomy Agent，也没有 AuthoringSession。source 本身是已审查内容，安装阶段只验证它能在当前目标环境真实运行。
- GenerationWorkflow 的 source 只有两种：`authoring` 和 `release`。`authoring` 的 `ref` 指向作者会话，并保存作者确认时的
  revision；`release` 的 `ref` 必须指向单题 `CatalogReleaseEntry`，不能直接指向整批 CatalogRelease。Entry 保存所属 release、
  source path 和 `contentRevision`，因此每道题的 build、verify、失败报告和 cleanup 都有独立 source identity。Server 只在
  release `Committing` 时为该 Entry 生成并持久化新的 opaque challenge ID 与运行时 slug；重试始终复用该身份，随后才
  materialize 目录、保存 artifact reference 和写入发布时间。
- 任一 source、构建或真实验证出现非基础设施失败后，release 立即停止领取新的 entry，转入 `CleaningUp`；已经执行的 worker
  不被强杀，在当前阶段结束后按其 cleanup 协议清理 staging。清理完成后 release 才成为 `Failed`。内容失败只能修改 `catalog/`
  后创建新 digest；基础设施错误在同一 entry deadline 内重试，耗尽后同样清理并失败。两类失败都不进入 Agent 修复。
- 所有 entry 到达 `ReadyToCommit` 后，Server 执行唯一的 release-level commit：先在短事务中为每个 Entry 持久化运行时身份，
  再将 source materialize 到 staging 目录，并按 release ID、contentRevision 和已持久化身份幂等移动到最终目录；随后在同一
  数据库事务中写入新 Challenge、runtime artifact、编译后的 taxonomy mapping，并将 release 置为 `Ready`。文件操作必须发生在
  最终事务之前，事务提交前崩溃时 release 保持 `Committing`，恢复后按目标文件树 hash 与已保存身份继续，不公开也不重新构建
  artifact。`Committing` 的基础设施错误在 release deadline 内重试；文件树 hash 不匹配、非空目标目录等语义错误转入
  `CleaningUp`。
- staging artifact 使用不可变 OCI digest 或 Incus fingerprint，不需要跨多题的 Registry/Incus "final promotion"；只有 `Ready`
  后的 Challenge 记录会引用它们。成功前 Catalog API 不公开任何 challenge，失败时清理 staging artifact，只保留 release digest、
  逐题状态和结构化报告。
- `Failed` 是不可变历史记录。管理员再次安装时创建新的 CatalogRelease attempt；若上次只存在基础设施失败，可以使用同一
  source digest 重试，内容失败则必须使用新 digest。
- `Ready` 后，Catalog Release 只是一条不可变初始化谱系记录。普通作者继续走
  AuthoringSession -> GenerationWorkflow -> 真实验证 -> NeedsAuthorReview -> ChallengePublishing；Taxonomy Worker 以该
  release 安装的基线 snapshot 为起点处理后续增量。

#### Source 与运行时 revision

可复用 source 不能绑定环境生成的镜像指纹。需要明确拆分两类 revision：

| 概念 | 计算内容 | 用途 |
| --- | --- | --- |
| `contentRevision` | candidate source 的确定性文件树 hash：相对路径按字节排序，纳入普通文件的原始内容和可执行位。拒绝 symlink、设备文件、路径逃逸及 `id`、`source_slug`、`image`、`published_at` 等平台字段；YAML schema 校验独立执行，不对 YAML 做语义重写。 | Catalog Release source 校验、taxonomy mapping 和跨环境复用。 |
| `runtimeArtifact` | 目标环境的 Incus image fingerprint 或 OCI immutable reference，以及生成时间等发布信息 | Node/VK8s Environment 启动和运行时 artifact 回收。 |

taxonomy mapping 必须绑定 `challenge ID + contentRevision`，不再绑定包含 `image`、`published_at` 的完整已发布目录 revision。
平台 artifact 变化不会使 Skill/Tag 关系失效；题目源文件改变才会失效。Catalog `release.yaml` 只保存每题 source path 与
contentRevision；source taxonomy 也引用同一对值。安装时 Server 将它们编译为运行时的 `challenge ID + contentRevision`
mapping。`challenge.yaml` 保持 candidate 语义，不保存目标环境 image、发布时间或其他平台运行时字段。

已发布的作者题目可以导出为同一份 portable candidate source，再加入 `catalog/challenges/`；导出过程只保留 source 文件和
语义 metadata，重新计算 `contentRevision`，绝不导出原平台的 challenge ID、slug、artifact、发布时间、学习记录或数据库事实。
导入 release 后仍由目标平台在安装成功时生成新的运行时身份。

#### 初始化与测试

1. **平台基线由管理员准备**：Kubernetes CRD、PostgreSQL、Server PVC、外部 Registry 及其 CA、Incus endpoint、
   project/network/role identity、Node system-container base image、K8s runtime base image 都是可信平台依赖。它们由
   `scripts/incus/bootstrap.sh`、镜像构建目标和部署前置条件显式创建；Server、Controller、Worker 只做 readiness/preflight，
   不在启动时隐式修改这些资源。
2. **产品初始化安装 release**：平台基线就绪后，管理员安装一个正式 Catalog Release；`Ready` 才代表平台有可见题库。
   没有 release 的全新 Server 仍可合法启动并返回空 Catalog，便于运维诊断，但不是正常产品初始化完成态。
3. **E2E 使用同一入口**：`test/fixtures/catalog/` 保存极小的 fixture release（至少一个 Node、一个 VK8s 和一个 Catalog/UI
   fixture）。global setup 将它打包为 OCI artifact，通过正式管理员安装入口安装并等待 `Ready`，然后浏览器和 runtime E2E
   才开始。常规 E2E 不调用模型，也不依赖历史 PVC 或直接复制 `data/`。
4. **测试层次**：Catalog Release 安装有单独的真实集成测试，覆盖 source -> build -> staging -> verify -> release commit ->
   taxonomy 基线，以及 `Committing`/`CleaningUp` 中断后的幂等恢复；
   浏览器 E2E 只验证已安装题库上的 Catalog、环境、终端、检查点、Assistant 和 My Space；Agent 真实测试仍是独立、显式执行的
   测试层。

本次重构将当前 `data/challenges/cleanup-logs` 迁移为 `catalog/challenges/cleanup-logs` candidate source，将当前 taxonomy
内容迁移为 `catalog/taxonomy` source，并删除平台字段与环境特定 artifact 引用。删除直接复制 `data/` 的
`dev/kind-catalog.sh`、单题镜像旁路 `dev/incus-catalog.sh`、`cmd/catalog-seed`、`internal/catalogseed` 及其文档/Make 入口；
以 Catalog Release OCI 打包与安装入口替代它们。

### Kustomize

只保留两层：

```text
kustomization.yaml                  生产共同清单的唯一入口
  -> deploy/manifests/*.yaml
  -> deploy/crds/*.yaml

deploy/overlays/kind/kustomization.yaml
  -> ../../../                       只增加 Kind Registry 和开发 patch
```

- 删除 `deploy/base`、`deploy/runtime`、`deploy/crd` 和 `config/kustomization.yaml`，也删除它们的 Kustomization 文件。
- 根 Kustomization 直接列出清单、CRD 和 ConfigMap generator；不再通过“base 引用 runtime、runtime 引用资源、base 再引用
  config”的多层间接关系组装。
- `deploy/manifests` 只包含 namespace、RBAC、PostgreSQL、Server、Controller、Generate Worker、Taxonomy Worker、
  NetworkPolicy 和 PVC。`deploy/overlays/kind` 只包含 Registry、Kind patch 和其 README。
- `build/images` 保存 Dockerfile、entrypoint 和 runtime-init；`deploy` 不再携带镜像构建输入。

### 脚本与 Makefile

- 将 `dev/*.sh` 分类移入 `scripts/dev`、`scripts/kind`、`scripts/incus`；脚本均使用 `.sh` 扩展名。
- Makefile 只暴露稳定动作：`generate`、`verify-generated`、`build`、`images`、`deploy-kind`、`reset-kind`、`test-unit`、
  `catalog-package`、`catalog-install`、`test-e2e`。`catalog-install` 只调用 Server 的管理员 release 安装入口，不能复制
  data PVC 或直接发布 image。目标不再泄漏历史 worker 名称或内部目录。
- README 只列这些入口。操作前提、TLS/Registry 约束、Telepresence 和故障定位写入 `docs/operations`。

## 文档整理

`REVIEW/` 已删除。长期有效的结论只保留在 `docs`，并且每篇文档有唯一职责：

| 文档 | 唯一职责 |
| --- | --- |
| `docs/architecture/system-architecture.md` | 六个顶层组件与依赖图。 |
| `docs/architecture/code-layout.md` | 本文最终目录、分层和 import 规则。 |
| `docs/architecture/workflows.md` | GenerationWorkflow、TaxonomyWorkflow 与 Worker 协议。 |
| `docs/architecture/catalog-release.md` | Catalog source、content revision、安装状态、原子性和管理员边界。 |
| `docs/architecture/runtime-environments.md` | Node/VK8s/Incus/Registry runtime 边界。 |
| `docs/architecture/api-contracts.md` | HTTP、内部 Worker API、CRD 和生成规则。 |
| `docs/operations/deployment.md` | 生产部署、外部 Registry 和 CA 前提。 |
| `docs/operations/development.md` | 本地、Kind、Incus、Telepresence。 |
| `docs/operations/testing.md` | 单元、集成、E2E 分层与命令。 |
| `docs/product/*` | 用户体验、题目格式、taxonomy 和产品方向。 |

根 `README.md` 只保留项目定位、最短启动路径和 docs 导航。完成结构迁移后，旧文档应合并或删除，不能保留两份描述同一契约的文件。
顶层 `NEXT.md`、`REVIEW.md`、`TODO.md` 保留：前者记录下一阶段路线，第二者记录持续审查发现与待讨论风险，第三者记录
当前可执行工作。它们只链接到 `docs` 中的权威设计，不复制 API、运行时或部署契约。

## 实施提交序列

这是一套 13 个可独立审查的提交。每次迁移在同一提交内完成路径移动、全部 import 重写和旧路径删除；不保留 type alias、
re-export、旧 Kustomization 入口或兼容配置键。每次提交都必须保持可编译。

1. **`docs: define repository refactor and catalog release protocol`**：提交本设计，删除 `REVIEW/`。只变更文档，不改运行时行为。
2. **`refactor(api): centralize contracts and generated clients`**：迁移 OpenAPI、CRD Go 类型和生成物到 `api/`、
   `internal/transport/httpapi/generated`、`web/src/api/generated`；建立统一 `make generate` / `make verify-generated`。
3. **`refactor(domain): establish domain models and application ports`**：建立 `domain/*` 与 `application/*`，迁移纯模型、状态机和
   端口接口。包含 `GenerationWorkflow`、`CatalogRelease`、`CatalogReleaseEntry`、source kind 与 `contentRevision`，不迁移 SDK。
4. **`refactor(postgres): split durable repositories by domain`**：将 `internal/db` 拆到 `adapter/postgres`，按领域拆 schema 和
   repository；建立 Catalog Release、entry、运行时身份和提交状态持久化。开发环境直接重建 schema，不写兼容迁移。
5. **`refactor(adapters): isolate runtime and external clients`**：迁移 Kubernetes、Incus、OCI Registry、vcluster、OpenSandbox、
   LLM 和内部 Worker HTTP client 到 `adapter/*`；SDK 类型不得泄漏到领域或应用层。
6. **`refactor(server): separate application services from HTTP transport`**：抽出 authoring、generation、catalog、environment、
   assistant 等 application 用例；将公开/内部 HTTP、SSE、WebSocket、认证和错误映射迁到 `transport/httpapi`，使 `cmd/server`
   只调用 `bootstrap/server`。
7. **`refactor(generate-worker): consolidate generation execution`**：将 `generator`、`builder`、`publisher`、`verifier`、
   `generateworker` 收敛为 `worker/generate`。Worker 仅通过内部 API 领取、续租和报告阶段，绝不访问 PostgreSQL。
8. **`refactor(taxonomy-worker): isolate maintenance workflow`**：将 Mapper、reviewer pair、状态机和执行器收敛到
   `worker/taxonomy`，删除旧 taxonomy worker 入口及通用 work-item 残留。
9. **`refactor(controller): split environment reconcilers`**：拆为 `controller/nodeenvironment` 与
   `controller/vk8senvironment`，使 Controller 只依赖 environment domain 与 provider adapter。
10. **`feat(catalog): add portable release bundle tooling`**：实现 portable export、确定性 `contentRevision`、source/taxonomy
    校验和 OCI release bundle 打包。source 中拒绝平台字段；此提交不安装或公开 release。
11. **`feat(catalog): install verified releases atomically`**：实现管理员安装入口、Release/Entry 生命周期、artifact staging、
    `ReadyToCommit`、`Committing`、幂等恢复、`CleaningUp` 与原子可见性提交。迁移静态题目为 catalog source，删除
    `catalog-seed` 旁路，并让 E2E 通过正式安装入口准备题库。
12. **`refactor(ops): normalize configuration deployment and scripts`**：迁移配置、镜像输入、Kustomize 和开发脚本到最终目录，
    删除 `deploy/base`、`deploy/runtime`、旧 `dev/`、多层 Kustomization 和旧配置键。
13. **`test(docs): rebuild fixtures and finalize repository layout`**：迁移测试目录与长期文档，`todo.md` 改为 `TODO.md`，删除未引用
    生成物、重复文档、旧 Make target 和剩余旧路径。

### 每次提交的验证门槛

- 每次：`git diff --check`、相关 Go 单元测试和对应二进制构建必须通过。
- 第 2 次：`make generate`、`make verify-generated`；第 4、7、11 次：追加 PostgreSQL、Worker 或 Catalog 集成测试。
- 第 11 次：覆盖 release source -> build -> staging -> verify -> commit、`Committing`/`CleaningUp` 恢复和失败清理。
- 第 13 次：`go test -count=1 ./...`、`make lint`、`npm run build --prefix web`、`make verify-generated`、两套
  Kustomize 渲染及完整确定性 E2E。Live Agent 测试保持显式单独执行，不作为每次重构提交的默认门槛。

## 完成标准

- `internal/server`、`internal/db`、`internal/k8s`、`internal/incusprovider`、`internal/generator`、`internal/generateworker`、
  `internal/taxonomyworker`、`internal/builder`、`internal/publisher`、`internal/verifier` 等旧顶层目录不存在。
- 每个二进制的 `main.go` 不超过进程生命周期与 bootstrap 调用；没有业务逻辑、配置拼装或 SDK 初始化。
- `domain` 与 `application` 不 import 任何 adapter/transport/SDK；Worker 不访问 PostgreSQL；HTTP handler 不访问 SDK。
- 仓库只剩根生产 Kustomization 和 Kind overlay 两个入口，构建与部署资源彻底分离。
- `Catalog Release` 在空平台可从 immutable OCI bundle 自动安装；source 失败不调用 Agent，所有题通过后才原子公开基线
  challenge 与 taxonomy；E2E fixture 通过相同入口安装且不调用模型。
- `REVIEW/`、历史 data copy/catalog seed、重复文档、旧 Make target、旧脚本路径和未引用生成物全部删除。
- 所有既有用户可见行为与当前 Workflow/Environment 契约保持一致，但实现不保留兼容层。
