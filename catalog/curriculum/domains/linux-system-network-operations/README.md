# Linux 系统与网络运维

**状态：**课程重构草案，尚未成为 Catalog Release。

这个 Domain 训练学习者在 Linux 主机和小型私有网络中建立证据、缩小故障域、做出有边界的修复，并从用户视角确认服务恢复。它不是命令速记题库，也不把所有 Linux 能力都承诺为首发运行时能力。

## 范围

- 文件、账号、权限、软件包状态、进程和 systemd 生命周期、日志、计划任务、资源压力、磁盘空间、私网连通性、DNS、HTTP/TLS 与 SSH。
- 单机和多节点事故；多节点题只使用环境私网中的命名节点和服务。
- OpenSSH、Nginx、dnsmasq、一个小型 HTTP 应用和一个后台 worker 作为可复用运维对象。

## 首发范围之外

- boot loader、内核模块、宿主机内核调优、硬件、RAID/LVM、公有云控制面和外部网络依赖。
- 挂载、loop device、主机防火墙、抓包、network namespace、修改地址或路由；只有 capability probe 证明隔离边界和行为后才能实现对应题目。
- Kubernetes、容器运行时、数据库、CI/CD 与云厂商管理。

## 实验系统

所有核心场景必须使用 [Linux 实验系统约定](labs.md)。该文件定义固定节点、服务、名称、日志路径和证书关系，防止题库积累互不兼容的一次性环境。

教学内容的读者层次、提示边界、复盘结构和作者检查规则见[内容模型](content-model.md)。首轮场景的去重、能力状态和后续扩展依据见[覆盖矩阵](coverage.md)。

## 当前核心场景

当前有 24 道核心场景卡，每个 Topic 两道。数字不是目标配额，只是第一轮用来验证课程模型、运行时能力和实现模式的最小集合。

| Topic | 核心场景 | 扩展条件 |
| --- | ---: | --- |
| [Shell、文件与配置定位](../../topics/linux-system-network-operations/shell-files-and-configuration.md) | 2 | 基础文件与进程证据可用后扩展。 |
| [用户、组与权限](../../topics/linux-system-network-operations/users-groups-and-permissions.md) | 2 | 基础账户、ACL 与 sudo 可用后扩展。 |
| [软件包与配置管理](../../topics/linux-system-network-operations/packages-and-configuration.md) | 2 | 需要本地 APT repository 能力。 |
| [进程与 systemd 服务](../../topics/linux-system-network-operations/processes-and-systemd-services.md) | 2 | 需要 systemd、journal 与 service lifecycle probe。 |
| [日志与计划任务](../../topics/linux-system-network-operations/logs-and-scheduled-tasks.md) | 2 | 需要 journald、logrotate 与 timer/cron probe。 |
| [CPU、内存与资源限制](../../topics/linux-system-network-operations/cpu-memory-and-resource-limits.md) | 2 | 资源限制与有界负载行为必须先确认。 |
| [磁盘与文件空间](../../topics/linux-system-network-operations/disk-and-file-space.md) | 2 | 需要可控的文件增长与打开文件行为。 |
| [网络地址与路由](../../topics/linux-system-network-operations/network-addressing-and-routing.md) | 2 | 需要 `CAP_NET_ADMIN` 和多节点网络 probe。 |
| [DNS 与名称解析](../../topics/linux-system-network-operations/dns-and-name-resolution.md) | 2 | 需要本地 resolver 与私网名称解析。 |
| [端口、TCP 与 HTTP 服务](../../topics/linux-system-network-operations/ports-tcp-and-http.md) | 2 | 需要 Nginx 和固定 HTTP 应用。 |
| [TLS 与证书信任](../../topics/linux-system-network-operations/tls-and-certificate-trust.md) | 2 | 需要本地 CA 与固定证书资产。 |
| [SSH 与远程运维](../../topics/linux-system-network-operations/ssh-and-remote-operations.md) | 2 | 需要 sshd、私钥和多节点访问 probe。 |

## 学习导览

建议先完成 Shell、文件与配置定位，以及用户、组与权限；随后学习进程/systemd、日志和磁盘。网络部分按“DNS -> 端口、TCP 与 HTTP -> TLS -> SSH”浏览。网络地址与路由、资源限制和软件源题目应等各自能力确认后再做，不应该阻塞其他 Topic。

Topic 的推荐关系只用于导览，不锁定任何题目。
