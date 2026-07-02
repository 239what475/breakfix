# Verify 设计

## 目标

`verify` 是平台判断一道 challenge 是否真正可发布的唯一最终裁决环节。

在进入 `verify` 之前，下面两类来源都一律视为不可信产物：

- agent 生成的 challenge artifact
- 用户直接提交的 challenge artifact

`verify` 的职责不是修复、补全、规范化 challenge 内容，而是判断这份提交物是否真的能够在集群中构建出一套可运行、可验证、可发布的真实题目环境。

## 核心原则

1. `submit` 必须足够薄
   `gateway` 只负责接收 artifact、持久化、创建 `VerifyTask`

2. `verify` 对 challenge 内容是只读的
   不修改 `challenge.yaml`
   不分配最终 challenge id
   不生成修正版 artifact

3. 验证必须发生在真实运行形态上
   verifier 不能只检查文件本身，而必须：
   - 构建真实镜像
   - 在集群中启动真实 challenge 环境
   - 在真实环境里执行 `answer.sh`
   - 在真实环境里执行 `verify.sh`

4. `publish` 与 `verify` 在代码职责上分离
   这不意味着产品上要额外人工触发 publish。
   当前流程中，verify 成功后系统会立即进入 publish。
   但实现上仍然必须保持“先 verify，再 publish”两个明确阶段。

5. 平台托管字段只允许在 `publish` 阶段写入
   最终 `id`、最终 `image`、最终目录路径都属于 gateway 的发布职责，不属于 verifier 职责

## 总体流程

### Agent 出题流程

1. 用户输入一个粗略题意
2. `review agent` 生成结构化 draft
3. 用户可修改 draft
4. `generate agent workflow` 根据 draft 生成 challenge 文件
5. `generate agent` 使用 `lab_*` 工具进行内部自测
6. `judge` 判断当前产物是否可以提交平台验证
7. 如果 `judge` 通过，则系统自动将当前产物提交给 gateway `submit`
8. gateway 保存 artifact，并创建 `VerifyTask`
9. verifier 执行真实集群验证
10. 如果验证失败：
    - `VerifyTask` 记录失败报告
    - `judge` 读取报告
    - `judge` 将问题反馈回 `generate`
11. 如果验证成功：
    - 系统立即进入 publish
    - publish 成功后，整个 workflow 才算最终成功

### 用户直接提交流程

1. 用户直接上传或提交 challenge artifact
2. gateway 保存 artifact，并创建 `VerifyTask`
3. verifier 执行真实集群验证
4. 如果验证失败：
   - 前端展示验证报告
   - 用户修复后重新提交
5. 如果验证成功：
   - challenge 立即被发布

## VerifyTask CRD

`VerifyTask` 是唯一的异步验证对象。

建议结构如下：

```yaml
apiVersion: breakfix.dev/v1
kind: VerifyTask
metadata:
  name: vt-xxxxx
spec:
  source:
    kind: agent | user
    ref: ""
  submission:
    id: "sub-xxxxx"
status:
  phase: Pending | Running | Verified | Failed | Succeeded
  message: ""
  startedAt: null
  completedAt: null
  jobName: ""
  podName: ""
  tempImage: ""
  report:
    buildPassed: false
    answerPassed: false
    verifyPassed: false
    summary: ""
    issues: []
```

## VerifyTask 状态语义

- `Pending`
  已创建，等待 controller 接管

- `Running`
  verifier job 正在执行

- `Verified`
  verifier 已完成真实验证且通过，等待 publish

- `Failed`
  验证完成，但失败

- `Succeeded`
  验证成功且已 publish

## Gateway 职责

### Submit

gateway `submit` 只做以下事情：

1. 校验请求身份
2. 分配 `submissionID`
3. 将原始 artifact 落盘到本地文件系统
4. 记录必要元信息
5. 创建 `VerifyTask`
6. 返回 `verify_task_id`

gateway submit 不做：

- build 镜像
- 执行 `answer.sh`
- 执行 `verify.sh`
- 直接 publish

### 内部下载接口

gateway 需要提供内部下载接口，让 verifier job 能取到已保存的原始 artifact。

建议形式：

`GET /api/internal/verify-submissions/:id/artifact`

因为 gateway 已经是 artifact 的权威持有方，所以 verifier 只需要下载接口即可。

### Publish

publish 由 gateway/controller 负责，并且只发生在 verification 成功之后。

gateway publish 需要做：

1. 读取自己保存的原始 artifact
2. 解包到 staging 目录
3. 写入平台托管字段
4. materialize 到 `data/challenges/<challenge-id>`
5. 更新 `VerifyTask.status`

publish 是唯一允许修改最终 challenge 元数据的阶段。
在产品行为上，它会在 verify 成功后自动发生。

## Verifier 职责

verifier 的职责是：把 artifact 变成真实 challenge 运行环境，并判断它是否真的成立。

它需要做：

1. 从 gateway 下载原始 artifact
2. 解包到临时工作目录
3. 做只读结构校验：
   - 必要文件是否存在
   - YAML 是否可解析
   - shell 脚本是否存在
4. 构建临时验证镜像
5. 用该镜像在集群中启动真实 challenge 环境
6. 在真实环境里执行 `answer.sh`
7. 在真实环境里执行 `verify.sh`
8. 收集结构化结果
9. 回写 `VerifyTask.status`

verifier 不做：

- 重写 `challenge.yaml`
- 分配最终 challenge id
- 生成最终发布用 image 名
- 上传修正版 artifact
- 直接 publish

## 真实验证语义

这是整个设计里最重要的一条：

验证通过的对象不是 challenge 文件本身，而是 challenge 文件构建出来的真实运行环境。

也就是说 verifier 必须验证下面四件事：

1. artifact 能成功构建出镜像
2. 镜像能在集群中启动真实 challenge 环境
3. `answer.sh` 能在真实环境里完成修复
4. `verify.sh` 会接受这个修复后的真实环境

只要其中任意一步失败，这道题就不能发布。

## 临时镜像策略

verifier 应该使用临时验证镜像名，而不是正式 challenge image 名。

例如：

- `172.18.0.1:5000/break-fix/verify-vt-xxxxx:latest`

这样可以避免在 verify 阶段依赖最终发布命名。

策略建议：

- 验证成功：保留临时镜像，或按后续实现决定是否复用
- 验证失败：删除失败镜像

核心要求是：
失败的验证不应在仓库里留下会被误认为正式题目的坏镜像。

## 失败报告

verifier 返回的应该是结构化问题，而不是只有原始日志。

建议结构：

```yaml
issues:
  - code: BUILD_FAILED
    message: "docker build failed"
  - code: ANSWER_EXIT_NONZERO
    message: "answer.sh exited with non-zero status"
  - code: VERIFY_EXIT_NONZERO
    message: "verify.sh failed"
  - code: MISSING_FILE
    message: "verify.sh missing from artifact"
```

这个报告会被两类调用方消费：

- agent workflow 中的 judge / generate
- 用户直接提交的前端页面

## Agent Workflow 与 Verify 的关系

在 agent 生成流程里，`VerifyTask` 才是外部最终裁决者。

这意味着：

- `lab_*` 自测只是 agent 内部质量提升手段
- generate agent 认为“已经做好了”并不等于平台接受
- judge 认为“已经可以提交了”也不等于平台接受
- judge 通过后，应由系统自动触发 submit，而不是依赖 judge 或 generate 主动调用 submit 工具
- 只有 `VerifyTask` 成功，challenge 才算真正通过

如果 `VerifyTask` 失败：

1. `judge` 读取 `VerifyTask.status.report`
2. `judge` 将报告整理成可执行反馈
3. `generate` 根据反馈修复并重新提交

## 用户提交与 Verify 的关系

用户直接提交 challenge 时，也必须走同一个 `VerifyTask` 流程。

这意味着：

- 不存在用户专用的另一套验证逻辑
- 不存在 agent 一套、用户一套的验收标准
- 两者共享同一套运行语义、同一套报告结构、同一套发布规则

## 职责分层

### Submit

- 接收 artifact
- 落盘 artifact
- 创建 `VerifyTask`

### Verify

- 基于真实 challenge 运行形态进行验证
- 返回成功 / 失败 / 报告

### Publish

- 将已验证通过的 challenge materialize 到正式 challenge store
- 写入平台托管字段

当前实现选择：

- 用户或 agent `submit`
- gateway 创建 `VerifyTask`
- verifier job 完成真实验证
- controller 在看到 `Verified` 后立即执行 publish
- publish 成功后 `VerifyTask` 才进入最终 `Succeeded`

这个分层是刻意保持的，后续实现不要混淆。

## 非目标

这版设计明确不做：

- verifier 侧修复 challenge
- verifier 上传修正版 artifact
- 将 publish 逻辑塞进 verifier job
- 因来源不同而信任 agent 多于用户
- 用本地静态文件检查替代真实运行时验证

## 建议的下一步

先实现 `VerifyTask` 作为统一的异步验证对象，并让下面两条路径都走它：

- agent workflow 在 judge 判定通过后的系统自动提交
- 用户直接提交

在此基础上，保持 verify 与 publish 的代码边界清晰，但产品流程直接自动 publish。
