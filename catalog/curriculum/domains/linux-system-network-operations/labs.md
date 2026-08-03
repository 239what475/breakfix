# Linux 实验系统约定

**状态：**内容契约，尚未实现为运行时镜像或 `generate.sh`。

本文件限制 Linux Domain 可以使用的实验系统。新增核心题必须复用其中一个系统；若确实需要新系统，先在这里补充其目的、复用范围、资产和能力依赖，再设计题目。

## 1. 单机运维主机

**节点：**`ops` 或 `app`

**固定对象：**

- `orders-api.service`：一个小型 HTTP 服务，配置位于 `/etc/orders-api/`，日志位于 `/var/log/orders-api/`，运行账号为 `orders`。
- `orders-worker.service`：一个后台 worker，工作目录为 `/var/lib/orders-worker/`，由 `orders-worker.timer` 周期触发维护任务。
- 系统组件：systemd、journald、cron、logrotate、OpenSSH 与基础诊断工具。

**适用 Topic：**Shell/文件、权限、systemd、日志、资源、磁盘、软件包状态。

**不承诺：**本地 APT repository、swap、cgroup resource control、挂载或修改主机网络。

## 2. 内部 Web 服务链路

**节点：**`client`、`gateway`、`app`

**固定对象：**

- `app` 运行 `orders-api.service`，私网端口为 `8080`。
- `gateway` 运行 Nginx，对 `api.training.internal` 提供 HTTP 或 HTTPS，并反向代理到 `app`。
- `client` 只作为外部调用方，使用 DNS 名称请求服务。
- Web 场景的成功响应固定包含可识别的服务字段，例如 `{"service":"orders-api","status":"ok"}`。

**适用 Topic：**DNS、端口/TCP/HTTP、TLS、配置定位、日志。

**不承诺：**公网 DNS、外部 CA、Ingress、主机防火墙或复杂负载均衡。

## 3. 私网访问链路

**节点：**`client`、`resolver`、`bastion`、`private-app`

**固定对象：**

- `resolver` 为 `training.internal` 提供私有 DNS；`api.training.internal` 和 `private-api.training.internal` 使用固定测试记录。
- `bastion` 是进入 `private-app` 的唯一 SSH 跳板。
- `private-app` 运行仅对私网开放的 `orders-api.service`。
- 题目使用专为训练生成的 SSH key、known_hosts 和 private CA，不接触真实用户凭据。

**适用 Topic：**DNS、SSH、TLS、端到端 HTTP 诊断。

**不承诺：**修改节点地址、路由、邻居表或跨宿主机网络编排。

## 4. capability-gated 扩展系统

以下系统在对应能力未通过 probe 前不得实现为 candidate：

| 系统 | 依赖能力 | 可承载内容 |
| --- | --- | --- |
| 内部软件源 | 每题私有 APT repository、package metadata 和受控 `.deb` 资产 | repository、hold/pin、依赖、升级与回滚。 |
| 资源压力主机 | 有界 CPU/内存/I/O 负载、可观察 OOM、systemd limit、可能的 swap | 资源压力、file descriptor、服务限制。 |
| 路由实验网络 | `CAP_NET_ADMIN`、隔离的地址/路由/邻居表修改、可恢复多子网拓扑 | prefix、默认路由、next hop、metric、IP 冲突、非对称路径。 |
| 受限文件传输 | sshd chroot、SFTP 权限链和安全上传处理 | SFTP、chroot、受限合作方账号。 |

## 场景卡规范

每个核心场景卡必须包含：

- **环境地图**：学习者需要知道的节点、服务和名称关系；不泄露底层 Provider、动态地址或根因。
- **本题相关知识**：两到四个自然语言考点，说明完成题目前应理解什么，但不直接点出本题根因。
- **学习目标**：完成后能建立什么证据、区分什么现象或做出什么有边界的修复。
- **初始状态**：题目初始化后确定存在的节点、服务、文件、名称、错误和可见证据。
- **用户看到的症状**：学习者无需猜测平台内部实现就能观察到的失败。
- **任务边界**：允许修复什么，不能通过哪些不安全捷径达成目标。
- **完成条件**：用户视角的业务恢复结果。
- **checkpoint**：一到三个独立、可周期观察的当前状态。
- **提示层级**：依次给出诊断方向、应收集的证据和关键概念，不提前给出完整修复命令或唯一文件路径。
- **解答与复盘**：解释证据如何收敛到根因、修复为何可靠、如何验证，以及生产环境应如何预防或更早发现。
- **作者 verifier**：只记录重启持久性、无关对象不变、负向授权、配置结构或其他不适合作为公开 checkpoint 的测试。

“重启后仍有效”“升级后仍有效”“不修改无关对象”“不造成 restart storm”等内容是作者 verifier 的附加测试；它们不能替代用户可见 checkpoint。
