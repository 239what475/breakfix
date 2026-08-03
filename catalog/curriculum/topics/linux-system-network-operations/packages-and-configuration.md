# 软件包与配置管理

**Topic ID：**`packages-and-configuration`
**Domain：**Linux 系统与网络运维
**状态：**首轮核心场景设计；依赖内部 APT repository capability probe

## 范围

本 Topic 关注软件包候选版本、私有软件源和有意保留的安装约束。所有题目必须使用环境内的受控 APT repository，不能把公网镜像、第三方 package server 或网络偶然性变成完成条件。

## 建议前置知识

Shell、文件与配置定位；进程与 systemd 服务。

## 参考资料

- [APT User's Guide](https://www.debian.org/doc/manuals/apt-guide/)
- [`sources.list(5)`](https://manpages.debian.org/sources.list)
- [`apt-mark(8)`](https://manpages.debian.org/apt-mark)

## 学习导读

### 学习目标

- 从 APT 的 source、metadata、installed/candidate 版本和 hold 状态定位升级失败。
- 区分软件源可用性、版本选择和包状态三类不同问题。
- 在内部仓库中进行最小、可解释的软件包修复。

### 核心心智模型

APT 的最终安装结果由 repository 定义、可见 metadata、版本候选和显式约束共同决定。能看到新版本不等于 solver 会选择它；一个 package 的 hold 也不应影响其他有意约束。

### 诊断证据与工具

使用 APT 的明确错误、source 定义、package policy 和 hold 列表建立证据。先确认 metadata 与 candidate，再改变 package 状态，避免把 service 故障误判为安装问题。

### 常见误区

- 为了修复内部源而添加公网 mirror 或手工解包软件。
- 清空所有 hold 以求一次升级成功。
- 不看 candidate 就强制安装版本。

### 面试追问

- source 条目、metadata refresh、candidate selection 与 hold 各在什么阶段影响安装？
- 如何只解除一个事故遗留 hold，同时证明其他 hold 没有被改变？

### 推荐关系

建议在 Shell 和 systemd 基础之后学习；需要先完成内部 APT repository capability probe。

## 核心场景

### `repair-local-package-source`：修复内部软件源

- **环境地图：**`app` 使用环境内 `apt.training.internal`；`orders-agent` 由内部 repository 提供并受 systemd 管理。
- **本题相关知识：**APT source、suite/component、package metadata、候选版本。
- **学习目标：**根据 APT 的具体失败将问题限定在 source 定义，并在不引入外部依赖的前提下恢复软件安装。
- **初始状态：**`app` 只能使用 `apt.training.internal` 提供的内部 APT repository。`/etc/apt/sources.list.d/orders.list` 的 suite 拼写错误，导致 `apt update` 失败；仓库中已有 `orders-agent` 的批准版本。
- **用户看到的症状：**安装 `orders-agent` 失败，APT 明确报告内部源的 Release 或 metadata 获取错误；节点没有任何公网软件源可用。
- **任务边界：**修复现有内部源定义并按 package manager 正常安装；不能添加公网源、手工解包 `.deb`、绕过签名策略或复制二进制文件。
- **完成条件：**内部 metadata 成功刷新，`orders-agent` 安装为仓库批准版本并能被 systemd 正常管理。
- **检查点：**APT 成功读取内部源；`orders-agent` 的已安装版本等于批准版本；对应服务为 active。
- **提示层级：**方向：将安装失败与服务健康分开看待；证据：读取 APT 对内部源的精确错误并比较 source 条目；概念：URI、suite 与 component 共同定位 repository 的 metadata。
- **解答与复盘：**先用 APT 错误缩小到 source 定义，恢复受控仓库条目后检查 candidate 并正常安装。生产中应对内部 repository 的 metadata、签名与 source 配置做发布前检查。
- **作者 verifier：**未新增公网 source；未绕过签名或手工安装；后续 metadata refresh 仍稳定成功。
- **能力状态：**仅在“内部软件源” capability probe 通过后实现。

### `release-held-orders-agent`：解除过期的软件包 hold

- **环境地图：**`app` 从内部 repository 获取 `orders-agent`；该 agent 服务于 `orders-worker`，另有无关 package 保持有意 hold。
- **本题相关知识：**installed/candidate 版本、APT hold、依赖求解、最小变更。
- **学习目标：**识别阻止升级的精确约束，只解除事故遗留的 package hold。
- **初始状态：**内部仓库提供 `orders-agent` 的修复版本，但该包在一次事故中被 hold，仍停在旧版本；另一项无关软件包的 hold 是有意保留的。
- **用户看到的症状：**APT 能看到新 candidate，却不会升级 `orders-agent`；worker 的 journal 显示旧版本已触发已知兼容性问题。
- **任务边界：**只解除影响 `orders-agent` 的过期约束；不能清空全部 hold、强制降级其他包或改变 repository 优先级。
- **完成条件：**`orders-agent` 升级到批准版本，其他被有意 hold 的包保持不变，关联服务恢复健康。
- **检查点：**`orders-agent` 版本正确；该包不再处于 hold；worker 服务成功运行。
- **提示层级：**方向：新 candidate 存在却未升级时，检查显式约束；证据：比较 installed/candidate 版本与 hold 列表；概念：hold 是 package 级约束，不是 repository 整体不可用。
- **解答与复盘：**确认 source 与 candidate 都正常后，定位 `orders-agent` 的过期 hold，仅释放该约束并验证 worker 恢复。生产变更应记录每个临时 hold 的原因、负责人和到期时间。
- **作者 verifier：**无关 hold 未改变；未改变 source priority 或强制降级；服务重启后使用批准版本。
- **能力状态：**仅在“内部软件源” capability probe 通过后实现。

## 扩展范围

将来可覆盖 pin、conffile 冲突、被中断的 dpkg 配置和受控回滚。每个场景都必须提供本地 repository 与确定的 package 资产。
