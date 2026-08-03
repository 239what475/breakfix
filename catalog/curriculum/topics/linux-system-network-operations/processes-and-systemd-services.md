# 进程与 systemd 服务

**Topic ID：**`processes-and-systemd-services`
**Domain：**Linux 系统与网络运维
**状态：**首轮核心场景设计，需 systemd 和 journal capability probe

## 范围

本 Topic 训练学习者把进程现象、unit 定义、journal 和服务生命周期关联起来。修复应回到受管 unit 或其明确的 drop-in，不通过额外后台进程、cron 包装或反复手工启动制造“暂时可用”。

## 建议前置知识

Shell、文件与配置定位；用户、组与权限。

## 参考资料

- [`systemd.service(5)`](https://www.freedesktop.org/software/systemd/man/latest/systemd.service.html)
- [`systemctl(1)`](https://www.freedesktop.org/software/systemd/man/latest/systemctl.html)
- [`journalctl(1)`](https://www.freedesktop.org/software/systemd/man/latest/journalctl.html)

## 学习导读

### 学习目标

- 从 unit 的实际合成配置和 journal 理解 systemd 为什么启动或拒绝启动服务。
- 区分进程正在运行、service 为 active 与 unit 为 enabled 三种不同状态。
- 用受管 unit 修复服务，而不是制造脱离 systemd 的替代进程。

### 核心心智模型

systemd 运行的是主 unit 与 drop-in 合成后的结果。`active` 描述当前运行状态，`enabled` 描述目标启动关系；两者都不能仅凭手工启动推断。

### 诊断证据与工具

`systemctl status`、`systemctl cat`、`journalctl -u` 和服务端点各自验证 unit 定义、启动失败和业务可用性。应先确认系统实际加载的 unit，再决定是否修改主文件或覆盖层。

### 常见误区

- 只看发行版主 unit，忽略 drop-in 覆盖。
- 用软链接、wrapper 或后台 `nohup` 代替修复 systemd 定义。
- 将“目前 active”误认为“重启后会自动启动”。

### 面试追问

- 何时应该使用 drop-in，何时应修改主 unit？
- `start`、`restart`、`enable`、`daemon-reload` 分别解决什么问题？

### 推荐关系

建议在 Shell/配置定位之后学习，并作为日志、资源限制和计划任务 Topic 的基础。

## 核心场景

### `repair-orders-api-execstart`：修复错误的服务启动命令

- **环境地图：**`app` 由 systemd 管理 `orders-api.service`；发行版 unit 正确，附加 drop-in 位于 unit 覆盖层。
- **本题相关知识：**systemd unit、drop-in、ExecStart、journal。
- **学习目标：**从实际生效的 unit 定义定位启动失败，而非仅查看主 unit 文件。
- **初始状态：**`orders-api.service` 的发行版 unit 正确，但一个遗留 drop-in 覆盖了 `ExecStart`，指向已删除的旧二进制路径。配置和运行账号均正确。
- **用户看到的症状：**服务反复启动失败，`systemctl status` 和 journal 显示 `No such file or directory`；手工运行当前二进制可以工作。
- **任务边界：**修复 unit 的受支持定义；不能在旧路径放置软链接、创建包装脚本、关闭 systemd restart 行为或把服务改成无管理进程。
- **完成条件：**systemd 使用当前二进制和既有配置启动 `orders-api`，API 恢复健康。
- **检查点：**unit 为 active；主进程成功运行；服务端点返回预期响应。
- **提示层级：**方向：比较手工运行成功与 systemd 启动失败的输入差异；证据：读取 journal 和 systemd 实际合成的 unit；概念：drop-in 可以覆盖主 unit 的启动行为。
- **解答与复盘：**由启动错误定位失效执行路径，再从合成 unit 找到覆盖层并恢复受支持定义。生产中应在发布 drop-in 后执行 unit 校验与受控 restart，避免旧路径在长期运行后失效。
- **作者 verifier：**失效路径未通过软链接或 wrapper 被伪造；重启后仍由 systemd 管理；无关 unit 未被改动。
- **能力状态：**需“单机运维主机”中的 systemd/journal probe。

### `restore-orders-api-boot-enable`：恢复服务的开机启用状态

- **环境地图：**`ops` 运行 `orders-api.service`，该服务应随 `multi-user.target` 的正常启动关系拉起。
- **本题相关知识：**active/enabled、`[Install]`、target、服务生命周期。
- **学习目标：**区分当前进程状态与开机启用关系，并用 systemd 的标准语义恢复后者。
- **初始状态：**`orders-api.service` 可以手动启动，但在一次维护中被 disable；它应属于 `multi-user.target` 的正常服务集合。
- **用户看到的症状：**当前请求暂时成功，重启服务管理器后的模拟启动检查却显示 API 不会自动拉起。
- **任务边界：**恢复标准 enable 关系；不能以 root 的 shell profile、cron `@reboot` 或独立守护脚本替代 unit enablement。
- **完成条件：**服务保持运行，并被 systemd 标记为 enabled，满足目标启动关系。
- **检查点：**`orders-api.service` 为 active；`systemctl is-enabled` 返回 `enabled`；服务端点返回预期响应。
- **提示层级：**方向：将“现在能访问”与“下次启动能否拉起”分开验证；证据：查看 active、enabled 状态和 unit 的安装语义；概念：enable 创建目标关系，不等同于立即启动进程。
- **解答与复盘：**确认服务并非配置或二进制故障，而是启动关系被移除，按 unit 的 `[Install]` 语义恢复 enablement 并验证当前服务。生产中应把 enablement 纳入基础配置检查，而不是依赖人工启动。
- **作者 verifier：**目标关系正确且持久；未用 cron、shell profile 或外部守护替代；重启后服务能自动恢复。
- **能力状态：**需“单机运维主机”中的 systemd/journal probe。

## 扩展范围

后续可加入依赖顺序、执行用户、工作目录、环境文件、失败恢复和 reload/restart 差异；每题必须有可观察的业务结果，不能只要求背诵 unit 字段。
