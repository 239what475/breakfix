# Breakfix 部署指南

## 前置条件

- 阿里云账号（科研包 2000 元抵扣金）
- 一台 ECS（2C2G，已备案域名）作为网关
- 域名解析到网关 ECS

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

**方式一：GitHub Releases（推荐）**

```bash
sudo curl -Lo /usr/local/bin/breakfix-api \
  https://github.com/your-org/breakfix/releases/latest/download/breakfix-api-linux-amd64
sudo chown breakfix:breakfix /usr/local/bin/breakfix-api
```

**方式二：本地构建上传**

```bash
# 本地
go build -ldflags "-s -w -X github.com/breakfix/breakfix/internal/build.Version=v0.1.0" \
  -o dist/breakfix-api-linux-amd64 ./cmd/server

scp dist/breakfix-api-linux-amd64 <服务器名>:~/

# 网关 ECS
sudo mv ~/breakfix-api-linux-amd64 /usr/local/bin/breakfix-api
sudo chown breakfix:breakfix /usr/local/bin/breakfix-api
```

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
# 登录（公网）
docker login crpi-xxxx.cn-hangzhou.personal.cr.aliyuncs.com

# 构建 + 推送
docker build -t breakfix-cleanup-logs:v1 ./challenges/cleanup-logs
docker tag breakfix-cleanup-logs:v1 crpi-xxxx.cn-hangzhou.personal.cr.aliyuncs.com/breakfix/cleanup-logs:v1
docker push crpi-xxxx.cn-hangzhou.personal.cr.aliyuncs.com/breakfix/cleanup-logs:v1
```

> 注意：题目 `challenge.yaml` 中的 `image` 只需写 `breakfix/cleanup-logs:v1`，Server 会自动拼上配置中的 `registry` 前缀。

---

## 4. CLI 分发

**方式一：GitHub Releases**

```bash
curl -Lo /usr/local/bin/breakfix https://github.com/your-org/breakfix/releases/latest/download/breakfix-cli-linux-amd64
chmod +x /usr/local/bin/breakfix
```

**方式二：本地构建上传**

```bash
# 本地
go build -ldflags "-s -w -X github.com/breakfix/breakfix/internal/build.Version=v0.1.0" \
  -o dist/breakfix-cli-linux-amd64 ./cmd/cli

scp dist/breakfix-cli-linux-amd64 <服务器名>:/usr/local/bin/breakfix
```

---

## 验证

```bash
sudo systemctl status breakfix-api            # active
breakfix register -u test -p test123          # QR 码出现
breakfix login -u test -p test123 -t <totp>
breakfix list
breakfix start cleanup-logs
breakfix ssh <id>
breakfix submit <id>
```
