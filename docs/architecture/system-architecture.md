# 系统架构

Breakfix 是一个以真实 Kubernetes 运行环境为基础的运维练习平台。题目是文件系统中的发布内容；用户环境、生成任务和真实验证任务是 Kubernetes CRD。系统刻意将面向用户的 Server 与 Kubernetes 调和的 Controller 分为独立进程。

## 权威来源

- HTTP 路由、请求和响应：[`api/openapi.yaml`](../../api/openapi.yaml)
- Environment、Generation、VerifyTask 的 CRD 契约：[`internal/k8s/apis/breakfix/v1/`](../../internal/k8s/apis/breakfix/v1/)
- 题目目录和发布规则：[`internal/challenge/`](../../internal/challenge/)
- 运行时配置：[`config/breakfix.example.yaml`](../../config/breakfix.example.yaml)

本文件记录所有权和流程，不重复维护上述来源中的完整字段清单。

## 进程边界

```text
Browser
  | HTTP / WebSocket
  v
breakfix-server
  | reads and writes Environment spec; reads status
  | Kubernetes API
  v
breakfix-controller ----> namespaces, Pods, vclusters, Jobs, CRD status
  |
  +----> Generator Job / VerifyTask Job
```

`breakfix-server` 负责 HTTP、内嵌 Web UI、WebSocket 终端、认证、作者会话、做题助手、文件系统题库与 artifact 存储，并且是 SQLite 的唯一写者。它从 CRD status 幂等投影学习记录、尝试记录和终端使用记录；它只创建或更新 Environment `spec`，以及请求删除 CRD，绝不直接写 Environment `status`。

`breakfix-controller` 只运行 controller-runtime manager 和环境清理循环。它读取 CRD `spec`，创建和清理 Kubernetes 资源，运行检查点，并写回 CRD `status`。它不打开 SQLite，不读取 `data_dir/challenges`，也不读取 Server 持有的 artifact 文件。

Generator 是由 `Generation` CRD 触发的短期 Job。它经 Server 的受内部密钥保护接口上传 artifact；`VerifyTask` 使用同一内部接口下载 artifact 后在真实运行时验证。Controller 只保留该内部服务地址，不共享 Server 文件系统。

## 数据所有权

| 数据 | 权威所有者 | 访问原则 |
| --- | --- | --- |
| 已发布题目 | `data_dir/challenges/` | Server 读取并在发布时原子写入；不是数据库或 CRD 的副本。 |
| 作者 artifact 与提交归档 | Server 数据目录 | Server 保存和提升；Generator、VerifyTask 通过内部 HTTP 交接。 |
| 账户、作者会话、学习和终端记录 | SQLite | 仅 Server 写入；Controller status 是投影输入。 |
| 用户运行环境 | ContainerEnvironment / VClusterEnvironment CRD | Server 提供期望 spec，Controller 管理资源和 status。 |
| 生成与真实验证 | Generation / VerifyTask CRD | Controller 调和 Job 和状态，Server 将成功结果关联到作者会话。 |

Environment spec 在创建时包含不可变执行快照：题目引用、manifest revision、镜像、runtime 和预期 checkpoint ID。这样 Controller 无须读取题目目录，也不会因为发布后的题目文件改变而改变已启动环境的执行语义。

## 请求与恢复

启动挑战时，Server 从发布题目目录读取题目，创建对应的 Environment CRD，并等待 Controller 将其调和到 Ready。终端通过 Server 代理到 workspace Pod；连接活动作为 Environment spec 的 `activityAt` 输入。Controller 基于该输入计算租约、Draining 与清理，最终通过 CRD status 表达完成、失败和销毁。

Server 重启不会停止 Controller 调和。Controller 重启后会从已有 CRD 继续创建资源、检查检查点或清理。Server 恢复后会重新投影现有 CRD status 到 SQLite，并删除已完成投影的 Destroyed/Failed CRD。真实恢复行为由 [`test/e2e/server-recovery.spec.ts`](../../test/e2e/server-recovery.spec.ts) 验证。

## 当前部署假设

目前两个二进制可以运行在同一主机、使用同一配置文件和同一个 Kubernetes 集群，但它们不共享数据目录的所有权。SQLite 与发布题目目录目前是本地存储，因此不能把 Server 水平扩展到多个写实例。

未来将 Server 和 Controller 分别部署到不同 Pod 或扩展多个 Server 时，需要先引入支持并发写入的数据库、共享题目/artifact 存储以及稳定的内部 Service；这不是当前实现隐含提供的能力。
