# 题目内容格式

已发布题目是 `data_dir/challenges/<id>/` 下的目录。题库不存入数据库，也不是 Kubernetes CRD；Server 读取该目录得到 catalog。目录、校验和发布行为以 [`internal/challenge/`](../../internal/challenge/) 为准。

## 已发布目录

每道发布题至少包含：

```text
challenge.yaml
Dockerfile
generate.sh
problem.md
solution.md
hints/<checkpoint-id>.md
checks/checkpoints.sh
answer.sh
```

`challenge.yaml` 记录用户可见元数据、runtime、检查点，以及平台发布时写入的 `id`、`image`、`published_at`。发布目录必须有合法 ID、非空标题/描述/标签、`easy|medium|hard` 难度、`container|vcluster` runtime 和至少一个检查点。每个检查点都有唯一 ID、标题和描述；提示路径必须留在题目目录内，依赖只能引用其他检查点。

Generator 在验证前产出的 artifact 使用相同文件布局，但平台管理的 `id`、`image` 与 `published_at` 不属于 generator 输入。Server 只在 VerifyTask 成功且作者发布时写入这些字段。

## 运行时初始化

`generate.sh` 被打包进镜像，在 workspace Pod 首次启动时由基础镜像入口执行一次。它负责构造故障初始状态；不能依赖每次用户连接终端时再次执行。初始化完成后，用户进入常规 shell。

`Dockerfile` 负责复制题目资产和声明基础镜像。生成工作流会拒绝构建期联网安装或下载，因为真实 VerifyTask 构建不假定外网可用。运行时差异只通过 `runtime` 和对应基础镜像表达，不通过另一个题目格式分叉。

## 检查点协议

`checks/checkpoints.sh --json` 是题目完成的唯一可执行判断。它返回 JSON：

```json
{
  "checks": [
    {"id": "checkpoint-id", "passed": true, "summary": "short result", "details": "optional detail"}
  ]
}
```

脚本必须以成功退出码返回，并且恰好报告 manifest 声明的每个 checkpoint ID 一次。`summary` 必须非空；`details` 用于诊断。Controller 将同一份协议的结果写入 Environment status，所有检查点通过即自动完成。

检查点验证环境的最终、可观察状态，不应规定用户必须输入的命令、固定编辑文件路径或工具链。检查器也不应修改环境；否则自动轮询会改变题目本身。

## 教学资产

`problem.md` 清楚描述症状、目标和边界。每个检查点可通过 `hints/` 提供渐进提示；`solution.md` 说明诊断和修复理由，而不只粘贴命令。`answer.sh` 是平台真实验证和作者自测用的参考解法，必须在运行时初始化后的环境中通过全部检查点。

学习者工作台没有手动 Submit：进度来自 Controller 周期检查。题目作者在生成工作流中审核自然语言检查点，并在发布前看到经过 VerifyTask 验证的实际资产，见[作者生成与真实验证](../architecture/authoring-workflow.md)。
