# 日志与计划任务

**Topic ID：**`logs-and-scheduled-tasks`
**Domain：**Linux 系统与网络运维
**状态：**首轮核心场景设计，需 journal、timer 与日志文件 capability probe

## 范围

本 Topic 覆盖从 journal 与应用日志建立证据、保持日志输出路径，以及用 systemd timer 管理可观察的周期任务。题目不把“手动执行一次成功”误判为计划任务已经恢复。

## 建议前置知识

Shell、文件与配置定位；进程与 systemd 服务；用户、组与权限。

## 参考资料

- [`journalctl(1)`](https://www.freedesktop.org/software/systemd/man/latest/journalctl.html)
- [`systemd.timer(5)`](https://www.freedesktop.org/software/systemd/man/latest/systemd.timer.html)
- [`logrotate(8)`](https://man7.org/linux/man-pages/man8/logrotate.8.html)

## 学习导读

### 学习目标

- 将服务启动失败、应用日志输出和 journal 中的证据关联起来。
- 区分 timer 的启用、下一次触发与关联 service 的实际执行结果。
- 恢复受管日志与调度机制，而不是用临时文件或无限循环脚本掩盖问题。

### 核心心智模型

journal、应用文件日志和 timer/service 是独立但相互印证的状态。服务成功执行一次不代表周期调度存在；日志路径配置正确也不代表对象类型和服务身份允许写入。

### 诊断证据与工具

使用 `journalctl` 确认服务失败阶段，检查日志路径对象类型与权限；使用 timer 状态、下一次触发和成功标记共同确认调度，而不只运行 service 一次。

### 常见误区

- 为了让服务启动而关闭日志或把输出改到任意临时目录。
- 用 cron 或无限循环替换现有 systemd timer。
- 只验证 worker 手工运行，忽略 timer 没有启用。

### 面试追问

- 如何证明一个 timer 真的会在未来触发，而非仅仅处于 loaded 状态？
- 应用日志不存在时，如何区分路径、权限、服务身份和应用配置问题？

### 推荐关系

建议在 systemd 与文件权限 Topic 之后学习；磁盘与资源题会复用这里的日志和周期任务概念。

## 核心场景

### `restore-orders-api-log-directory`：恢复应用日志目录

- **环境地图：**`app` 上的 `orders-api.service` 以 `orders` 身份写入 `/var/log/orders-api/access.log`，journal 记录启动失败原因。
- **本题相关知识：**文件类型、日志路径、服务身份、journal 与应用日志。
- **学习目标：**把日志初始化失败与路径对象、权限和服务身份关联起来，恢复可持续的日志输出。
- **初始状态：**`orders-api` 已配置将访问日志写入 `/var/log/orders-api/access.log`。一次清理操作把目录替换成 root-only 的普通文件，服务在打开日志时失败。
- **用户看到的症状：**服务启动失败，journal 记录无法创建或打开访问日志；配置文件仍指向正确的日志路径。
- **任务边界：**恢复预期目录结构和服务账号访问权；不能禁用日志、把日志改到随意的临时路径，或让日志目录对所有账号可写。
- **完成条件：**服务启动后持续写入预期日志文件，journal 中不再出现日志初始化错误。
- **检查点：**日志路径是可用目录；服务为 active；发起 HTTP 请求后 access log 出现对应记录。
- **提示层级：**方向：从服务启动阶段而非 HTTP 代码开始排查；证据：比较 journal、路径对象类型和服务账号访问；概念：文件路径的一部分若类型错误，正确的配置字符串仍无法写入。
- **解答与复盘：**定位到日志初始化的对象类型冲突，恢复固定目录及最小写入权限，再用真实请求确认日志持续产生。生产清理任务应先验证目标类型并保留日志目录契约。
- **作者 verifier：**日志没有被禁用或改到临时路径；普通账号不可任意写入；服务重启后仍能写入规范日志。
- **能力状态：**需“单机运维主机”中的 journal 和文件日志 probe。

### `restore-orders-worker-timer`：恢复后台维护定时器

- **环境地图：**`ops` 上的 `orders-worker.timer` 定期触发 oneshot `orders-worker.service`，worker 在 `/var/lib/orders-worker/last-success` 写入成功记录。
- **本题相关知识：**systemd timer、oneshot service、下一次触发、周期任务证据。
- **学习目标：**区分 service 可手工执行与 timer 已恢复调度，并从多种时间状态确认修复。
- **初始状态：**`orders-worker.timer` 应每五分钟触发一次 oneshot 的 `orders-worker.service`，但 timer 被 disable，最近一次成功运行记录已过期。
- **用户看到的症状：**worker 可以手工运行，定期生成的 `/var/lib/orders-worker/last-success` 却长期未更新；`systemctl list-timers` 中没有下一次触发时间。
- **任务边界：**恢复既有 timer/unit 的正常调度；不能用无限循环脚本、cron 替代或手工伪造成功标记。
- **完成条件：**timer 被启用并有下一次触发时间，worker 按 unit 执行后更新真实成功记录。
- **检查点：**timer 为 active；定时器列表显示下一次触发；一次受管 worker 执行更新成功记录。
- **提示层级：**方向：将 worker 的执行能力与调度能力分开检查；证据：比较 timer 状态、下一次触发和最近成功记录；概念：timer 与它触发的 service 是两个独立 unit。
- **解答与复盘：**确认 worker 本身可用后，定位 timer 的启用状态并恢复其与 service 的受管关系。生产中应对“下一次运行”和“最后一次成功”分别告警，避免只监控进程是否存在。
- **作者 verifier：**timer 在服务管理器重启后仍启用；未以 cron 或循环进程替代；真实周期触发能更新记录。
- **能力状态：**需“单机运维主机”中的 systemd timer probe。

## 扩展范围

可扩展到 logrotate、磁盘保留策略、cron 环境差异和失败告警。扩展场景必须有真实日志或时间证据，不用猜测时间窗口作为唯一判定。
