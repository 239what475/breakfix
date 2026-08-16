# 当前架构审查

本文件记录当前实现和最近设计变更中仍需处理的问题。已经确认的系统边界与运行契约继续以 [`docs/`](docs/README.md) 为准；本文件只保留需要修正或讨论的风险。

## P1：生成回合与持久化设计

### 1. 去掉 attempt 上限后会无条件快速重试

`TODO.md` 计划让 authoring run 只受 `DeadlineAt` 约束，但当前 `RunTurn` 对所有 executor 错误都会调用 `RetryAuthoringRun`（[`runtime_service.go`](internal/application/authoring/runtime_service.go:227)）。现有实现仍由固定 attempt 上限兜底；如果只删除上限而不增加错误分类和退避，模型、工具或依赖持续失败时会在 deadline 内快速重建 Eino，重复消耗模型预算并放大故障。

建议：定义可重试的暂时性错误与应立即结束本轮的永久性错误；对可重试错误使用退避，并明确本轮结束后 workflow 的 durable 状态和下一轮入口。不要把“无 attempt 上限”实现成紧密循环。

### 2. 周期快照与 candidate 提交共用归档路径，存在竞争

`TODO.md` 计划每 30 秒归档 workspace。OpenSandbox 适配器却使用固定的 `/tmp/breakfix-generator-candidate.tar.gz`（[`client.go`](internal/adapter/opensandbox/client.go:22)），`ArchiveWorkspace` 会创建、读取并删除该文件（[`client.go`](internal/adapter/opensandbox/client.go:258)）；`SubmitCandidate` 也会调用同一个归档动作（[`generator_service.go`](internal/application/generation/generator_service.go:331)）。快照和提交并发时，任一操作都可能读取另一操作生成的 tar，或删除对方尚未读取的文件。

建议：为每次归档使用 workflow/操作级唯一临时路径，并在 Server 侧对 snapshot、submit 和其他 workspace 归档建立最小的互斥；归档结果必须能明确对应发起者。

### 3. `event` 消息尚未接入 Agent 会话模型

`TODO.md` 计划在 Server 托管回合超时后追加 `event` 消息，但当前消息投影只接受 `user` 和 `assistant`（[`runtime_service.go`](internal/application/authoring/runtime_service.go:260)），Eino 输入构造也只处理这两种角色（[`authoring_executor.go`](internal/adapter/llm/authoring_executor.go:126)）。直接写入 `event` 后，读取 AuthoringSession 或构造下一轮模型输入会失败。

建议：先定义 event 的持久化、API/UI 展示和模型投影语义；通常 event 应可展示但不直接喂给模型，或在构造模型输入时转换为明确的系统/用户可见通知。完成契约前不能只新增数据库 role。

### 4. 快照文件清理和崩溃原子性没有定义

`TODO.md` 只定义了记录快照 digest，并在提交时清空 digest，没有定义旧快照文件的删除、孤儿文件回收，或“写文件成功但数据库指针尚未更新”时的恢复规则。若直接覆盖同一路径，Server 在覆盖和更新 DB 之间崩溃，可能既丢失上一份有效快照，也没有可引用的新快照。

建议：使用不可变或版本化快照文件，先完整写入并校验 digest，再原子更新数据库指针；保留旧版本直到新指针提交成功，并由后台任务清理旧版本和无引用文件。明确重启时如何选择最后一个完整快照。

## 已执行验证

本轮已通过：

- Active workspace 失效恢复的生命周期测试
- `go test -count=1 ./...`
- `npm run build --prefix web`
- `npm run test:acceptance:mcp --prefix test -- --list`
- `git diff --check`

尚未运行真实模型、Kind、OpenSandbox 或 Incus 的 live 验收；上述命令不能证明资源供应、快照恢复或 Judge/Verify 真实链路已经可用。
