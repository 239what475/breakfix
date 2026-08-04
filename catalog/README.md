# Breakfix Catalog 内容工作区

此目录保存进入 portable Catalog Release 前的课程与题目内容。

## 当前状态

`curriculum/` 是供内容评审的草案，不是已发布题库。当前 Linux Domain 先保留 24 道具体核心场景；它们共享少数实验系统，并明确区分：

- 可以在基础 NodeEnvironment 能力确认后优先实现的场景；
- 必须等待特定 capability probe 的核心场景；
- 仅作为课程覆盖清单、尚未形成题目的内容。

草案不包含平台身份、运行时镜像摘要、OCI bundle 或声称已经验证的 candidate 脚本。一道题目规格只有满足以下条件后，才能成为 portable candidate：

1. `Domain`、`Topic`、`Tag` 和 RoadmapRevision 契约已经实现。
2. 题目依赖的 NodeEnvironment capability 已通过真实 probe。
3. 规格已转换为 `challenge.yaml`、`problem.md`、`solution.md`、提示文件、`generate.sh`、`answer.sh` 和 `checks.sh`。
4. 完整 candidate 已通过真实 `Build -> ArtifactPublish -> Verify`。

## 目录布局

```text
catalog/
  curriculum/
    domains/<domain>/README.md
    domains/<domain>/labs.md
    topics/<domain>/<topic>.md
```

每份 Topic 文档有两部分：

- **学习导读**：说明这个 Topic 的系统模型、学习目标、证据与工具、常见误区、推荐前置知识和面试追问。
- **核心场景**：可评审的完整场景卡，包含环境地图、相关知识、学习目标、确定初态、用户可见症状、独立 checkpoint、分层提示和解答复盘。
- **扩展范围**：尚不应实现的故障模型或课程内容；它们不能被伪装成已有题目，也不计入题目数量。

## 内容规则

- 题目场景、题干、解答、初始化设计和验证设计必须原创。公开资料只用于事实调研和覆盖检查，不能被当作改写模板。
- 一个挑战围绕一个主要故障模型。复合事故只有在两个根因确实需要学习者区分、且每个根因有独立用户可见证据时才使用。
- `problem.md` 只描述用户能看到的事故、目标和边界；作者实现细节、初始化方法和 checker 设计不出现在学习者题干中。
- checkpoint 只断言当前可观察结果，例如服务健康、客户端响应、权限边界或文件状态。跨重启、跨升级、长期保留、实现偏好和“不要影响无关对象”属于 verifier 的作者测试，不是周期 checkpoint。
- 新题必须复用现有实验系统，或先在 Domain 的 `labs.md` 中增加并审查新的系统。不能为单题发明一次性服务、配置格式或拓扑。
- 内容字段的详细边界见各 Domain 的 `content-model.md`；作者用 `coverage.md` 维护跨题去重与能力状态，二者都不是运行时 manifest。
