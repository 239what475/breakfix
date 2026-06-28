# Breakfix 设计文档

运维/SRE 面试练习平台。类似 SadServers × LeetCode，修复系统故障而非写代码。

---

## 架构

```
用户 CLI (breakfix)
    │
    │  gRPC :9090 TLS (Register/Login 无需客户端证书, 其他操作 mTLS)
    ▼
┌────────────────────────────────────────────────────────┐
│  网关 ECS / 开发机                                      │
│                                                        │
│  API Server (单一 Go 二进制)                             │
│    ├── :9090 gRPC TLS  — 所有 API (单端口)              │
│    ├── :3128 HTTP proxy  — Pod 出网 (goproxy)           │
│    ├── CA (crypto/x509)  — 签发客户端证书               │
│    ├── TOTP (pquerna/otp) — Google Authenticator 2FA   │
│    ├── client-go         — K8s Pod 管理 + PTY exec     │
│    └── SQLite            — 嵌入式数据库                  │
│                                                        │
└──────────┬─────────────────────────────────────────────┘
           │ 内网 VPC
           ▼
┌────────────────────────────────────────────────────────┐
│  ACK K8s 集群 (Data Plane)                              │
│                                                        │
│  Nodes: 无公网 IP                                       │
│                                                        │
│  ┌──────────────────────────────────────────┐          │
│  │  user-xxx (Namespace)                    │          │
│  │  ├── Pod abc123 (cleanup-logs)           │          │
│  │  └── Pod def456 (nginx-502)              │          │
│  │  └── Generator Job (agent workflow)      │          │
│  └──────────────────────────────────────────┘          │
│                                                        │
│  每个用户独立 Namespace，退出即销毁                       │
└────────────────────────────────────────────────────────┘
           │
           ▼
┌──────────────────┐
│  ACR (VPC 内网)   │
│  容器镜像          │
└──────────────────┘
```

---

## ECS 资源规划

| 机器 | 规格 | 计费 | 说明 |
|------|------|------|------|
| 网关 ECS | 2C2G | 已有 | 常驻，跑 API Server |
| ACK 节点 | 4C8G（默认 1 台） | 按量付费 | 自动扩缩，最少 1 |

---

## 技术选型

| 层 | 选型 | 说明 |
|------|------|------|
| 语言 | Go | 两个二进制：server + CLI |
| 容器编排 | ACK Standard | 免费控制面 |
| 数据库 | SQLite (WAL) | 嵌入 API Server |
| 镜像仓库 | ACR 个人版 | VPC 内网，免费 |
| 认证 | CA (crypto/x509) + TOTP (pquerna/otp) | 内置，无外部依赖 |
| API | gRPC TLS（可选 mTLS） | 单端口 9090 |
| PTY | client-go remotecommand | 无 kubectl 依赖 |
| 出网代理 | goproxy | 嵌入 API Server，1Mb/s 限速 |
| CLI | Cobra | register/login/list/start/ssh/submit/stop/status |
| 构建 | GitHub Actions | 版本 tag 触发 release |
| Agent | eino + Claude Code + DeepSeek v4 pro | K8s Job 内自动生成题目 |

---

## 用户流程

```
breakfix register -u user -p pass
  → 终端显示 QR 码（ANSI 背景色）
  → 手机扫入 Authenticator
  → 保存 CA 公钥到 ~/.breakfix/

breakfix login -u user -p pass -t <totp>
  → 验证密码 + TOTP → API Server 签发客户端证书
  → 证书存 ~/.breakfix/

breakfix list / start / ssh / submit / stop / status
  → mTLS gRPC (port 9090)
```

---

## 网络

### Ingress

```
用户 → 公网 → 网关 ECS (:9090 TLS)
                     │
               VPC 内网
                     │
                ACK Pod
```

### Egress

| 场景 | 方案 |
|------|------|
| Pod pull 镜像 | ACR VPC 内网 |
| 题目需要出网 | Pod 设 HTTP_PROXY → API Server :3128 (goproxy) |

---

## 数据目录

```
/var/lib/breakfix/
├── breakfix.db        # SQLite
├── ca-cert.pem        # CA 证书（重启不变）
├── ca-key.pem         # CA 私钥
└── challenges/        # 题目
```

## 配置文件

```yaml
# breakfix.yaml
data_dir: /var/lib/breakfix
port: 9090
proxy_port: 3128
kubeconfig: /var/lib/breakfix/kubeconfig
registry: crpi-xxxx-vpc.cn-hangzhou.personal.cr.aliyuncs.com
acr_namespace: break-fix
```

---

## 题目

见 CHALLENGE_DESIGN.md。

## API

见 API_DESIGN.md。

## 部署

见 DEPLOY.md。

## Agent 工作流

见 AGENT_WORKFLOW.md。
