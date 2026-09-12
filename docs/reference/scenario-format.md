# 场景内容格式

运维场景的 portable candidate 与已发布 revision 使用同一组教学和运行时文件，平台身份由后者在 materialize 时补充。运维场景
Catalog 从状态为 active 的 Scenario 及其 active revision 构造；目录、校验和发布行为以
[`internal/content/scenario/`](../../internal/content/scenario/) 为准。`Scenario` 是领域对象，产品层显示为“场景”。

## Portable Candidate

作者生成的 CandidateRevision 和 Catalog Release source 都是 portable candidate。运维场景必须声明现场说明、运行时、依赖版本、
环境拓扑、初始化说明、目标现象与至少一项关键证据；不能包含 `id`、`revision_id`、`source_slug`、`image`、`content_revision` 或
`published_at`。source 的确定性 `contentRevision` 由文件树计算，不写回 candidate manifest。

```yaml
runtime: node
type: operations-scenario
title: 恢复反向代理
description: 恢复客户端到应用服务的可用路径。
tags: [linux, nginx, systemd]
versions:
  - component: nginx
    version: 1.27.5
topology: "客户端通过反向代理访问应用服务。"
initialization: "generate.sh 配置错误的上游地址并启动服务。"
reproduction:
  objective: "反向代理指向错误上游，客户端无法获得应用响应。"
  evidence:
    - id: proxy-upstream-wrong
      description: "代理配置仍使用错误的上游端口。"
      node: proxy
nodes:
  - name: proxy
    title: Proxy
checkpoints:
  - id: proxy-ready
    title: Proxy is healthy
    description: 服务正常运行并转发请求。
    hint: hints/proxy-ready.md
    node: proxy
```

`versions` 的每项必须有唯一的 `component` 与非空 `version`。`topology` 说明环境中的参与者和关系，`initialization` 说明
`generate.sh` 如何建立初始状态；`reproduction.objective` 是要复现的现象，`evidence` 给出可观察事实。Node evidence 必须声明
执行节点，Kubernetes evidence 不得声明节点。当前运维场景入口只接受 `type: operations-scenario`，最多八个标签。文档实践化
使用独立内容模型，不通过本格式、运维标签或作者投稿流程发布。标签去除首尾空格、英文字母转为小写后，必须是 1 到 32 个字符的中文、英文字母、数字、`.`、`+` 或
`-`，按规范化结果去重和确定性排序。没有 Tag 实体、同义词合并或发布后的标签维护流程。

## 已发布目录

已发布场景是 `data_dir/scenarios/<source_slug>/<scenario_revision_id>/` 下的 immutable revision 目录，包含：

```text
scenario.yaml

# Optional learning aids
problem.md
solution.md
hints/<checkpoint-id>.md
```

运行时资产按 runtime 分开，不能混用：

```text
# runtime: node
nodes/<node>/generate.sh
nodes/<node>/reproduce.sh
nodes/<node>/answer.sh     # Optional reference repair; required on every node when present
nodes/<node>/checks.sh     # Required only for nodes that own checkpoints

# runtime: k8s
k8s/generate.sh
k8s/reproduce.sh
k8s/answer.sh              # Optional reference repair
k8s/checks.sh              # Required only when checkpoints are declared
```

发布时平台写入 `id`、`revision_id`、`source_slug`、`image`、`content_revision` 和 `published_at`。`id` 是稳定、不含题意的
opaque Scenario identity；`revision_id` 是本次不可变发布结果。API、Environment 和学习记录使用 `id + revision_id`；`source_slug`
必须与发布目录一致，但不是关系键。

发布目录必须有合法平台字段、非空标题/描述与完整复现核心，但不要求任何学习辅助。`runtime: node` 的 `image` 是完整 64 位小写 Incus fingerprint；
`runtime: k8s` 的 `image` 是完整 `repository@sha256:<64 位小写摘要>` OCI 引用。Node checkpoint 必须声明执行节点，K8s checkpoint
不能有节点字段；checkpoint 顺序只控制 UI 展示，不表达依赖或必须通过的先后关系。

Server 只在真实验证成功后写入发布字段。作者流程在 `ScenarioPublishing` finalizer 中 materialize；Catalog Release 在全部 entry
验证完成后的原子 commit 中 materialize。作者修订为同一 `id` 创建新的 `<scenario_revision_id>`；旧目录和 artifact 保留。弃用仅切换
stable Scenario 状态，不删除历史内容。

## 运行时初始化

所有场景在真实运行时初始化，而不是构建时。平台总是以 `/bin/bash <script>` 执行脚本，因此作者不应依赖可执行位或 shebang。

- Node 基础镜像只含 Ubuntu、systemd、tmux、APT 和常用诊断工具。每个 Node instance 首次启动时运行自己的 `generate.sh`。
- K8s 管理终端首次启动时运行 `k8s/generate.sh`。

`generate.sh` 可以安装场景专属软件并建立错误初态；它不参与 Builder，也不能依赖构建期联网。Verifier 先运行
`reproduce.sh` 证明 manifest 所述的初始现象存在。只有完整提供了参考修复时，才继续运行 `answer.sh` 和修复后检查点：Node
仅在拥有 evidence 的节点运行 `reproduce.sh`，随后全部节点答案并行执行；K8s 都在管理终端运行。学习环境永远不会自动执行
`reproduce.sh` 或 `answer.sh`。

## 复现证据协议

每个执行位置的 `reproduce.sh` stdout 只输出 JSON：

```json
{
  "evidence": [
    {"id": "proxy-upstream-wrong", "observed": true, "summary": "short result", "details": "optional detail"}
  ]
}
```

脚本必须恰好报告该位置被分配的每个 evidence ID 一次。`observed: true` 表示目标现象的证据确实存在；`false` 是有效结果，表示
该 candidate 未能复现现场，并且 Verifier 不会继续执行参考修复。脚本、解析或协议错误才非零退出。`reproduce.sh` 只能观察
初始化后的环境，不能修改环境或执行、source `generate.sh`、`answer.sh` 或用户脚本。

## 检查点协议

每个执行位置的 `checks.sh` stdout 只输出 JSON：

```json
{
  "checks": [
    {"id": "checkpoint-id", "passed": true, "summary": "short result", "details": "optional detail"}
  ]
}
```

脚本必须恰好报告该执行位置 manifest 声明的每个 checkpoint ID 一次。未通过是有效检查结果，输出 `passed: false` 且退出 0；脚本、
解析或协议错误才非零退出。Controller 在声明 checkpoint 的学习环境周期执行同一协议并写入 Environment status；Verifier 仅在
复现证据通过且提供完整参考修复后单次执行它。检查器只能观察环境，不能修改环境或依赖唯一命令路径。

## 教学资产

`problem.md` 可提供现场的诊断思路、症状和边界；每个 checkpoint 可选择引用一个 `hints/` 中的渐进提示。它们都不是发布前提，
但存在时必须是 artifact 内可解析的 Markdown，并且 hint 必须由它所属 checkpoint 实际引用。

`solution.md` 与参考修复必须成组提供：Node 场景要求每个声明节点都有 `answer.sh`，K8s 场景要求有 `k8s/answer.sh`，并且场景至少
声明一个 checkpoint 与对应 `checks.sh`。`solution.md` 必须为每个 checkpoint 恰好包含一个 `<!-- checkpoint: <id> -->` 标记；
参考修复必须在目标现象已复现的真实环境通过全部 checkpoint。没有参考修复的场景只验证复现证据，验证报告不会伪造空答案或完成状态。
学习者没有手动 Submit；作者在发布前分别看到复现证据和（提供时的）参考修复/检查点验证结果，详见[工作流](../architecture/workflows.md)。
