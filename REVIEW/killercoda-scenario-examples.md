# Killercoda Scenario Examples 对照审查

## 审查对象

- 本地路径：`/home/what/myproject/breakfix-similar-projects/killercoda-scenario-examples`
- 审查提交：`3d03034`
- 定位：Killercoda 托管平台公开的 scenario 内容样例；它不是完整的开源平台实现。

## 已确认的设计

1. 每个 scenario 是可读目录，`index.json` 声明标题、描述、backend image、intro、steps
   和 finish；Markdown 与脚本放在同一目录。
2. `verification/index.json` 将 step 的 `verify` 字段指向 `verify.sh`；该脚本检查一个
   具体状态。`network-traffic-kubernetes` 展示每个 step 独立 Markdown。
3. `foreground.sh`、`background.sh` 和 `assets` 能在场景开始时准备环境；内容中还能嵌入
   `{{exec}}`，将给定命令直接送进终端。

## 与 Breakfix 的对照

Breakfix 的发布目录已经与此一样可读，但它的资产更完整：Node 题按逻辑节点保存
`generate.sh`、`answer.sh`、`checks.sh`，K8s 题在 `k8s/` 下保存同类运行时初始化资产，另有题面、
解答和按 checkpoint 的提示。Breakfix 的 `challenge.yaml` 有 opaque ID 与 revision 语义，且检查点必须覆盖全部声明 ID。
这比 `index.json + 可选 verify.sh` 更适合长期可验证题库。

## 可以吸收

### 现在吸收

- **目录的教学可读性**：按 checkpoint 给解答添加稳定章节锚点，要求题面、提示、解答和
  checkpoint title 使用同一组可读术语。无需新增复杂格式；可在内容 lint 中检查每个
  checkpoint 都存在 hint，且 `solution.md` 有对应锚点。
- **小而真实的内容样例库**：为每种题目结构保留命名清楚的样例 challenge，而不是将一个
  大而偶然的题目当范本。它会成为人工作者与 Generator 的共同参考。
- **资源引用校验**：借鉴其同目录内容组织，扩展现有路径校验以拒绝题目目录外的 Markdown
  链接、hint 路径和静态资源引用，确保发布目录可原子移动和离线阅读。

### 题库具备数据后再做

- 为 `runtime: k8s` 题提供受控的“打开文件/打开服务”展示动作。动作只产生 UI 导航
  或安全 URL，不执行用户命令，也不增加第二个完成状态。

## 不采用

- 不采用 `{{exec}}` 式命令注入。它会替用户执行解决方案，与 Breakfix 的终端学习和助手
  只读原则冲突。
- 不采用“每一个教学步骤一个 verify.sh”的判定模型。检查点应验证最终可观察状态，允许
  多种正确修复路径，并由 Controller 周期执行。
- 不将 `index.json` 另设为第二份 manifest。`challenge.yaml` 已是完整、版本化且由发布
  流程校验的契约。

## 结论

Killercoda 的价值在内容工程和教学可读性，而非验证模型。应把它转化为内容 lint、样例
库和解答结构要求，保持现有自动检查点协议不变。
