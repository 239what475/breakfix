# Breakfix 设计文档

> 一个运维/SRE 面试练习平台。类似 SadServers × LeetCode，不是写代码而是修复系统故障。题目由 Agent 自动生成和验证。
>
> 当前阶段：纯容器题目。

---

## 核心分割：Control Plane vs Data Plane

```
                        Internet (公网)
                             │
                             ▼
┌────────────────────────────────────────────────────────┐
│  网关 ECS (2C2G，已备案+域名，唯一公网入口)               │
│                                                        │
│  API Server (gRPC+mTLS)── 业务逻辑、调度，嵌入 SQLite      │
│  Teleport (Auth+Proxy)  ── CA 证书签发、SSH 入口、会话录制    │
│  Tinyproxy            ── 出网 HTTP 代理（题目需要时）      │
│  Nginx                ── CLI 二进制分发                  │
│                                                        │
└──────────┬─────────────────────────────────────────────┘
           │ 内网 VPC
           ▼
┌────────────────────────────────────────────────────────┐
│  ACK K8s 集群 (Data Plane)                              │
│                                                        │
│  只跑一件事：用户做题的 Pod。没有业务逻辑。              │
│                                                      │
│  Nodes: 无公网 IP，不开 SLB，不设公网端点               │
│                                                      │
│  │  ┌──────────────────────────────────────────┐  │
│  │  │  user-github-xxx (Namespace)             │  │
│  │  │  ├── Pod abc123 (cleanup-logs)           │  │
│  │  │  └── Pod def456 (nginx-502)              │  │
│  │  │  一个用户可同时做多道题                     │  │
│  │  └──────────────────────────────────────────┘  │
│  │  ┌──────────────────────────────────────────┐  │
│  │  │  user-github-yyy (Namespace)             │  │
│  │  │  ├── Pod ghi789 (...)                    │  │
│  │  └──────────────────────────────────────────┘  │
│  │                                                │
│  │  每个用户独立 Namespace，退出即销毁               │
└────────────────────────────────────────────────────────┘
           │
           ▼
┌──────────────────┐
│  ACR (VPC 内网)   │
│  容器镜像          │
└──────────────────┘
```

## ECS 资源规划

| 机器 | 规格 | 计费 | 说明 |
|------|------|------|------|
| 网关 ECS | 2C2G | 已有（已备案 + 域名） | 常驻 |
| ACK 节点 | 4C16G（默认 1 台） | 按量付费 | 自动扩缩，最少 1，最大按需 |

**抵扣金**：科研包 2000 元，用于 ACK 节点按量付费 + ACR 个人版免费。

### 不买的东西

| 不买 | 替代方案 |
|------|------|
| NAT 网关 | 集群默认零出网；需要出网的题目走网关 ECS Tinyproxy |
| SLB | 没有公网 Service，全走网关 ECS |
| Node 公网 IP | 全内网 |
| ACK API Server 公网端点 | 仅内网端点，网关 ECS 通过 VPC 内网调 K8s API |
| RDS | SQLite 嵌入 API Server，零额外内存，零额外部署 |
| Redis | 不需要，单机 API Server 用内存即可 |

---

## 技术选型

| 层 | 选型 | 说明 |
|------|------|------|
| 容器编排 | ACK Standard（阿里云） | 免费控制面，纯托管 |
| 网关 | 已有 ECS 2C2G | 已备案 + 域名，跑所有 control plane |
| SSH 代理 & 认证 | **Teleport** | CA 签发证书 + K8s exec 代理，不需要 sshd |
| 用户管理 | Teleport | 初期本地用户，后续接 OIDC |
| API 认证 | **mTLS + Teleport CA** | Teleport 给 CLI 签的短期证书直接用于 gRPC |
| 会话录制 | Teleport 自带 | 自动录制操作，后续评判用 |
| 出网代理 | **Tinyproxy** | 轻量 HTTP 正向代理，题目需要外网时走它 |
| API Server | Go | gRPC + mTLS，信任 Teleport CA |
| CLI | Go (Cobra) | 单一二进制，内嵌 tsh，GitHub Actions 发 release |
| 数据库 | **SQLite** | 嵌入 API Server 进程，零额外内存和部署 |
| 镜像仓库 | ACR 个人版 | VPC 内网端点，免费 |

---

## 网络策略

### Ingress（入网）

```
用户 → 公网 → 网关 ECS (Teleport Proxy)
                     │
               VPC 内网 exec
                     │
                     ▼
                ACK Pod
```

用户唯一的入口是网关 ECS 上的 Teleport Proxy。Pod 无公网 IP，外部无法直接访问。

### Egress（出网）

| 场景 | 出网 | 方案 |
|------|------|------|
| Pod pull 镜像 | 否 | ACR VPC 内网端点 |
| `apt install` / `pip install` | 否 | 基础镜像预装 |
| 题目明确需要外网 | **是** | Pod 配 HTTP_PROXY 指向网关 ECS Tinyproxy |

默认集群不出网。只有明确需要外网的题目才通过 Tinyproxy 放行。

### Tinyproxy 配置

```ini
# /etc/tinyproxy/tinyproxy.conf
Port 3128
Listen 0.0.0.0

# 只允许 ACK Pod 网段（例如 Pod CIDR 10.244.0.0/16）
Allow 10.244.0.0/16
Allow 127.0.0.1

# 并发限制
MaxClients 20
MaxConnectionsPerHost 5

# 基础认证
BasicAuth breakfix-proxy <随机密码>

# 不暴露自己是代理
DisableViaHeader Yes
```

Pod 注入环境变量：

```yaml
env:
- name: HTTP_PROXY
  value: "http://breakfix-proxy:<密码>@gateway-ecs-ip:3128"
- name: NO_PROXY
  value: "10.0.0.0/8,.internal,.local"
```

阿里云安全组限制 3128 端口仅允许 ACK 节点 ECS 的安全组访问，公网不可达。

### 阿里云生态内网端点

| 服务 | 网络 | 说明 |
|------|------|------|
| ACR | VPC 内网端点 | Docker pull 不走公网 |
| ACK API Server | 仅内网 | 不暴露公网端点 |

---

## 镜像策略

```
手动编写 Dockerfile
  │
  ▼
Build 镜像 → 推送到 ACR
  │
  ▼
用户 start: 从 ACR pull 镜像起 Pod（秒级）
```

统一基础镜像（Alpine/Ubuntu），含常用运维工具。每道题的差异通过 Dockerfile 层注入。后续 Agent 自动生成是第二阶段。

---

## 生命周期

```
用户执行 breakfix start <id>
  │
  ▼
API Server 创建 Namespace（首次）+ 创建 Pod
  │
  ▼
返回 instance-id
  │
  ▼
用户执行 breakfix ssh <instance-id>
  │  Teleport 认证 → exec 到 Pod
  ▼
交互 shell，用户做题
  │
  ├── 用户 exit → Pod 进入 draining（5 分钟定时器启动）
  │
  ├── breakfix ssh <id> → CLI 自动调 PingInstance → 重置为 running → 继续做题
  │
  ├── breakfix submit <id>
  │     │  kubectl cp verify.sh → kubectl exec → 退出码 = 判定结果
  │     ▼
  │   销毁 Pod + Namespace
  │
  └── 5 分钟到了无人连 → 自动销毁 Pod + Namespace
```

verify 脚本由 API Server 在 submit 时注入执行，用户全程看不到脚本内容。

### CLI 命令

| 命令 | 说明 |
|------|------|
| `breakfix login` | 浏览器 OAuth → Teleport 签发证书 → WhoAmI(mTLS) → 首次即注册 |
| `breakfix list` | 列出可用题目 |
| `breakfix start <id>` | 创建 Pod，返回 instance id。可同时起多个 |
| `breakfix ssh <id>` | 内嵌 tsh exec 到 Pod |
| `breakfix submit <id>` | kubectl cp 注入 verify.sh → exec → 拿退出码 → 销毁 |
| `breakfix stop <id>` | 放弃并立即销毁 |
| `breakfix status <id>` | 查看实例状态 |

---

## 多用户隔离

每个用户独立 Namespace，支持同时做多道题：

```
ACK
├── breakfix-challenges        ← 公共资源（题目镜像等）
├── user-github-what           ← 用户 what 的做题环境
│   ├── Pod abc123 (cleanup-logs)
│   └── Pod def456 (nginx-502)
├── user-github-other          ← 用户 other 的做题环境
│   └── Pod ghi789 (...)
└── ...
```

- 每个 Namespace 设 ResourceQuota，限制每人最多同时 3 个 Pod
- 用户退出/submit 后销毁对应 Pod，Namespace 在该用户最后一个 Pod 销毁时清理

---

## 认证：mTLS + Teleport CA

Teleport 本身就是 CA。安装时自动生成 CA 密钥对（`/etc/teleport/ca.pub` + `ca_key`），每次 `tsh login` 用它签发短期证书（12 小时）。

API Server 信任同一个 CA，验证所有 gRPC 请求的身份：

```
CLI                                    API Server
  │                                       │
  │  gRPC + Teleport 签发的客户端证书       │
  │ ──────────────────────────────────────▶
  │                                       │  验证: 证书由我信任的 CA 签发？
  │                                       │  验证: 证书过期了吗？
  │                                       │  提取: subject = "github-what"
  │                                       │
  │                                       │  身份已确认，不需要 user_id 参数
  │ ◀─────────────────────────────────────│
```

- CLI 所有 gRPC 请求都不带 `user_id`
- 身份在 TLS 握手阶段由证书确定，无法伪造
- 证书即使被偷，最长 12 小时后自动失效

---

## API Server 连接 ACK

API Server 通过 kubeconfig 文件连接 ACK API Server（内网端点），同 VPC 内网通信。

```
网关 ECS ──内网──▶ ACK API Server (内网端点)
           │
           └── kubeconfig 文件 (0600 权限)
```

---

## 网关 ECS 上运行的组件

| 组件 | 端口 | 用途 |
|------|------|------|
| API Server | 443 (gRPC+mTLS) | 处理 CLI 请求，嵌入式 SQLite，信任 Teleport CA |
| Teleport | 3023 (SSH), 3080 (Web) | CA + 认证 + SSH 代理 + 会话录制 |
| Tinyproxy | 3128 (内网) | 题目出网 HTTP 代理 |
| Nginx | 80/443 | CLI 二进制分发 |

---

## 题目存储

- **题目文件**：Git 仓库，每个题目一个子目录，含 `challenge.yaml` + Dockerfile + verify.sh
- **SQLite 只存索引**：id、title、difficulty、category、git_path，供 list 查询

```
git repo: breakfix/challenges
├── nginx-500-errors/
│   ├── challenge.yaml
│   ├── Dockerfile
│   └── verify.sh
├── zombie-processes/
│   ├── challenge.yaml
│   ├── Dockerfile
│   └── verify.sh
└── ...
```

> Agent 自动出题是第二阶段功能。当前所有题目手动编写，第一道题目手动跑通全流程后再启动 Agent 相关开发。

---

## 题目 Spec 格式

```yaml
id: nginx-500-errors
title: "找出 Nginx 日志中的 500 错误并打包压缩"
difficulty: easy
category: log-analysis
timeout: 1800            # 秒

description: |
  服务器上的 Nginx 产生了大量日志，你的任务是：
  1. 找出所有包含 500 状态码的日志行
  2. 将它们打包压缩为 /tmp/500-errors.tar.gz

setup:
  dockerfile: |
    FROM registry.cn-hangzhou.aliyuncs.com/breakfix/base:alpine-3.20
    ...

verify:
  script: |
    #!/bin/bash
    [ -f /tmp/500-errors.tar.gz ] && exit 0 || exit 1

judge_rubric:
  - 正确找到 500 错误日志行
  - 使用正确工具打包压缩
  - 输出路径正确
```

---

## 监控与健康检查

初期用最简单的方案：

| 层面 | 方式 |
|------|------|
| 进程存活 | `systemd Restart=always`，挂了自动拉 |
| gRPC 健康检查 | gRPC 内置 `/grpc.health.v1.Health/Check`，无需额外代码 |
| 磁盘告警 | 阿里云云监控，配 >80% 磁盘使用告警 |
| 日志 | `journald` 本地留存，不聚合 |

不做 Prometheus，不做 OpenTelemetry。够用就行，后续再加。

---

## 相关设计文档

| 文件 | 内容 |
|------|------|
| `DESIGN.md` | 本文档，基础设施架构 |
| `API_DESIGN.md` | 认证（mTLS）+ DB Schema + gRPC Proto + 流程 |
| `CHALLENGE_DESIGN.md` | 题目类型（Script / Break-Fix / Investigate）和 Spec |
| `DEV.md` | 本地开发指南（Kind + go build tags + Makefile） |
| `DEPLOY.md` | 部署指南（网关 ECS + ACK + ACR + Teleport） |

## 下一步

1. 搭开发环境（本地 Kind 集群模拟 ACK）
2. ~~选第一道题目~~（日志压缩题，确认题目 Spec）
3. 写 API Server 骨架（Go + gRPC+mTLS + SQLite + kubeconfig）
4. 对接 Teleport（Helm 装 K8s Plugin + 配 RBAC）
5. 写 CLI 骨架（Go + Cobra，内嵌 tsh）
6. GitHub Actions CI 打包 release
7. 手动做第一道题目跑通全流程
8. 第二阶段：Agent 自动出题 + 会话录制评判 + VM 题目

---

## 附录：未来 VM 题目支持

当平台需要支持必须使用虚拟机的题目（如内核模块、内核参数调优等）时，采用以下方案：

- **ECS**：使用阿里云支持嵌套虚拟化的 ECS 实例规格（非裸金属）：
  - ecs.c9i.8xlarge / ecs.c9i.16xlarge
  - ecs.g9i.8xlarge / ecs.g9i.16xlarge
  - ecs.r9i.8xlarge / ecs.r9i.16xlarge
  - 参考：[启用嵌套虚拟化](https://help.aliyun.com/zh/ecs/user-guide/enable-nested-virtualization)
- **KubeVirt**：管理 VM 生命周期，统一 Pod/VM 调度
- **节点池隔离**：VM 题目调度专用节点池（支持嵌套虚拟化的 ECS），配 taint/toleration，空闲时缩到零
- **镜像**：qcow2 存 OSS（VPC 内网端点），CDI 导入 PVC 模板，PVC 克隆秒级创建
