# Linux Domain 首轮覆盖矩阵

**状态：**作者内容审查表。24 张场景卡尚未成为运行时 candidate。

## 使用规则

- “相关知识”是读者可见的学习目标文本，不是新的目录层级或稳定关系键。
- “主要证据”表示学习者应从哪里开始建立假设；它不是规定命令或唯一解法。
- `基础` 表示仍需基础 NodeEnvironment probe，但不依赖特殊隔离能力；`gated` 表示对应能力通过前不得制作 candidate。
- 新题必须在这张表中说明与现有场景不同的故障模型、证据或约束。

## 基础实验系统场景

| 场景 | 主 Topic | 实验系统 | 相关知识 | 主要证据 | 根因类别 | 状态 |
| --- | --- | --- | --- | --- | --- | --- |
| `trace-orders-api-effective-config` | Shell、文件与配置定位 | 内部 Web 服务链路 | 生效配置、进程参数、配置优先级 | process/journal、gateway 请求 | 遗留配置覆盖 | 基础 |
| `repair-orders-config-symlink` | Shell、文件与配置定位 | 单机运维主机 | 符号链接、发布目录、文件类型 | journal、链接解析、发布清单 | 链接目标失效 | 基础 |
| `restore-orders-api-config-access` | 用户、组与权限 | 单机运维主机 | 服务账号、目录 search 权限、最小授权 | journal、路径权限、受控身份读取 | 父目录访问被收紧 | 基础 |
| `narrow-oncall-sudo` | 用户、组与权限 | 单机运维主机 | sudoers、命令边界、最小权限 | runbook、sudo 授权列表 | 过度授权 | 基础 |
| `repair-orders-api-execstart` | 进程与 systemd 服务 | 单机运维主机 | unit、drop-in、进程生命周期 | systemctl/journal、HTTP 响应 | 失效 ExecStart 覆盖 | 基础 |
| `restore-orders-api-boot-enable` | 进程与 systemd 服务 | 单机运维主机 | active 与 enabled、target | systemctl enable 状态 | 启动关系丢失 | 基础 |
| `restore-orders-api-log-directory` | 日志与计划任务 | 单机运维主机 | 文件类型、日志路径、服务身份 | journal、access log、路径状态 | 日志目录结构损坏 | 基础 |
| `restore-orders-worker-timer` | 日志与计划任务 | 单机运维主机 | timer 与 service、周期执行证据 | list-timers、成功标记 | timer 被禁用 | 基础 |
| `reclaim-deleted-orders-log` | 磁盘与文件空间 | 单机运维主机 | block、目录用量、打开文件描述符 | df/du 差异、进程持有状态 | deleted-but-open 日志 | 基础 |
| `recover-inode-exhaustion` | 磁盘与文件空间 | 单机运维主机 | inode、临时文件、保留集合 | inode 使用率、worker 写入 | 历史小文件耗尽 inode | 基础 |
| `remove-stale-hosts-override` | DNS 与名称解析 | 内部 Web 服务链路 | NSS 顺序、hosts、权威记录 | getent、DNS 查询、HTTP 请求 | 本地覆盖过期 | 基础 |
| `repair-orders-api-a-record` | DNS 与名称解析 | 私网访问链路 | zone、A 记录、resolver reload | DNS 查询、地址直连、名称请求 | 权威记录缺失 | 基础 |
| `repair-loopback-only-listener` | 端口、TCP 与 HTTP 服务 | 内部 Web 服务链路 | 监听地址、socket、请求路径 | app 本机/远端请求、socket、502 | listener 范围错误 | 基础 |
| `repair-nginx-upstream` | 端口、TCP 与 HTTP 服务 | 内部 Web 服务链路 | upstream、HTTP 502、配置测试 | Nginx error log、app listener、HTTP | upstream 端口漂移 | 基础 |
| `replace-expired-gateway-certificate` | TLS 与证书信任 | 内部 Web 服务链路 | 证书有效期、SAN、证书/私钥配对 | TLS 握手、Nginx 配置、HTTPS | 过期证书 | 基础 |
| `install-training-ca-trust` | TLS 与证书信任 | 内部 Web 服务链路 | trust store、CA、验证绕过 | TLS client 错误、信任库、请求 | client 缺少 CA | 基础 |
| `repair-authorized-keys-permission-chain` | SSH 与远程运维 | 私网访问链路 | StrictModes、权限链、SSH key | sshd journal、client 登录 | SSH 路径权限不安全 | 基础 |
| `restore-proxyjump-access` | SSH 与远程运维 | 私网访问链路 | ProxyJump、Host 配置、两跳信任 | 两跳连接、SSH verbose plan | client 配置错误 | 基础 |

## Capability-gated 场景

| 场景 | 主 Topic | 实验系统 | 相关知识 | 主要证据 | 根因类别 | 状态 |
| --- | --- | --- | --- | --- | --- | --- |
| `repair-local-package-source` | 软件包与配置管理 | 内部软件源 | APT source、metadata、candidate | APT 错误、sources、候选版本 | source suite 错误 | gated: 内部 APT repository |
| `release-held-orders-agent` | 软件包与配置管理 | 内部软件源 | hold、installed/candidate、最小变更 | apt policy、hold 列表、worker journal | 过期 hold | gated: 内部 APT repository |
| `stop-runaway-orders-worker` | CPU、内存与资源限制 | 资源压力主机 | load、服务进程、重试循环 | CPU、journal、API 延迟 | 无界重试 | gated: 有界 CPU 负载 |
| `recover-orders-api-nofile-limit` | CPU、内存与资源限制 | 资源压力主机 | nofile、unit limit、并发症状 | journal、进程 limit、受控请求 | LimitNOFILE 被收紧 | gated: systemd limit/负载 |
| `repair-service-subnet-route` | 网络地址与路由 | 路由实验网络 | 前缀、下一跳、路径分段 | route、逐跳连通、HTTP | 服务子网下一跳错误 | gated: CAP_NET_ADMIN |
| `diagnose-private-ip-conflict` | 网络地址与路由 | 路由实验网络 | 地址所有权、邻居表、冲突症状 | 邻居状态、连续请求 | 重复私网地址 | gated: CAP_NET_ADMIN |

## 首轮审查结论

- 24 个场景覆盖 12 个 Topic；18 个可在基础能力确认后进入 candidate 设计，6 个必须先完成专门 probe。
- 同一 `orders-api` 只作为一致的业务载体，不是根因本身。根因分布在配置加载、访问控制、服务管理、存储、名称解析、连接路径、信任和远程访问。
- 扩展到 40 道以上前，应优先增加新的证据类型和运行模式，例如 logrotate、ACL、端口冲突、Host 路由、host key 轮换和 SFTP，而不是重复同类配置漂移。
