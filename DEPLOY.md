# Breakfix 部署指南

## 前置条件

- 阿里云账号（科研包 2000 元抵扣金）
- 一台 ECS（2C2G，已备案域名）作为网关
- 域名解析到网关 ECS

---

## 0. 本地开发

### 0.1 一次性初始化

```bash
cp breakfix.example.yaml breakfix.yaml        # 编辑填入生产配置
```

前置依赖：Docker、Kind（集群名 `breakfix-dev`）。

### 0.2 日常命令

```bash
make dev            # 启动完整环境（registry + 镜像 + 编译 + server）
make dev-server     # 改代码后重编译+重启（不动 DB，不用重登录）
make dev-down       # 停 server + 停 registry
make dev-reset      # 停所有 + 清 DB（需要重新 register → login）

make docker-challenge NAME=xxx  # 重建单个题目镜像
```

### 0.3 首次使用

```bash
make dev
./bin/breakfix-cli register -u test -p pass123   # 扫 QR 码
./bin/breakfix-cli login    -u test -p pass123 -t <totp>
./bin/breakfix-cli list
./bin/breakfix-cli start cleanup-logs
./bin/breakfix-cli submit <instance-id>
```

> `make dev` 保留 DB 和 CA，之后 `make dev-server` 重启不需要重新登录。需要全新开始时用 `make dev-reset`。

---

## 1. 网关 ECS

### 1.1 创建用户和数据目录

```bash
sudo useradd -r -s /bin/false breakfix
sudo mkdir -p /var/lib/breakfix/challenges
sudo tee /var/lib/breakfix/breakfix.yaml <<EOF > /dev/null
data_dir: /var/lib/breakfix
kubeconfig: /var/lib/breakfix/kubeconfig
registry: crpi-xxxx-vpc.cn-hangzhou.personal.cr.aliyuncs.com
	acr_namespace: breakfix
port: 9090
mtls_port: 9533
proxy_port: 3128
EOF
sudo chown -R breakfix:breakfix /var/lib/breakfix
```

### 1.2 安装 kubeconfig

集群 → 连接信息 → 复制 kubeconfig（内网接入）→ 上传到网关：

```bash
# 本地
scp kubeconfig <服务器名>:/tmp/kubeconfig

# 网关 ECS
sudo mv /tmp/kubeconfig /var/lib/breakfix/kubeconfig
sudo chown breakfix:breakfix /var/lib/breakfix/kubeconfig
```

### 1.3 安装 API Server

**前置**：在本地仓库创建 `.breakfix-server` 文件，一行写服务器 SSH 主机名：
```bash
echo myserver2 > .breakfix-server
```

**方式一：GitHub Releases（推荐）**

```bash
sudo curl -Lo /usr/local/bin/breakfix-api \
  https://github.com/your-org/breakfix/releases/latest/download/breakfix-api-linux-amd64
sudo chown breakfix:breakfix /usr/local/bin/breakfix-api
```

**方式二：make deploy-server（本地构建 + 自动部署）**

```bash
make deploy-server     # 编译 → scp → systemctl restart，一条命令
```

> 之后每次改代码，只需 `make deploy-server` 即可更新远程服务端。

### 1.4 systemd

```bash
sudo tee /etc/systemd/system/breakfix-api.service <<'EOF' > /dev/null
[Unit]
Description=Breakfix API Server
After=network.target

[Service]
Type=simple
User=breakfix
ExecStart=/usr/local/bin/breakfix-api -config /var/lib/breakfix/breakfix.yaml
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF

sudo systemctl daemon-reload
sudo systemctl enable --now breakfix-api
```

### 1.5 同步题目

```bash
# 方式一：Git 克隆
sudo git clone https://github.com/your-org/breakfix-challenges.git /var/lib/breakfix/challenges
sudo chown -R breakfix:breakfix /var/lib/breakfix/challenges

# 方式二：本地 scp 上传
# scp -r ./challenges <服务器名>:/tmp/
# ssh <服务器名> sudo mv /tmp/challenges/* /var/lib/breakfix/challenges/
# ssh <服务器名> sudo chown -R breakfix:breakfix /var/lib/breakfix/challenges
```

---

## 2. ACK 集群

### 2.1 创建集群

阿里云控制台 → 容器服务 → 创建 Kubernetes 集群 → ACK 托管版 Standard。

| 选项 | 选择 | 原因 |
|------|------|------|
| 集群类型 | ACK 托管版 Standard | 控制面免费 |
| Auto Mode | 关闭 | 需要 Pro 版，且 ContainerOS 不兼容 |
| VPC | 网关 ECS 所在 VPC | 同内网通信 |
| API Server | 仅内网 | 安全，不暴露公网 |
| SNAT | 不配 | 镜像走 ACR VPC 内网，不需要出网 |
| 网络插件 | Flannel | 简单，无特殊网络需求 |
| IPv6 双栈 | 不开 | 全内网 IPv4 |
| 服务转发模式 | IPVS | 默认 |
| 集群本地域名 | cluster.local | 默认 |
| 容器镜像加速 | 不开 | 镜像全在 ACR VPC 内网 |
| Prometheus | 基础版 | 免费，可学习 |
| GOATScaler | 不开 | 单节点用不上 |

### 2.2 创建节点池

| 选项 | 选择 | 原因 |
|------|------|------|
| 托管配置 | 普通节点池（不开托管） | 单节点，避免自动升级重启断线 |
| 实例规格 | `ecs.e-c1m2.xlarge`（4C8G） | 科研包可抵扣 |
| 计费 | 按量付费 | 抵扣金 |
| 操作系统 | Alibaba Cloud Linux 4（容器优化版） | |
| 系统盘 | 40GB ESSD Entry（默认） | 科研包赠送 |
| 数据盘 | 不配 | 省空间 |
| 最小/最大节点数 | 1 / 2 | 自动扩缩 |
| 节点 Pod 数量 | 64 | 默认够用 |
| 登录方式 | 创建后设置 | 不需要 SSH 到节点 |
| 自定义数据 | 留空 | 不需要 |

---

## 3. ACR 镜像仓库

### 3.1 创建命名空间

ACR 控制台 → 个人版实例 → 创建命名空间 `breakfix`。

记录实例的 **VPC 内网地址**（实例概览页），填入 `breakfix.yaml` 的 `registry` 字段。格式：

```
crpi-xxxx-vpc.cn-hangzhou.personal.cr.aliyuncs.com
```

### 3.2 推送镜像

```bash
# 首次需要 docker login（使用公网地址，去掉 -vpc）:
docker login crpi-xxxx.cn-hangzhou.personal.cr.aliyuncs.com

# 之后用 make 自动构建 + 推送:
make deploy-image NAME=cleanup-logs      # 推送单个镜像
make deploy-images                       # 推送全部镜像
```

`deploy-image` 自动读取 `breakfix.yaml` 中的 VPC 地址，去掉 `-vpc` 得到公网推送地址。

> 注意：题目 `challenge.yaml` 中的 `image` 只需写 `cleanup-logs:v1`，Server 会自动拼上 `{registry}/{acr_namespace}/` 前缀。

---

## 4. 日常远程部署

```bash
make deploy               # 全量部署（镜像 → ACR + 二进制 → ECS）
make deploy-server        # 只推二进制
make deploy-image NAME=xxx  # 只推单个镜像
make deploy-images        # 只推全部镜像
make deploy-cleanup       # 清理 ACK 中残留的 break* 命名空间
make deploy-reset         # 清理 K8s + 清远程 DB + 重启（彻底重置）

make status               # 查看远程服务状态
make logs                 # 查看远程实时日志
```

---

## 5. CLI 分发（给面试者）

CLI 是面试者在自己电脑上用的，用于注册、登录、连接题目环境。**不要装到服务器上**。

**方式一：GitHub Releases**

面试者在自己的机器上执行：
```bash
curl -Lo /usr/local/bin/breakfix https://github.com/your-org/breakfix/releases/latest/download/breakfix-cli-linux-amd64
chmod +x /usr/local/bin/breakfix
```

**方式二：本地构建**

```bash
go build -ldflags "-s -w -X github.com/breakfix/breakfix/internal/build.Version=v0.1.0" \
  -o dist/breakfix-cli-linux-amd64 ./cmd/cli
```

> 跨平台构建由 GitHub Actions 的 release workflow 处理，本地不需要手动设 GOOS/GOARCH。

### 连接远程服务器

CLI 通过 `--server` 指定网关 ECS 地址，端口自动推导（9090 明文 / 9533 mTLS）：

```bash
breakfix --server <ecs-公网IP> register -u user -p pass
breakfix --server <ecs-公网IP> login -u user -p pass -t <totp>
breakfix --server <ecs-公网IP> list
breakfix --server <ecs-公网IP> start cleanup-logs
```

---

## 验证

```bash
make status                                  # active
breakfix --server <ecs-ip> register -u test -p test123
breakfix --server <ecs-ip> login -u test -p test123 -t <totp>
breakfix --server <ecs-ip> list
breakfix --server <ecs-ip> start cleanup-logs
breakfix --server <ecs-ip> ssh <id>
breakfix --server <ecs-ip> submit <id>
```
