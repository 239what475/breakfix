# 系统架构

Breakfix 是一个以真实 Kubernetes 运行环境为基础的运维练习平台。题目是文件系统中的发布内容；用户环境和真实验证任务是 Kubernetes CRD。系统将面向用户的 Server、Kubernetes Controller 与模型调用的 Agent Worker 分为独立进程。

## 权威来源

- HTTP 路由、请求和响应：[`api/openapi.yaml`](../../api/openapi.yaml)
- Environment、VerifyTask 的 CRD 契约：[`internal/k8s/apis/breakfix/v1/`](../../internal/k8s/apis/breakfix/v1/)
- 题目目录和发布规则：[`internal/challenge/`](../../internal/challenge/)
- 运行时配置：[`config/breakfix.example.yaml`](../../config/breakfix.example.yaml)

本文件记录所有权和流程，不重复维护上述来源中的完整字段清单。

## 进程边界

```text
Browser
  | HTTP / WebSocket
  v
breakfix-server <----> PostgreSQL <----> breakfix-agent-worker
  | Kubernetes API                         | fenced internal HTTP
  v                                        v
breakfix-controller ----> namespaces, Pods, vclusters, verifier Jobs, CRD status
  ^
  | Environment / VerifyTask CRD
  +-----------------------------------------
```

`breakfix-server` 负责 HTTP、内嵌 Web UI、WebSocket 终端、认证、作者会话、做题助手、文件系统题库与 artifact 存储。它是领域数据的唯一写者，从 CRD status 幂等投影学习记录、尝试记录和终端使用记录；它只创建或更新 Environment `spec`，以及请求删除 CRD，绝不直接写 Environment `status`。

`breakfix-agent-worker` 从 PostgreSQL 领取有租约的 Agent Run，运行 Eino，并通过 Server 的受围栏保护内部 API 调用领域工具、读取工作区或提交候选。它只有 `agent_*` 表权限，没有 Kubernetes 凭据、Registry 凭据、OpenSandbox 生命周期密钥或领域表写权限。

`breakfix-controller` 只运行 controller-runtime manager 和环境清理循环。它读取 CRD `spec`，创建和清理 Kubernetes 资源，运行检查点，并写回 CRD `status`。它不访问 PostgreSQL、题目目录或 Server 持有的 artifact 文件。

Generator 是持久 Agent Session/Run：Server 创建 Run，Worker 在 Server 管理的 OpenSandbox 工作区中生成候选，随后由 Server 保存 artifact 并创建 `VerifyTask`。Controller 只为 VerifyTask 创建独立 verifier Job；它不共享 Server 文件系统。

## 数据所有权

| 数据 | 权威所有者 | 访问原则 |
| --- | --- | --- |
| 已发布题目 | `data_dir/challenges/` | Server 读取并在发布时原子写入；不是数据库或 CRD 的副本。 |
| 作者 artifact 与提交归档 | Server 数据目录 | Server 保存和提升；Generator、VerifyTask 通过内部 HTTP 交接。 |
| 账户、作者会话、学习和终端记录 | PostgreSQL | Server 写领域数据；Worker 仅按独立权限读写 `agent_*` Runtime 数据。 |
| Generator workspace | Server-owned PVC + PostgreSQL record | Server 创建、清理和围栏；OpenSandbox 只以 BYO 模式挂载。 |
| 用户运行环境 | ContainerEnvironment / VClusterEnvironment CRD | Server 提供期望 spec，Controller 管理资源和 status。 |
| 生成与真实验证 | Agent Run / VerifyTask CRD | Worker 生成候选；Controller 调和 VerifyTask，Server 将成功结果关联到作者会话。 |

Environment spec 在创建时包含不可变执行快照：题目引用、manifest revision、镜像、runtime 和预期 checkpoint ID。这样 Controller 无须读取题目目录，也不会因为发布后的题目文件改变而改变已启动环境的执行语义。

## 请求与恢复

启动挑战时，Server 从发布题目目录读取题目，创建对应的 Environment CRD，并等待 Controller 将其调和到 Ready。终端通过 Server 代理到 workspace Pod；连接活动作为 Environment spec 的 `activityAt` 输入。Controller 基于该输入计算租约、Draining 与清理，最终通过 CRD status 表达完成、失败和销毁。

Server 重启不会停止 Controller 调和。Controller 重启后会从已有 CRD 继续创建资源、检查检查点或清理。Server 恢复后会重新投影现有 CRD status 到 PostgreSQL，并删除已完成投影的 Destroyed/Failed CRD。真实恢复行为由 [`test/e2e/server-recovery.spec.ts`](../../test/e2e/server-recovery.spec.ts) 验证。

## 当前部署假设

运行时包将 Server、Controller、Agent Worker 和 PostgreSQL 分别部署；Server 的 RWO data PVC 持有 catalog 与 artifact，因此 Server 使用 `Recreate` 策略。Controller 不共享该目录，Worker 不挂载任何业务卷。

多 Server 实例仍需要可共享的题目/artifact 存储与可协调的文件提升协议；PostgreSQL 本身不解决该文件系统边界。
