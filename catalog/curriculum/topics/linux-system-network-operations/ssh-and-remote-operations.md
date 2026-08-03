# SSH 与远程运维

**Topic ID：**`ssh-and-remote-operations`
**Domain：**Linux 系统与网络运维
**状态：**首轮核心场景设计，需 sshd 与多节点私网 capability probe

## 范围

本 Topic 处理私网环境中的 SSH key、`authorized_keys` 安全权限链和 ProxyJump。训练密钥、known_hosts 与节点均为环境专用资产；题目不接触真实凭据，也不通过关闭 host-key 检查或直接暴露私网节点解决访问问题。

## 建议前置知识

用户、组与权限；DNS 与名称解析；端口、TCP 与 HTTP 服务。

## 参考资料

- [`sshd_config(5)`](https://man.openbsd.org/sshd_config)
- [`ssh_config(5)`](https://man.openbsd.org/ssh_config)
- [`ssh(1)`](https://man.openbsd.org/ssh)

## 学习导读

### 学习目标

- 将 SSH 认证失败区分为网络、服务、客户端选择、host key 和服务端权限链问题。
- 理解 `StrictModes` 为何检查 home、`.ssh` 与 `authorized_keys` 的完整路径。
- 使用 ProxyJump 保持私网隔离，而不是为排障直接暴露目标主机。

### 核心心智模型

SSH 连接由多层信任构成：客户端需选择正确目标、用户和私钥；每一跳验证服务端 host key；服务端再验证用户公钥和文件权限。ProxyJump 不复制私钥到跳板，而是让客户端将两跳组合为一条受保护路径。

### 诊断证据与工具

用 SSH verbose 输出、sshd journal、`sshd -t`、文件 owner/mode 以及分段连接检查定位。不要把“能到达端口 22”误认为身份认证或跳板配置正确。

### 常见误区

- 关闭 `StrictModes` 或 host-key checking 来恢复登录。
- 把 private-app 直接暴露给 client，绕过 bastion。
- 只检查 `authorized_keys` 内容，忽略父目录权限和客户端 alias 实际匹配结果。

### 面试追问

- `Permission denied (publickey)` 时如何区分私钥选择错误与服务端权限链不安全？
- ProxyJump 中每一跳的用户、名称和 host key 分别由谁验证？

### 推荐关系

建议在权限、DNS 和 TCP/HTTP 基础之后学习；受限 SFTP 等扩展需另行验证 chroot 能力。

## 核心场景

### `repair-authorized-keys-permission-chain`：修复公钥认证权限链

- **环境地图：**`client -> bastion -> private-app`；private-app 上的 `deploy` 用户使用训练公钥登录，sshd 保持 `StrictModes` 开启。
- **本题相关知识：**public key authentication、`StrictModes`、home 目录、`.ssh`、`authorized_keys`。
- **学习目标：**从 sshd 拒绝日志识别不安全路径组件，并在不放宽 SSH 安全策略的前提下恢复公钥认证。
- **初始状态：**`private-app` 上 `deploy` 用户的训练公钥内容正确，但 home directory、`.ssh` 或 `authorized_keys` 中的一处对组可写，触发 sshd 的 `StrictModes` 拒绝。
- **用户看到的症状：**client 使用批准私钥连接时被拒绝；server journal 明确指出不安全的路径组件。通过 bastion 的网络路径本身正常。
- **任务边界：**恢复安全的 owner/mode 链；不能关闭 `StrictModes`、把 home tree 设为过度开放、改用密码认证，或替换训练密钥。
- **完成条件：**批准私钥可经既有跳板登录 `private-app`，未批准密钥仍被拒绝。
- **检查点：**批准 key 登录成功；无效 key 登录失败；私网跳板路径保持可用。
- **提示层级：**方向：先确认网络路径已通，再从服务端认证拒绝着手；证据：读取 sshd 对具体路径组件的日志并检查完整目录链；概念：`StrictModes` 拒绝可能被其他用户写入的认证文件路径。
- **解答与复盘：**将公钥内容正确、网络可达与认证被拒绝三项证据组合，定位有问题的 owner/mode，恢复安全权限后用批准和未批准 key 双向验证。生产中应将 SSH 账号目录权限纳入配置管理与合规检查。
- **作者 verifier：**`StrictModes` 保持开启；home tree 未被过度放宽；服务重启后公钥认证仍正确。
- **能力状态：**需“私网访问链路”中的 OpenSSH probe。

### `restore-proxyjump-access`：恢复经跳板机的访问

- **环境地图：**`client -> bastion -> private-app`；private-app 只允许由 bastion 到达，client 使用 `private-app` SSH alias 发起两跳连接。
- **本题相关知识：**SSH Host 配置、`HostName`、`User`、ProxyJump、两跳信任边界。
- **学习目标：**将一条失败的复合 SSH 连接拆成两跳，定位 client alias 的配置错误并保持目标私网隔离。
- **初始状态：**`private-app` 只能由 `bastion` 到达。client 的 SSH alias 配置中 ProxyJump 使用了错误的跳板用户名和目标主机名；每一跳的训练 key 与 known_hosts 均已正确提供。
- **用户看到的症状：**`ssh private-app` 失败，而分别验证 client->bastion 和 bastion->private-app 显示网络和服务本身可用。
- **任务边界：**修复 client 侧的精确 SSH 配置；不能将 `private-app` 直接暴露给 client、关闭 host-key 校验、把私钥复制到不应持有它的节点，或建立长期未受管隧道。
- **完成条件：**client 使用 alias 经 bastion 安全登录 private-app，私网隔离仍存在。
- **检查点：**`ssh private-app` 成功经过 bastion；client 无法直接连接 private-app 的 SSH 端口；目标会话以规定用户建立。
- **提示层级：**方向：不要把两跳失败当成一个不可分割的问题；证据：分别验证每一跳，再查看 alias 的最终 connection plan；概念：`Host`、`HostName`、`User` 与 `ProxyJump` 在客户端组合决定实际目标。
- **解答与复盘：**先证明两段基础路径可用，排除网络和服务端问题，再修复 client alias 的名称与跳板身份组合，最后用别名完成端到端登录。生产中应为关键跳板路径提供受控的 SSH config 模板和连接回归测试。
- **作者 verifier：**host-key 检查保持开启；private-app 未对 client 直接暴露；无关 SSH alias 未被破坏。
- **能力状态：**需“私网访问链路”中的 OpenSSH 和拓扑 probe。

## 扩展范围

local port forwarding、host key 轮换和受限 SFTP 可后续加入。SFTP/chroot 题只有在“受限文件传输” capability probe 通过后才能实现。
