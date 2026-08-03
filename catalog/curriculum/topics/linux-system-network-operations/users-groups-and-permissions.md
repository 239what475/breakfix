# 用户、组与权限

**Topic ID：**`users-groups-and-permissions`
**Domain：**Linux 系统与网络运维
**状态：**首轮核心场景设计，尚未实现为运行时题目

## 范围

本 Topic 覆盖运行账号、所有权、目录遍历权限、访问控制列表和 `sudo` 的最小授权。修复必须解释哪个身份需要访问哪个对象，不能以 `chmod 777`、关闭安全检查或无限制 root 权限取代诊断。

## 建议前置知识

Shell、文件与配置定位；理解文件权限与目录 search 权限的区别。

## 参考资料

- [`credentials(7)`](https://man7.org/linux/man-pages/man7/credentials.7.html)
- [`acl(5)`](https://man7.org/linux/man-pages/man5/acl.5.html)
- [`sudoers(5)`](https://www.sudo.ws/docs/man/sudoers.man/)

## 学习导读

### 学习目标

- 根据服务实际运行身份解释一次访问被拒绝。
- 区分文件读写权限、目录 search 权限、组与 ACL 的职责。
- 用最小授权恢复事故流程，并能说明为何没有扩大权限面。

### 核心心智模型

Linux 访问控制不是只看叶子文件：进程身份必须能遍历每个父目录，并通过所有权、mode、组和 ACL 的组合获得所需权限。sudo 是对可执行操作的授权边界，不是“临时 root”。

### 诊断证据与工具

从 journal 中的被拒绝身份开始，沿路径检查对象类型、owner、group、mode 和 ACL；sudo 场景则以实际 runbook 命令和 `sudo -l` 的授权结果为证据。

### 常见误区

- 用 `chmod 777` 或把服务账号加入管理员组解决访问问题。
- 只检查目标文件，忽略父目录的 search 权限。
- 为方便值班保留无限制 `NOPASSWD: ALL`。

### 面试追问

- 文件可读但路径仍返回 `Permission denied`，可能缺少什么权限？
- 如何设计一条允许重启单一服务、却不能获得 root shell 的 sudo 规则？

### 推荐关系

建议在 Shell、文件与配置定位之后学习；它是 SSH、TLS 私钥和服务运行账号题目的直接前置。

## 核心场景

### `restore-orders-api-config-access`：恢复服务配置访问权

- **环境地图：**`app` 运行 `orders-api.service`，服务身份为 `orders`；TLS 配置位于 `/etc/orders-api/tls/`。
- **本题相关知识：**服务账号、目录 search 权限、最小授权、ACL。
- **学习目标：**从被拒绝身份和完整路径找到访问边界，并恢复仅服务所需的读取能力。
- **初始状态：**`orders-api.service` 以 `orders` 账号运行，需要读取 `/etc/orders-api/tls/client.key`。密钥本身的所有权正确，但其父目录在一次权限收紧后不再允许服务账号遍历。
- **用户看到的症状：**服务启动失败，journal 包含对私钥的 `Permission denied`；以 root 查看文件时内容和 mode 看似正常。
- **任务边界：**只授予 `orders` 读取该配置所需的最小权限；私钥不能变为所有用户可读，服务账号也不能加入管理员组或获得 sudo。
- **完成条件：**服务能读取密钥并成功启动，同时普通未授权账号仍无法读取该文件。
- **检查点：**`orders-api.service` 为 active；以 `orders` 身份可读取所需配置；以未授权训练账号读取失败。
- **提示层级：**方向：确认谁在读取失败，而不是只看 root 能否读取；证据：沿私钥路径检查每层目录的访问条件；概念：读取文件需要文件权限，访问路径还需要目录的 search 权限。
- **解答与复盘：**从服务账号和 journal 出发定位父目录限制，使用最窄的组或 ACL 恢复路径访问，再以授权与未授权身份验证边界。生产中应将服务密钥访问模型写入部署规范并持续审计。
- **作者 verifier：**私钥不会对普通用户可读；服务账号未加入管理员组或获得 sudo；服务重启后权限边界保持。
- **能力状态：**可在“单机运维主机”实现。

### `narrow-oncall-sudo`：收紧值班人员的 sudo 权限

- **环境地图：**`ops` 上 `oncall` 组按 runbook 维护 `orders-api.service`，需要重启服务和读取其 journal。
- **本题相关知识：**sudoers、命令授权、最小权限、服务运维 runbook。
- **学习目标：**将真实运维操作转换为明确的 sudo 边界，并验证允许与拒绝两侧。
- **初始状态：**`oncall` 组需要重启 `orders-api.service` 并查看该服务的 journal。一份临时 sudoers 文件错误地给予该组无限制的免密 root 权限。
- **用户看到的症状：**值班手册中的两个操作可用，但安全审计显示 `oncall` 成员也能执行 root shell 和无关服务控制。
- **任务边界：**保留手册需要的两类运维操作；不能通过删除所有 sudo 权限、禁用 sudo 或保留宽泛通配规则完成任务。
- **完成条件：**值班账号仅能以批准方式重启 `orders-api.service` 和查看其 journal，其余提权操作仍被拒绝。
- **检查点：**批准的 service 操作成功；批准的 journal 查询成功；`oncall` 不具有无限制 root shell 权限。
- **提示层级：**方向：先列出事故流程真正需要的动作；证据：比较 runbook 与当前 sudo 授权列表；概念：sudoers 应授权具体命令及必要参数，而不是抽象的管理员身份。
- **解答与复盘：**将值班职责拆成最小命令集合，用经过语法校验的规则表达，并同时验证可做与不可做的操作。生产中应将 runbook 与 sudo 策略共同审查，避免事故临时放宽后无人回收。
- **作者 verifier：**sudoers 语法有效；没有宽泛通配或 shell 逃逸路径；无关 service 控制仍被拒绝；规则在服务重启后持续生效。
- **能力状态：**可在“单机运维主机”实现。

## 扩展范围

可扩展到 ACL mask、setgid 协作目录、sticky directory 和受限 SFTP；这些题必须证明它们带来新的权限语义，而不是重复改变 mode bit。
