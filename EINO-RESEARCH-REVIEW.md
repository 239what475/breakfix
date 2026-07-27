# Agent Runtime 迁移设计审查

本文只记录实现过程中确认的设计缺口及已采用的调整；它不是兼容层，也不改变 `EINO-RESEARCH.md` 的总体边界。

状态说明：`[已讨论并验证]` 表示已有设计结论且已通过对应验证；`[已讨论并决策]` 表示设计已确定；`[已移除]` 或 `[已由新方案替代]` 表示历史问题不再是当前实现边界；`[待外部验证]` 表示不需要继续设计讨论，但仍需在合适环境完成实际验证。

当前没有待讨论的产品或架构决策，也没有待验证项。文末只记录不阻塞当前架构的后续安全硬化。

## [已讨论并验证] 2026-07-26：运行镜像构建上下文混入本地数据和依赖目录

首次构建新的 Server 镜像时，仓库没有 `.dockerignore`。Docker 因此会把本地 `node_modules`、测试结果、data、二进制和
未追踪运行时配置一并发送给 builder；当前工作区的上下文约为 626 MB。这既会拖慢构建，也会让本地 artifact 或实例配置意外
进入构建上下文，不符合运行镜像只由受版本控制源码构成的边界。

调整：增加仓库级 `.dockerignore`，明确排除运行数据、构建产物、Node 依赖、测试输出、实例配置和本地 kubeconfig。新的
Server 构建上下文已降至约 17 KB；前端依赖仍只在 Docker build stage 中由 lockfile 安装。该调整不引入新的运行时数据源。

## [已讨论并决策，后续硬化] 2026-07-26：独立 verifier 仍需要 rootful BuildKit 权限

Verifier 必须与 Agent Worker 使用不同的 Job 模板和最小化的 ServiceAccount，但当前实际构建实现通过
rootful `buildkitd` 在 Job 内构建并推送镜像。该模式需要 privileged container；把 verifier 简单改为
non-privileged 会使真实镜像构建失败，不能作为安全改进提交。

调整：首轮 verifier 保留 explicit `privileged: true`，但只使用 `breakfix-verifier` ServiceAccount。该
ServiceAccount 仅可读取/更新 VerifyTask status、创建和清理本 VerifyTask 的 Environment，以及读取目标
Pod 并使用 `pods/exec` 执行 answer/checkpoints；它没有 Agent Worker 的模型、PostgreSQL、Server 领域写入或
OpenSandbox 凭据。若要去除特权，必须先单独完成 rootless BuildKit 或远程 BuildKit 的真实 Kubernetes POC，
不能把它和 Agent Runtime 迁移混为一谈。

## [已讨论并决策] 2026-07-26：VerifyTask source ref 的最终语义

`VerifyTask.spec.source.ref` 是提交它的 Generator Agent Run ID。CRD 不保留没有信息量的固定 `source.kind`。
Server 只会在同一 Run 的确定 submission 已经持久化后 create-or-get 对应 VerifyTask；Server watcher 反向校验
submission、VerifyTask 和当前 Generator Run 的三方绑定。旧 Generation CRD、Reconciler、镜像和临时 ref 语义已删除。

这个边界使 Controller 只验证不可变 artifact，不知道或不需要知道模型会话；Server 则能把 VerifyTask 的 artifact
失败明确交给同一 Generator Session 的下一 Run。

## [已讨论并决策] 2026-07-26：Assistant Run 缺少可恢复的工作区输入

原设计的 `agent_runs` 列出了 `input_revision`，但没有记录一次 Assistant 请求中的 `current_window` 与 `open_windows`。这两个值决定
`get_terminal_scrollback` 可以读取的 tmux 窗口。若 Worker 在首次执行前或执行中退出，只依靠环境 UID 无法无损恢复这个工具边界，只能错误地退化为默认窗口。

调整：在 `agent_runs` 增加不可变的 `input_json`，仅保存该 Run 的结构化输入。Assistant 当前保存：

```json
{"current_window":"shell-1","open_windows":["shell-1","shell-2"]}
```

它不保存终端输出、工具结果、模型 prompt、模型 reasoning、Eino checkpoint 或逐 token event；这些仍分别遵守原设计的数据所有权和瞬时流边界。

同一问题也影响 Assistant 已有的 evidence 标签。通用 `agent_messages` 因此增加 `metadata_json`，仅保存小型、用户可见的结构化注解。Assistant 最终消息写入实际调用过的只读工具标签，不写工具参数、工具内容或模型内部状态。

## [已讨论并决策] 2026-07-26：Eino 内建流式重试不能满足废弃 partial draft 的语义

Eino `v0.9.13` 的 `ModelRetryConfig` 在流式调用中会并行消费一份完整流来决定是否重试，同时让另一份流向下游发送 chunk。重试判定发生在 stream 结束后，因此浏览器可能已经看到了随后会被废弃的文字。

调整：Assistant 不使用 Eino 内建流式重试。一次完整 Agent 调用遇到明确的传输错误时，最多重新执行三次；每次重新执行前通过瞬时 Server event 发布 `reset`，浏览器清空旧草稿。Assistant 没有写工具，重复该调用不会重复领域副作用。最终消息仍只在成功后写入 `agent_messages`，且只写一次。

## [已讨论并决策] 2026-07-26：OpenSandbox Go SDK 的模块路径与 Worker 授权边界

`opensandbox-group/OpenSandbox` 的 SDK tag `sdks/sandbox/go/v1.0.5` 实际声明的 Go module 仍为
`github.com/alibaba/OpenSandbox/sdks/sandbox/go`。以仓库新路径执行 `go get` 会因 module path 不匹配失败；接入时必须使用该声明路径并固定 `v1.0.5`，不能通过 `replace` 伪造第二份 SDK。

同时，SDK 的 `ConnectSandbox` 会以 lifecycle API key 调用 `GetEndpoint`，而 endpoint headers 是按 Sandbox 返回的访问材料，并不携带
Run attempt、过期时间或撤销接口。它们不能满足 Worker 失去租约后立即失效的 fencing 要求，也不能作为全局 lifecycle key 的安全替代物交给 Worker。

调整：Server 是唯一 OpenSandbox SDK/lifecycle credential 持有者。Generator Worker 的 files、exec 和 archive 操作均通过受内部 API 保护的
Server proxy；每个请求由 Server 使用 `run_id`、`attempt` 和 `lease_owner` 校验当前租约后才转发到绑定的 Sandbox。这个选择使用
`EINO-RESEARCH.md` 已规定的“没有官方可撤销 scoped credential 时使用 Server 代理”分支，不建立双 backend 或向 Worker 注入 OpenSandbox key。

## [已由 Server-owned BYO PVC 方案替代] 2026-07-26：OpenSandbox Helm 0.2.0 与 Server 0.2.2 的 POC 阻断项

`helm/opensandbox/0.2.0` 的 `Chart.lock` 声明 controller dependency 为 `0.1.0`，而同一 tag 的 `Chart.yaml` 声明为 `0.2.0`。
因此 `helm dependency build` 直接失败，必须在 POC worktree 中以同 tag 的本地 dependency 运行 `helm dependency update` 才能渲染。该
tag 的默认 Server 镜像还是 `v0.1.13`，与 SDK `v1.0.5` 的源码版本不对应；POC 显式固定为 Controller `v0.2.0`、Server `v0.2.2`、
`execd v1.0.21` 和 egress `v1.1.4`，不能直接把 chart default 当作已验证组合。

真实 Kubernetes POC 中，Server `v0.2.2` 成功创建 `BatchSandbox` 和 server-managed PVC，但在给 PVC 写 ownerReference 时记录
`Got an unexpected keyword argument '_content_type' to method patch_namespaced_persistent_volume_claim`。OpenSandbox 自身也明确说明：
controller 驱动的 TTL 删除依赖该 ownerReference；失败后 lifecycle `DELETE` 的 label sweep 只能作为回退，因此 TTL 路径可能遗留 PVC。

`helm/opensandbox/0.2.0` 还实际嵌入旧 `opensandbox-server` chart `0.1.0`。该模板对 PVC 仅授予 `create/get`，缺少
`list/delete/patch`。所以 POC 中显式 `DELETE /sandboxes/{id}` 虽返回 `204` 并删除 `BatchSandbox`，同样无法列出或删除带有
`opensandbox.io/volume-managed-by=server` 标签的 PVC。SDK `v1.0.5` 对应的上游源码 commit 已在 chart 模板中补全这三个权限，
但尚未发布与该源码匹配的 Helm tag。即使补全 RBAC，`v0.2.2` 的 `_content_type` 调用仍会在本地 Kubernetes Python client `35.0.0`
上先于 API 请求失败，ownerReference 依然不能创建。

调整：不能使用 `v0.2.2` 作为固定生产 provider，也不能用 `latest` 或本地 patch 掩盖该缺陷。上游若发布修复版本，必须以固定
Server/Chart revision 重跑 POC；若在本阶段没有官方修复，则需要先把 workspace PVC 的权威生命周期明确改由 Breakfix Server 管理，
并用独立设计说明替换“依赖 OpenSandbox TTL 回收 PVC”的前提，不能在实现中悄悄增加扫尾逻辑。在这两者之一完成前，不开始
`OpenSandboxBackend` 或 Generator 迁移。

## [已讨论并验证] 2026-07-26：以 Server-owned BYO PVC 解除 OpenSandbox workspace 阻断

重新核对官方 releases 后，最新正式 OpenSandbox Server 仍是 `server/v0.2.2`；没有可用的发布版本修复其 server-managed PVC
ownerReference 路径。不能使用未发布的 upstream main、`latest` 或本地 patch 来假装该路径可用。

随后在 Kind 的官方组合 Controller `v0.2.0`、Server `v0.2.2`、`execd v1.0.21`、egress `v1.1.4` 上完成了新的真实
BYO PVC POC。Breakfix 预先创建 `breakfix-byo-workspace-poc`，调用官方 SDK 时传入
`claimName=breakfix-byo-workspace-poc` 和 `createIfNotExists=false`。实际结果如下：

- Sandbox create、文件上传、streaming command 均成功，client 输出 `CREATE_FILES_AND_STREAM=PASS`。
- 重启 OpenSandbox Server 后，以同一个 opaque Sandbox ID 重连并下载 marker，随后官方 delete 成功，输出
  `RECONNECT_AND_DELETE=PASS`。
- OpenSandbox delete 后 PVC 仍是 `Bound`；由 POC 的平台侧 `kubectl delete pvc` 回收，证实 provider 没有取得 PVC 所有权。
- `default-deny` NetworkPolicy 的 sandbox command 以 DNS 失败退出，输出 `EGRESS_DENY=PASS`。
- 无卷、75 秒 TTL 的 Sandbox 到期后对应 `BatchSandbox` 和 Pod 均不存在，证实 provider 的 Sandbox TTL/delete 路径可用。官方 Server 对 timeout 的最小值为 60 秒，POC 据此使用 75 秒。

调整：workspace PVC 生命周期正式收归 Breakfix Server，并在 `EINO-RESEARCH.md` 中补全记录、创建、未知 create 响应、删除和权限
边界。Server 使用 `generator_workspaces` 记录自己创建的确定 PVC 名称，OpenSandbox 永远以 BYO 模式挂载；Server cleanup loop
负责删除 PVC。这个调整没有建立第二个 provider、没有使用未发布 provider，也没有实现 Sandbox metadata adopt。

## [已讨论并决策] 2026-07-26：Taxonomy reviewer 的持久化边界

`EINO-RESEARCH.md` 原先将 Curriculum Reviewer 和 SRE Reviewer 描述为两个可并行的独立 Agent Run；但这会与既有
Taxonomy 委员会约束冲突：一名 reviewer 的结论不能在另一名 reviewer 因模型或协议错误失败时成为可恢复的半成品。若把两份
结论分别写入领域表，下一次调度必须维护并恢复不对称的 reviewer 状态，既扩大 WorkItem 状态机，也让一次不完整审查看起来像
领域事实。

调整：Mapper 保持无 Session 的 `taxonomy-mapper` Run。两名 reviewer 由一个 `taxonomy-review` Run 负责，在同一 Worker
attempt 内并行执行两个独立的 Eino typed-result 调用；只有两者都正常返回并通过严格领域校验时，Server 才在一个事务中持久化
完整 review pair、完成该 Run 并推进 Round。任一调用失败时，review pair 不写入领域表；该 Run 在模型层最多完成三次传输重试后
直接进入终态，由 Server 对当前 Round 计入一次 Taxonomy 技术失败预算并安排下一 Run。这样没有供应商 session、没有 partial review
事实，也不会在运行中的调用被十次预算提前中断。该调整不改变 Mapper 与两名 reviewer 的职责分工或并行模型调用，只把 reviewer
pair 作为不可分割的持久化边界。

## [已讨论并验证] 2026-07-26：Go OpenAPI 生成物的可复现性缺口

`api/cfg.yaml` 记录了 `oapi-codegen v2.7.1`，但此前 Makefile 既没有固定 Go 生成命令，也没有校验
`internal/api/server.gen.go`。前端类型有单独生成步骤，导致 OpenAPI 的两类生成物可以独立漂移。

调整：`make generate-api` 现在固定使用 `oapi-codegen v2.7.1`，同时生成 Go 路由模型和前端类型；
`make verify-api-generated` 在临时目录重建两者并递归比较。本次重建将历史遗留的 required 字段、枚举类型和嵌入
规范差异一并同步到 Server Handler 与测试；`authoring_turn_active` 则是本次 OpenAPI 增加的字段。之后不再接受未解释的
生成漂移。

## [已移除] 2026-07-26：运行镜像继承 Go module proxy

旧 Dockerfile 在镜像构建阶段执行 `go mod download`，所以曾尝试把构建机的 `GOPROXY` 传给 Docker。这会让发布镜像的
构建路径依赖开发机的网络设置，也把源码编译、依赖下载和运行镜像封装混在同一个步骤中。

调整：Go 与前端构建在 Docker 之外完成，目标平台二进制统一落在 `bin/release/<os>-<arch>/`。三个运行镜像只以该
目录为 Docker context 并复制对应二进制，不再包含 Go、Node、`GOPROXY` 或宿主机网络配置。Controller 的 `vcluster`
仍由独立 stage 按固定版本下载并校验，这是镜像自身明确的供应链输入，不是构建机本地文件降级路径。

## [已讨论并验证] 2026-07-26：本地 E2E 不覆盖 Controller Dockerfile 的 GitHub 下载路径

Controller Dockerfile 会在构建时从 GitHub 下载并校验 `vcluster v0.35.1`。此前网络环境对该 release 下载严重限速，
无法在合理时间内完成镜像构建；真实 Kubernetes E2E 因而临时使用当前源码编译的 Controller 二进制和本机已校验的
同版本 `vcluster` 二进制组成的 Kind 专用镜像。

调整：临时镜像只用于验证 Controller、vcluster 和 VerifyTask 的运行时行为，不进入 Git，也不替代正式 Dockerfile
的供应链验证。随后正式 Dockerfile 已在可访问 GitHub Releases 的环境中完成实际构建，下载的 `vcluster-linux-amd64`
通过固定 SHA256 校验。Controller 镜像只包含 distroless 基础层、该已校验的 CLI 和发布二进制。

## [已讨论并验证] 2026-07-26：Taxonomy 首次导入暴露两个 PostgreSQL 迁移遗漏

空数据库启动时，预置 challenge 没有 current taxonomy snapshot。`EnqueueUnmapped` 原先把空 Snapshot 传给完整
catalog 校验，因此第一道题无法进入 Mapper 队列。修正后，缺少 snapshot 时所有已发布目录都会通过既有的 durable
mapping WorkItem 入队，首个 Mapper 以空 base revision 建立 taxonomy；不会绕过委员会或直接写入 mapping。

真实 PostgreSQL 验证还发现 migration 5 将 legacy taxonomy JSON 列转为 JSONB 后保留了 `NOT NULL` 约束，但
Mapper 前的 candidate 和 reviewer 结论在语义上必须为空。新增 migration 9 解除这三列的约束，兼容已经执行过
migration 5 的数据库。该调整使数据库模式与既定的 WorkItem 状态机一致，不改变 taxonomy 或 Agent Runtime 设计。

真实 Mapper 调用还表明原 system prompt 仅以“完整 ChangeSet”描述工具参数，未显式列出 strict schema 的四个必填数组和
当前 challenge mapping 的提交义务。补充 prompt 后，模型仍必须满足同一严格解码和领域校验；没有加入 Markdown 解析、
字段默认值或近似结果兼容。

## [已讨论并验证] 2026-07-26：Verifier 清理要求 Registry 支持 manifest DELETE

真实 Kind E2E 的已有失败 VerifyTask 在 cleanup 阶段持续收到 Registry HTTP 405。原因是本地 `registry:2` 没有开启
`REGISTRY_STORAGE_DELETE_ENABLED=true`，不是 Controller 将确定性的 artifact failure 误判成成功。设计已经要求 terminal
failed task 删除临时镜像，因此把 405 忽略、移除 finalizer 或保留失败镜像都不符合目标语义。

调整：本地 `make dev-registry` 明确启用 manifest deletion；发现旧配置时保留 `/var/lib/registry` 数据卷并重建 registry。
运行时部署文档将 Registry V2 manifest DELETE 列为前置条件。Controller 的 cleanup 仍在删除失败镜像成功后才完成；没有引入
删除失败兜底或弱化 finalizer。

## [已讨论并决策] 2026-07-26：Generator Run 拥有 OpenSandbox 生命周期

Generator workspace 不再按长期 Generator Session 复用，也不再配置独立 `workspace_timeout`。Server 使用 SDK 的
`ManualCleanup=true` 创建每个 Generator Run 的 Sandbox；同一 Run 的 Worker technical retry 复用该 workspace，Run 提交 immutable
candidate、失败、取消或超时时由 Server 删除 Sandbox 与对应 BYO PVC。VerifyTask artifact failure 在同一 Generator Session 创建下一
Run，但新 Run 创建新的 Sandbox/PVC，并从失败 artifact 初始化。

这将正常生命周期完全绑定到明确的 Run 终态，不依赖 OpenSandbox provider TTL。create 成功但响应在持久化 Sandbox ID 前丢失时，
Server 只删除已知 PVC，不查询或 adopt 未记录 Sandbox；该外部响应歧义保持显式而非引入 metadata 扫描协议。

## 后续硬化

- 若未来将特权 verifier 视为安全硬化目标，再单独完成 rootless BuildKit 或远程 BuildKit 的真实 Kubernetes POC；在此之前维持当前已决策的最小权限 rootful BuildKit 方案。
