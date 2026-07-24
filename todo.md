# 代码库整理清单

当前单题挑战的产品闭环已经完成。下一阶段扩展题库前，先完成下面的结构整理；每项应单独提交，并保持行为与现有真实 e2e 验收一致。文档归并依赖最终的代码、配置和进程边界，因此安排在最后；配置模板和部署命令等可执行说明则随其对应改动同步更新。

## 1. [x] 统一运行时配置与工具入口

- 新建 `config/`，只存放 Breakfix 运行时配置：跟踪 `config/breakfix.example.yaml`，忽略本地 `config/breakfix.yaml` 与 `config/breakfix.local.yaml`。
- 更新 Gateway 默认配置路径、Makefile、部署文档和真实 e2e，使它们统一使用新的配置位置；远端运行配置仅在服务器上维护，不参与仓库部署。
- 修正配置模板，使所有键都与 `internal/config.Config` 的 YAML tag 对应，并为模板加载增加测试。
- 让配置加载拒绝未知 YAML 字段，避免配置拼写错误或模板陈旧时被静默忽略。
- 保持 `api/cfg.yaml` 位于 `api/`、Vite/TypeScript 配置位于 `frontend/`、部署 manifests 位于 `deploy/`；它们不是运行时配置，不迁入 `config/`。
- 删除未引用且与 Makefile 部署逻辑重复的 `hack/deploy.sh`，不将其改名迁入其他脚本目录。
- 将 `pkg/vclustercli` 迁入 `internal/vclustercli` 并更新 controller import；它没有外部消费者，不应承诺为公共 Go API。

验收：本地启动、部署 target、配置模板加载与 controller 的 vcluster 生命周期测试均通过；`hack/` 和 `pkg/` 不再存在。

## 2. [x] 整理 Node 项目与 Playwright 工具链

`frontend/` 已经是独立的 Vue 项目，保留其 `package.json`、lockfile、Vite/TypeScript 配置和 `node_modules`。根目录 Node 项目只服务于全系统 Playwright 测试，不能与前端实现混为一谈。

- 将根目录 `package.json`、`package-lock.json` 和 `playwright.config.ts` 迁入 `test/`，令 `test/` 成为独立的 `breakfix-e2e` Node 项目。
- Playwright 配置继续指向 `test/e2e/`，测试产物改为 `test/results/` 与 `test/report/`，并更新 `.gitignore`。
- 将 Makefile 和 CI 改为通过 `npm --prefix test` 安装、运行 Playwright；真实恢复测试仍从仓库根目录定位 Makefile、配置和集群资源，不依赖 Node 执行时的当前目录。
- 删除根目录 Node package 中未使用的历史 UI 依赖；不要将 Playwright 放入 `frontend/`，因为它测试 Gateway、浏览器和 Kubernetes 环境组成的完整系统。
- 删除根目录 `node_modules` 与遗留测试报告等忽略产物。

验收：根目录不再有 Node package、lockfile、Playwright 配置或 Node 依赖目录；前端构建和全部 Playwright 命令保持可运行。

## 3. [x] 清理空目录与遗留路径

- 删除无内容且无用途的目录：`cmd/review-once`、`frontend/src/composables`、`test/e2e/helpers`。
- `third_party/` 已删除；确认 `.agents` 和 `.codex` 不被本地工具使用后删除。
- 将基础、Kubernetes 基础与 generator 镜像构建上下文从根目录 `images/` 迁入 `deploy/images/`，与 CRD 和 RBAC 一起作为部署资产维护。
- 清理所有已忽略的临时测试、构建和报告目录；不删除 `data/challenges/` 中的受版本控制题目。

验收：`git status --ignored` 中只保留预期的本地配置、构建产物和运行数据；仓库中不存在空的历史目录。

## 4. [x] CRD 契约单一来源与生成闭环

当前 Kubernetes CRD Go 类型与 `deploy/crd/*.yaml` 分别手写，字段、枚举和校验规则会发生漂移。将 Go 类型作为唯一来源，生成部署清单和 DeepCopy 代码。

- 将 `apis/breakfix/v1/` 迁入 `internal/k8s/apis/breakfix/v1/`；它只服务本仓库内的 Gateway、controller 与 generator，不承诺外部 Go API。
- 保留类型定义、JSON tag 与 Kubebuilder 校验标记为手写源文件；将 `DeepCopy` 实现改为 `zz_generated.deepcopy.go`。
- 固定与 Go 1.26、Kubernetes 0.36 匹配的 `controller-gen` 版本，使用 Makefile 从类型定义生成 `zz_generated.deepcopy.go` 与 `deploy/crd/breakfix.dev_*.yaml`。
- `deploy/crd/` 是应提交的部署契约，禁止手改；它不是可忽略的构建产物。
- 为 phase 枚举、必填字段和 status 子资源补齐或保留 Kubebuilder 标记，生成结果必须覆盖当前 ContainerEnvironment、VClusterEnvironment、Generation 与 VerifyTask 契约。
- 新增 `make generate-crd` 与 `make verify-crd-generated`；后者生成后检查 Git diff，CI 必须执行它。
- 在 Kind 集群 apply 新生成的 CRD，再执行真实 container、vcluster 与 VerifyTask 流程，验证 CRD 创建、status 写入、检查点和清理行为。

验收：CRD 字段只在 Go 类型中定义；DeepCopy 和 CRD YAML 均可重复生成且无 diff；CI 能阻止未生成的变更；真实集群流程保持通过。

## 5. [x] 建立 Server/Controller 数据所有权与可执行边界

当前 `cmd/gateway` 同时运行 HTTP/WebSocket 服务和 controller-runtime manager。目标不是把相同的数据目录和 SQLite 暴露给两个进程，而是明确数据所有权后拆为 `breakfix-server` 与 `breakfix-controller`。

- 将 `cmd/gateway` 与 `internal/gateway` 重命名为 `cmd/server` 与 `internal/server`；`breakfix-server` 负责 HTTP、Web UI、WebSocket、认证、作者会话、助手、题目文件、artifact 存储和数据库。
- 新建 `cmd/controller`，仅启动 controller-runtime manager 与环境清理循环；同时将它加入 CI/release 的构建矩阵。
- Controller 只读取 CRD `spec`、创建和清理 Kubernetes 资源、执行检查点并写 CRD `status`。它不得读取 `data_dir/challenges`、artifact 文件或数据库。
- Server 创建 Environment CRD 时，从文件系统权威题目生成不可变执行快照：题目引用、题目 revision、镜像、runtime 与预期 checkpoint ID。题目正文、解答、提示和脚本不进入 CRD；题目仍是文件系统目录，而不是 CRD。
- Controller 根据 Environment spec 中的预期 checkpoint ID 校验 Pod 内 `/checks/checkpoints.sh --json` 输出，并将检查结果、Ready、完成、失败和销毁原因写入 status。
- Server 是数据库唯一写者。新增幂等的 CRD 状态投影器：根据 `ReadyAt`、检查点完成和最终状态写入 attempt、完成记录与学习视图；用户显式 Stop/Reset 等意图仍由 Server 直接记录。
- Server 只能写 CRD spec/删除请求，不能直接写 Environment status。终端活动和租约续期必须成为 spec 中的期望输入，由 Controller 计算并写入 status。
- 保持 generator 与 artifact 的内部 HTTP 交接；将 `GATEWAY_INTERNAL_URL`、`GatewayURL` 等改为 `SERVER_INTERNAL_URL`、`ServerURL`，Controller 只持有这个内部服务地址，不读取其文件系统。
- 初期 Server 与 Controller 可以同主机、同配置文件运行，但通过独立进程和唯一数据所有权协作；未来跨 Pod 部署前再单独引入集中式数据库、共享题目存储和稳定的内部 Service。

验收：Server 重启不停止 Controller 调和；Controller 重启后继续处理已有 CRD；Controller 代码不依赖数据库或 challenge 目录；本地组合启动、真实环境和恢复 e2e 均通过。

## 6. Server 文件职责拆分

完成 Server/Controller 边界后，`internal/server/handlers.go` 仍会混合认证、题库、挑战 HTTP 接口、环境生命周期和内部 artifact 下载。保持公开路由不变，仅按职责拆分文件：

- `auth_handlers.go`：注册、登录和当前用户读取。
- `catalog_handlers.go`：题库列表、题目内容、检查点状态和 API 映射。
- `challenge_handlers.go`：开始、重置、停止挑战与终端路由入口。
- `environment_service.go`：环境查找、创建、恢复、销毁、状态读取和 TTL 计算。
- `verify_artifacts.go`：仅供 generator/VerifyTask 使用的内部 artifact 下载接口。
- 保留 `handlers.go` 或重命名为 `handler.go`，只放 `Handler`、共享领域类型和构造函数。

验收：路由、鉴权、WebSocket 和环境恢复行为不变；现有 Server 单元测试与真实恢复 e2e 通过。

## 7. Generator 文件职责拆分

`internal/generator/generator.go` 同时承担 agent 编排、静态校验、流处理、归档和上传。保持 `generator` 包和工作流语义不变，拆分为：

- `workflow.go`：`Generator.Run`、生成与 judge 阶段、修复循环和上传调度。
- `validation.go`：manifest 与运行时语义校验，以及对应纯函数测试。
- `archive.go`：artifact 归档和文件系统辅助函数。
- `agent_stream.go`：agent event 消费、静默检测和流日志。
- `verifytask.go` 继续独立，负责真实发布环境验证。

验收：生成 prompt、judge 的严格 PASS/FAIL 契约、VerifyTask 交接格式和真实验证流程均不改变。

## 8. Playwright 按产品场景拆分

`test/e2e/workspace.spec.ts` 当前混合静态页面、个人空间、容器工作台、vcluster 和作者流程。按场景拆分，并复用已有 `live-helpers.ts`：

- `catalog.spec.ts`：未登录目录、筛选、排序、窄屏布局。
- `my-space.spec.ts`：个人空间、学习记录、作者入口与响应式导航。
- `workspace.live.spec.ts`：container 挑战的真实终端、检查点、重置与恢复。
- `vcluster.live.spec.ts`：vcluster 挑战的真实环境与检查点。
- `authoring.live.spec.ts`：作者讨论、生成、真实验证和发布。
- `server-recovery.spec.ts` 继续独立，保留其显式 Server 终止与重启行为；补充 Controller 重启后继续回收已有环境的真实 e2e。

验收：普通浏览器测试默认可运行；真实环境测试仍由显式环境变量启用；Server 恢复和 Controller 恢复 e2e 均保持可运行。

## 9. 统一 API 契约类型

`api/openapi.yaml` 已是服务端路由和模型的生成源，但 `frontend/src/api/types.ts` 仍人工维护，存在漂移风险。

- 为前端引入从 `api/openapi.yaml` 生成 TypeScript 类型的固定命令。
- 将生成类型放入明确的 generated 位置；`frontend/src/api/client.ts` 继续只承担请求与 SSE 封装。
- 移除手写的重复 API 模型，保留只属于前端状态的类型。
- 在构建或 CI 中验证 OpenAPI 生成后没有未提交变更。

验收：前后端都以同一 OpenAPI 文件为契约；前端构建、服务端测试和 Playwright 测试通过。

## 10. 最后统一文档与设计资源

在前述代码、配置和进程边界稳定后，再处理长期文档的目录和引用。配置模板、部署命令等需要与可执行行为保持一致的说明，应在第 1 项实施时同步更新，不能延后。

- 建立清晰的文档层级：`docs/architecture/`、`docs/product/`、`docs/content/`、`docs/operations/` 和 `docs/assets/`。
- 将根目录 `new-design.md` 迁入 `docs/architecture/`，作为运行时环境设计说明；将 `iximiuz/next-steps.md` 迁入 `docs/product/`，不再保留以外部产品命名的目录。
- 将 `iximiuz/` 中的设计图片迁入 `docs/assets/` 并使用描述性文件名，更新所有 Markdown 链接。
- 将现有 `DESIGN.md`、`API_DESIGN.md`、`AGENT_WORKFLOW.md`、`CHALLENGE_DESIGN.md` 与 `DEPLOY.md` 分别归入对应层级。
- 新增 `docs/README.md`，列出每份文档的目的和权威来源：HTTP 契约以 `api/openapi.yaml` 为准，CRD 字段以 `internal/k8s/apis/breakfix/v1/` 为准，challenge 文件契约以 `internal/challenge/` 为准。
- 文档只记录架构决策、流程与不变量，不重复维护机器可验证的完整字段清单。
- 保留根目录 `NEXT.md` 和 `todo.md`，它们是当前阶段入口与执行清单，不属于长期设计文档。
- 更新交叉引用，保证仓库内不存在指向旧 `iximiuz/` 或根目录设计稿的链接。

验收：所有文档链接可用；目录名称反映内容而不是参考对象；文档准确反映最终实现，不改变应用行为。

## 不在本轮处理

- 初期不将 Server 与 Controller 部署到不同 Pod，也不为此提前引入共享卷、对象存储或集中式数据库。
- 不为了文件行数拆分 `controller`、`db`、`challenge` 或前端 feature 目录；它们当前领域边界清晰。
- 不改变内部 artifact/VerifyTask 协议、检查点契约、文件系统题库权威来源或环境生命周期语义。
- 不在代码整理期间新增学习路径、推荐、排行榜、讨论区或其他产品功能。
