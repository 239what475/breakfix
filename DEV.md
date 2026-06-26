# Breakfix 本地开发指南

## 依赖

- Docker（用于 Teleport + Kind K8s 集群）
- Go 1.24+
- kubectl
- kind CLI
- openssl（生成证书）

## 快速开始

```bash
# 1. 启动 Teleport（docker compose）
make dev-up

# 2. 配置用户和证书（只需一次）
make dev-setup

# 3. 用户设置密码（打开输出的链接或运行）
docker compose -f docker-compose.dev.yml exec teleport tctl users reset dev-user

# 4. 浏览器登录 Teleport，下载客户端证书
open https://localhost:3080
# → 用 dev-user + 密码登录
# → Teleport 自动生成 ~/.tsh/keys/ 下的客户端证书

# 5. 启动 Kind 集群
make kind-up
make docker-base
make docker-challenge NAME=cleanup-logs

# 6. 编译并启动 API Server（mTLS 已启用）
make dev
make run-server
# → API Server 在 :9090 监听，必须 mTLS

# 7. CLI 操作
make run-cli list
make run-cli start cleanup-logs
make run-cli ssh <instance-id>
make run-cli submit <instance-id>
```

## 架构

```
docker compose (Teleport)
    └── Teleport (Auth + Proxy + CA)
            ├── Web UI: https://localhost:3080
            ├── CA: dev/certs/ca.pub
            └── 签发客户端证书 → ~/.tsh/keys/

Kind (K8s)
    └── Pod (题目容器)

API Server (本地进程，:9090)
    ├── mTLS：验证 Teleport CA 签发的客户端证书
    ├── gRPC：双向 TLS
    └── 管理 Kind 集群中的 Pod
```

## 证书体系

```
make dev-setup 生成:
  dev/certs/
  ├── ca.pub              ← Teleport CA 公钥
  ├── server-cert.pem     ← API Server 证书（Teleport CA 签发）
  └── server-key.pem      ← API Server 私钥

tsh login 生成:
  ~/.tsh/keys/localhost/dev-user
  └── (客户端证书)         ← CLI 连接时使用
```

## 常用命令

```bash
make dev-up        # 启动 Teleport
make dev-down      # 停止 Teleport
make dev           # 编译 server + CLI
make run-server    # 启动 API Server
make lint          # golangci-lint
make clean         # 清理构建产物和临时证书
make kind-up       # 创建 Kind 集群
make kind-down     # 删除 Kind 集群
```

## 构建题目镜像

```bash
cd challenges/<name>
docker build -t breakfix-<name>:dev .
kind load docker-image breakfix-<name>:dev --name breakfix-dev
```
