# Breakfix 部署指南

## 前置条件

- 阿里云账号，已完成实名认证和学生认证（科研包 2000 元抵扣金）
- 一台 ECS（2C2G，已有域名备案，作为网关）
- 域名 `breakfix.your-domain.com` 解析到网关 ECS
- GitHub OAuth App（用于 Teleport 用户登录）：在 GitHub Settings → Developer settings → OAuth Apps 创建

---

## 1. 网关 ECS 初始化

SSH 到网关 ECS，执行以下步骤。

### 1.1 安装基础软件

```bash
sudo apt update && sudo apt install -y nginx tinyproxy git curl
sudo systemctl enable --now nginx
sudo systemctl enable --now tinyproxy
```

### 1.2 安装 Teleport

```bash
# 添加 Teleport 仓库（现代 Debian/Ubuntu 方式）
curl -fsSL https://deb.releases.teleport.dev/teleport-pubkey.asc \
  | sudo gpg --dearmor -o /etc/apt/keyrings/teleport.gpg
echo "deb [signed-by=/etc/apt/keyrings/teleport.gpg] https://deb.releases.teleport.dev stable main" \
  | sudo tee /etc/apt/sources.list.d/teleport.list
sudo apt update

# 安装
sudo apt install -y teleport

# 生成配置模板
sudo teleport configure --output-file=/etc/teleport/teleport.yaml
```

编辑 `/etc/teleport/teleport.yaml`：

```yaml
version: v3

teleport:
  nodename: breakfix-gateway
  data_dir: /var/lib/teleport
  log:
    output: stderr
    severity: INFO

auth_service:
  enabled: yes
  cluster_name: breakfix.your-domain.com
  listen_addr: 0.0.0.0:3025
  proxy_listener_mode: multiplex
  authentication:
    type: github

ssh_service:
  enabled: yes

proxy_service:
  enabled: yes
  web_listen_addr: 0.0.0.0:3080
  public_addr: teleport.your-domain.com:443
  ssh_public_addr: teleport.your-domain.com:3023
  kube_listen_addr: 0.0.0.0:3026
  kube_public_addr: teleport.your-domain.com:3026
```

启动 Teleport：

```bash
sudo systemctl enable --now teleport
```

#### 1.2.1 配置 GitHub OAuth

创建 `/etc/teleport/github-connector.yaml`：

```yaml
kind: github
version: v3
metadata:
  name: github
spec:
  client_id: <your-github-oauth-app-client-id>
  client_secret: <your-github-oauth-app-client-secret>
  redirect_url: https://teleport.your-domain.com/v1/webapi/github/callback
  teams_to_roles:
    - organization: <your-github-org>
      team: <your-team>
      roles:
        - access
```

应用：

```bash
sudo tctl create -f /etc/teleport/github-connector.yaml
```

### 1.3 配置 Tinyproxy

编辑 `/etc/tinyproxy/tinyproxy.conf`：

```ini
Port 3128
Listen 0.0.0.0

# 只允许 ACK Pod 网段（填实际的 Pod CIDR）
Allow 127.0.0.1
Allow 10.244.0.0/16

# 并发限制
MaxClients 20

# 基础认证（用户名和密码空格分隔）
BasicAuth breakfix-proxy <随机密码>

# 不暴露自己是代理
DisableViaHeader Yes
```

生成随机密码并写入配置：

```bash
# 生成密码
PASSWORD=$(openssl rand -hex 16)

# 修改配置中的密码行
sudo sed -i "s/BasicAuth breakfix-proxy .*/BasicAuth breakfix-proxy $PASSWORD/" /etc/tinyproxy/tinyproxy.conf
```

记录密码供后面 Pod 环境变量使用。

重启：

```bash
sudo systemctl restart tinyproxy
```

### 1.4 创建 breakfix 用户和数据目录

```bash
sudo useradd -r -s /bin/false breakfix
sudo mkdir -p /etc/breakfix /var/lib/breakfix/challenges
sudo chown -R breakfix:breakfix /etc/breakfix /var/lib/breakfix
```

### 1.5 API Server 配置

API Server 使用命令行 flag，不需要 YAML 配置文件。直接配 systemd unit：

```bash
sudo tee /etc/systemd/system/breakfix-api.service <<'EOF'
[Unit]
Description=Breakfix API Server
After=network.target

[Service]
Type=simple
User=breakfix
ExecStart=/usr/local/bin/breakfix-api \
  --port=9090 \
  --db=/var/lib/breakfix/breakfix.db \
  --challenges=/var/lib/breakfix/challenges
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF
```

> 注意：API Server 目前使用命令行 flag，不是 YAML 配置。`-X main.Mode=prod` 在编译时通过 ldflags 设置。

### 1.6 安装 API Server

```bash
# 从 GitHub Releases 下载
curl -Lo /usr/local/bin/breakfix-api https://github.com/your-org/breakfix/releases/latest/download/breakfix-api-linux-amd64
chmod +x /usr/local/bin/breakfix-api

sudo systemctl daemon-reload
sudo systemctl enable --now breakfix-api
```

### 1.7 同步题目

```bash
sudo -u breakfix git clone https://github.com/your-org/breakfix-challenges.git /var/lib/breakfix/challenges
```

### 1.8 配置 Nginx（CLI 二进制分发）

```bash
sudo tee /etc/nginx/sites-available/breakfix <<'EOF'
server {
    listen 80;
    server_name breakfix.your-domain.com;

    location /cli/ {
        alias /var/www/breakfix/releases/;
        autoindex on;
    }
}
EOF

sudo ln -sf /etc/nginx/sites-available/breakfix /etc/nginx/sites-enabled/
sudo mkdir -p /var/www/breakfix/releases
sudo systemctl reload nginx
```

---

## 2. 阿里云 ACK 集群

### 2.1 创建集群

在阿里云控制台：

1. 容器服务 → 创建 Kubernetes 集群 → **ACK 托管版 (Standard)**
2. API Server 端点：**仅内网访问**
3. 选择网关 ECS 所在的 VPC 和交换机
4. 创建

### 2.2 创建节点池

1. 集群 → 节点池 → 创建节点池
2. 扩缩容模式：**自动**
3. 最小节点数：1，最大按需
4. 实例规格：按量付费，`ecs.u1-c1m4.xlarge`（4C16G）
5. 操作系统：Alibaba Cloud Linux 3
6. 节点自定义数据（用于配置镜像加速）：

```bash
#!/bin/bash
mkdir -p /etc/containerd
cat >> /etc/containerd/config.toml <<'CONF'
[plugins."io.containerd.grpc.v1.cri".registry.mirrors]
  [plugins."io.containerd.grpc.v1.cri".registry.mirrors."docker.io"]
    endpoint = ["https://registry.cn-hangzhou.aliyuncs.com/breakfix-mirror"]
CONF
systemctl restart containerd
```

### 2.3 获取 kubeconfig

1. 集群 → 连接信息 → 复制内网接入地址
2. 在阿里云控制台下载 kubeconfig 文件

在网关 ECS 上：

```bash
sudo mkdir -p /etc/breakfix
# 将下载的 kubeconfig 拷贝到网关 ECS 的 /etc/breakfix/kubeconfig
sudo chmod 600 /etc/breakfix/kubeconfig
sudo chown breakfix:breakfix /etc/breakfix/kubeconfig
```

---

## 3. ACR 容器镜像仓库

### 3.1 创建个人版仓库

容器镜像服务 ACR → 创建命名空间 `breakfix` → 记录仓库地址 `registry.cn-hangzhou.aliyuncs.com/breakfix`

> ACR 个人版免费，VPC 内网可直接访问。

### 3.2 推送基础镜像

在开发机上：

```bash
docker login registry.cn-hangzhou.aliyuncs.com
docker tag breakfix-base:latest registry.cn-hangzhou.aliyuncs.com/breakfix/base:latest
docker push registry.cn-hangzhou.aliyuncs.com/breakfix/base:latest
```

---

## 4. Teleport 对接 ACK

### 4.1 安装 Teleport K8s Agent

```bash
# 添加 Teleport Helm 仓库
helm repo add teleport https://charts.releases.teleport.dev
helm repo update

# 生成 K8s join token
sudo tctl tokens add --type=kube --ttl=8760h

# 安装
helm install teleport-kube-agent teleport/teleport-kube-agent \
  --namespace teleport-agent \
  --create-namespace \
  --set roles=kube \
  --set proxyAddr=teleport.your-domain.com:443 \
  --set authToken=<上面生成的 token> \
  --set kubeClusterName=breakfix-ack
```

### 4.2 验证

```bash
# 管理员本地登录 Teleport 并注册 K8s 集群
tsh login --proxy=teleport.your-domain.com:443
tsh kube login breakfix-ack

# 验证可以访问 Pod
kubectl exec -ti <pod-name> -n <namespace> -- /bin/sh
```

---

## 5. 验证部署

### 5.1 检查组件

```bash
systemctl status teleport nginx tinyproxy breakfix-api
# 全部应为 active (running)
```

### 5.2 检查 API Server gRPC

```bash
curl -s http://localhost:9090/grpc.health.v1.Health/Check
# 或使用 grpcurl（如果安装）
```

### 5.3 完整流程测试

```bash
# 1. 用户下载 CLI
curl -o /usr/local/bin/breakfix https://breakfix.your-domain.com/cli/breakfix-linux-amd64
chmod +x /usr/local/bin/breakfix

# 2. 测试全链路
breakfix login
breakfix list
breakfix start cleanup-logs
breakfix ssh abc123
# ... 做题 ...
breakfix submit abc123
```

---

## 6. 镜像构建（出题时）

在开发机上，每道题目目录里：

```bash
cd challenges/cleanup-logs
docker build -t registry.cn-hangzhou.aliyuncs.com/breakfix/cleanup-logs:v1 .
docker push registry.cn-hangzhou.aliyuncs.com/breakfix/cleanup-logs:v1
```

---

## 快速检查清单

- [ ] 网关 ECS：nginx + tinyproxy + teleport + breakfix-api 全部 active
- [ ] Teleport：GitHub OAuth 登录正常
- [ ] Teleport K8s：`tsh kube login breakfix-ack` 成功
- [ ] ACK：节点池 Running，Pod 可创建
- [ ] ACR：镜像可 pull（`docker pull registry.cn-hangzhou.aliyuncs.com/breakfix/base:latest`）
- [ ] API Server：gRPC health check 正常
- [ ] CLI：`breakfix login` → `start` → `ssh` → `submit` 全链路通
