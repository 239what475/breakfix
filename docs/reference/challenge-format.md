# 场景内容格式

portable candidate 与已发布场景使用同一组教学和运行时文件，平台身份由后者在 materialize 时补充。Catalog 从状态为 active 的
Challenge 及其 active revision 构造；目录、校验和发布行为以
[`internal/content/challenge/`](../../internal/content/challenge/) 为准。当前代码名 `Challenge` 是兼容术语，产品层称为场景。

## Portable Candidate

作者生成的 CandidateRevision 和 Catalog Release source 都是 portable candidate。`challenge.yaml` 必须包含场景类型、标题、
运行时、描述、节点和检查点，不能包含 `id`、`revision_id`、`source_slug`、`image`、`content_revision` 或 `published_at`。
source 的确定性 `contentRevision` 由文件树计算，不写回 candidate manifest。

```yaml
runtime: node
type: operations-scenario
title: 恢复反向代理
description: 恢复客户端到应用服务的可用路径。
tags: [linux, nginx, systemd]
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

`type` 必须是 `documentation-example` 或 `operations-scenario`。文档示例不能包含标签；运维场景最多八个标签。标签去除首尾空格、
英文字母转为小写后，必须是 1 到 32 个字符的中文、英文字母、数字、`.`、`+` 或 `-`，按规范化结果去重和确定性排序。没有 Tag 实体、
同义词合并或发布后的标签维护流程。

## 已发布目录

已发布场景是 `data_dir/challenges/<source_slug>/<challenge_revision_id>/` 下的 immutable revision 目录，包含：

```text
challenge.yaml
problem.md
solution.md
hints/<checkpoint-id>.md
```

运行时资产按 runtime 分开，不能混用：

```text
# runtime: node
nodes/<node>/generate.sh
nodes/<node>/answer.sh
nodes/<node>/checks.sh

# runtime: k8s
k8s/generate.sh
k8s/answer.sh
k8s/checks.sh
```

发布时平台写入 `id`、`revision_id`、`source_slug`、`image`、`content_revision` 和 `published_at`。`id` 是稳定、不含题意的
opaque Challenge identity；`revision_id` 是本次不可变发布结果。API、Environment 和学习记录使用 `id + revision_id`；`source_slug`
必须与发布目录一致，但不是关系键。

发布目录必须有合法平台字段、非空标题/描述和至少一个 checkpoint。`runtime: node` 的 `image` 是完整 64 位小写 Incus fingerprint；
`runtime: k8s` 的 `image` 是完整 `repository@sha256:<64 位小写摘要>` OCI 引用。Node checkpoint 必须声明执行节点，K8s checkpoint
不能有节点字段；checkpoint 顺序只控制 UI 展示，不表达依赖或必须通过的先后关系。

Server 只在真实验证成功后写入发布字段。作者流程在 `ChallengePublishing` finalizer 中 materialize；Catalog Release 在全部 entry
验证完成后的原子 commit 中 materialize。作者修订为同一 `id` 创建新的 `<challenge_revision_id>`；旧目录和 artifact 保留。弃用仅切换
stable Challenge 状态，不删除历史内容。

## 运行时初始化

所有场景在真实运行时初始化，而不是构建时。平台总是以 `/bin/bash <script>` 执行脚本，因此作者不应依赖可执行位或 shebang。

- Node 基础镜像只含 Ubuntu、systemd、tmux、APT 和常用诊断工具。每个 Node instance 首次启动时运行自己的 `generate.sh`。
- K8s 管理终端首次启动时运行 `k8s/generate.sh`。

`generate.sh` 可以安装场景专属软件并建立错误初态；它不参与 Builder，也不能依赖构建期联网。`answer.sh` 是真实验证的参考解法：
Node 全部节点答案并行执行，K8s 在管理终端执行唯一答案。学习环境永远不会自动执行答案。

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
解析或协议错误才非零退出。Controller 在学习环境周期执行同一协议并写入 Environment status；Verifier 在验证环境运行 answer 后单次
执行它。检查器只能观察环境，不能修改环境或依赖唯一命令路径。

## 教学资产

`problem.md` 描述症状、目标和边界。每个 checkpoint 可通过 `hints/` 提供渐进提示；`solution.md` 说明诊断和修复理由，而不只粘贴命令。
`answer.sh` 必须在 generate 初始化后的真实环境通过全部 checkpoint。学习者没有手动 Submit；作者在发布前看到经
`Build -> ArtifactPublish -> Verify` 真实验证的资产，详见[工作流](../architecture/workflows.md)。
