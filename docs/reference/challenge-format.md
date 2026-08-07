# 题目内容格式

portable candidate 与已发布题目使用同一组教学和运行时文件，但平台身份由后者在 materialize 时补充。题库不存入数据库，也不是
Kubernetes CRD；Server 从已发布目录和当前 RoadmapRevision 构造 Catalog。目录、校验和发布行为以
[`internal/content/challenge/`](../../internal/content/challenge/) 为准。

## Portable Candidate

作者生成的 CandidateRevision 和 Catalog Release source 都是 portable candidate。它们的 `challenge.yaml` 只保存
标题、运行时、难度、描述、节点和检查点，不能包含 `id`、`source_slug`、`image`、`content_revision` 或 `published_at`。
source 的确定性 `contentRevision` 由文件树计算，不写回 candidate manifest。

## 已发布目录

已发布题目是 `data_dir/challenges/<source_slug>/<challenge_revision_id>/` 下的 immutable revision 目录，包含：

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
nodes/<node>/checks.sh       # 仅有 checkpoint 的节点需要

# runtime: k8s
k8s/generate.sh
k8s/answer.sh
k8s/checks.sh
```

`challenge.yaml` 记录用户可见元数据、`runtime: node|k8s`、节点和检查点，以及发布时由平台写入的 `id`、`revision_id`、`source_slug`、`image`、`content_revision`、`published_at`。`id` 是稳定且与题意无关的 opaque Challenge identity；`revision_id` 是本次不可变发布结果的 identity。API、Environment、学习记录和 Roadmap binding 同时引用 `id + revision_id`；`source_slug` 是可读目录名，必须与发布目录同名，不能作为关系键。

发布目录必须有合法的发布字段、非空标题/描述、`easy|medium|hard` 难度和至少一个 checkpoint。`runtime: node` 的 `image` 必须是完整的 64 位小写 Incus fingerprint；`runtime: k8s` 的 `image` 必须是完整的 `repository@sha256:<64 位小写摘要>` OCI 引用。Node manifest 还必须声明唯一逻辑节点；每个 checkpoint 必须声明执行节点，节点名称不能泄漏 Provider 实现。K8s checkpoint 没有节点字段。checkpoint 数组顺序只决定 UI 展示，不表达依赖或必须通过的先后顺序。

Server 只在验证成功后为已发布目录写入平台托管字段；作者流程在 `ChallengePublishing` 写入，Catalog Release 在所有 entry
验证完成后的原子 commit 写入。运行时 Roadmap binding 同时记录该目录的 `source_slug` 和
`materialized_revision`；后者只属于平台运行时投影，不会写入 portable candidate 或 Catalog Release。
它不会改写已验证 candidate archive。作者修订会创建同一 `id` 下的新 `<challenge_revision_id>` 目录，旧目录和 artifact
保留；弃用只移除当前 Roadmap binding，不删除历史内容。

发布后的课程归属和横向筛选信息不属于 `challenge.yaml`。Catalog Release 在最终 commit 时将 Domain、Topic、Tag、Challenge binding 和关系图一并写入 immutable RoadmapRevision；只有与当前 revision 精确绑定的题目才进入公开 Catalog。

## 运行时初始化

所有题目在真实运行时初始化，而不是构建时。平台总是以 `/bin/bash <script>` 执行脚本，因此作者不应依赖可执行位或 shebang。

- Node 基础镜像只含 Ubuntu、systemd、tmux、APT 和常用诊断工具。每个 Node instance 首次启动时运行自己的 `nodes/<node>/generate.sh`，成功后写 sentinel。
- K8s 管理终端首次启动时运行 `k8s/generate.sh`。

`generate.sh` 可以安装题目专属软件并建立错误初态，例如 Node 题安装 Nginx 后创建错误 systemd 配置，或 K8s 题创建初始 workload。它不参与 Builder，也不能依赖构建期联网。基础镜像、runtime-init unit 和 entrypoint 由平台提供，不属于题目资产。

`answer.sh` 是真实验证的参考解法。Node 的全部节点答案并行执行；K8s 在管理终端执行唯一答案。学习环境永远不会自动执行答案。

## 检查点协议

每个执行位置的 `checks.sh` stdout 只输出 JSON：

```json
{
  "checks": [
    {"id": "checkpoint-id", "passed": true, "summary": "short result", "details": "optional detail"}
  ]
}
```

脚本必须恰好报告该执行位置 manifest 声明的每个 checkpoint ID 一次。未通过是有效的检查结果：输出 `passed: false` 且退出 0；脚本、解析或协议错误才以非零退出。`summary` 必须非空，`details` 用于诊断。

Controller 在学习环境周期执行同一协议并写入 Environment status；Verifier 在验证环境的 answer 后单次执行它。两者共享唯一的 checkpoint report 解析与完整性校验，Node 各节点和 VK8s 管理终端没有独立格式。检查器必须只观察环境，不能修改环境或依赖用户必须输入的命令、唯一编辑路径或底层平台资源。

## 教学资产

`problem.md` 应清楚描述症状、目标和边界。每个 checkpoint 可通过 `hints/` 提供渐进提示；`solution.md` 说明诊断与修复理由，而不只粘贴命令。`answer.sh` 必须在 generate 初始化后的真实环境通过全部 checkpoint。

学习者没有手动 Submit：进度来自 Controller 自动检查。作者在发布前看到经 `Build -> ArtifactPublish -> Verify` 真实验证的
资产，详见[工作流](../architecture/workflows.md)。
