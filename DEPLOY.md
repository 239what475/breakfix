# Breakfix 部署指南

## 前置条件

- 阿里云账号，已完成实名认证和学生认证（科研包 2000 元抵扣金）
- 一台 ECS（2C2G，已有域名备案，作为网关）
- 域名 `breakfix.your-domain.com` 解析到网关 ECS

---

## 1. 网关 ECS 初始化

SSH 到网关 ECS，执行以下步骤。

### 1.1 安装基础软件

```bash
# 更新系统 & 安装基础工具
sudo apt update && sudo apt install -y nginx tinyproxy git curl

# 启动 nginx（后面配 CLI 分发）
sudo systemctl enable --now nginx

# 启动 tinyproxy（先默认跑，后面配）
sudo systemctl enable --now tinyproxy
```

### 1.2 安装 Teleport

```bash
# 添加 Teleport 仓库
curl https://goteleport.com/static/teleport.repo | sudo tee /etc/yum.repos.d/teleport.repo

# 或 Ubuntu/Debian
curl https://deb.releases.teleport.dev/teleport-pubkey.asc | sudo apt-key add -
echo "deb https://deb.releases.teleport.dev stable main" | sudo tee /etc/apt/sources.list.d/teleport.list
sudo apt update

# 安装
sudo apt install -y teleport

# 配置 Teleport（单机模式，Auth + Proxy 合并）
sudo teleport configure --output-file=/etc/teleport/teleport.yaml
```

编辑 `/etc/teleport/teleport.yaml`：

```yaml
teleport:
  nodename: breakfix-gateway
  data_dir: /var/lib/teleport
  log:
    output: stderr
    severity: INFO

auth_service:
  enabled: yes
  cluster_name: breakfix
  tokens:
    - "proxy,node,app:breakfix-token"
  authentication:
    type: github
    github:
      client_id: <your-github-oauth-app-client-id>
      client_secret: <your-github-oauth-app-client-secret>
      display: GitHub
      teams_to_roles:
        - organization: <your-github-org>
          roles:
            - access

proxy_service:
  enabled: yes
  web_listen_addr: 0.0.0.0:3080
  public_addr: teleport.your-domain.com:443
  ssh_public_addr: teleport.your-domain.com:3023
  kube_listen_addr: 0.0.0.0:3026
  kube_public_addr: teleport.your-domain.com:3026
```

启动：

```bash
sudo systemctl enable --now teleport
```

### 1.3 配置 Tinyproxy

编辑 `/etc/tinyproxy/tinyproxy.conf`：

```ini
Port 3128
Listen 0.0.0.0

# 只允许 ACK Pod 网段（后面填实际的 Pod CIDR）
Allow 127.0.0.1
Allow 10.244.0.0/16

# 并发限制
MaxClients 20
MaxConnectionsPerHost 5

# 基础认证
BasicAuth breakfix-proxy <生成随机密码>

# 不暴露自己是代理
DisableViaHeader Yes
```

生成随机密码：

```bash
echo "breakfix-proxy:$(openssl rand -hex 16)" | sudo tee -a /etc/tinyproxy/tinyproxy.conf
```

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

### 1.5 配置 API Server

编辑 `/etc/breakfix/server.yaml`：

```yaml
grpc:
  port: 443
  server_cert: /etc/breakfix/server-cert.pem
  server_key:  /etc/breakfix/server-key.pem
  client_ca:   /etc/teleport/ca.pub

db:
  path: /var/lib/breakfix/breakfix.db

k8s:
  kubeconfig: /etc/breakfix/kubeconfig

challenges:
  git_path: /var/lib/breakfix/challenges

registry:
  url: registry.cn-hangzhou.aliyuncs.com/breakfix

teleport:
  proxy_addr: teleport.your-domain.com:443
```

API Server 服务端证书：让 Teleport CA 签发，或后面用 `tctl` 签。

### 1.6 安装 API Server

```bash
# 从 GitHub Releases 下载
curl -Lo /usr/local/bin/breakfix-api https://github.com/your-org/breakfix/releases/latest/download/breakfix-api-linux-amd64
chmod +x /usr/local/bin/breakfix-api

# 创建 systemd service
sudo tee /etc/systemd/system/breakfix-api.service <<'EOF'
[Unit]
Description=Breakfix API Server
After=network.target

[Service]
Type=simple
User=breakfix
ExecStart=/usr/local/bin/breakfix-api --config /etc/breakfix/server.yaml
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF

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

sudo ln -s /etc/nginx/sites-available/breakfix /etc/nginx/sites-enabled/
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
4. 实例规格：按量付费，`ecs.u1-c1m4.large`（4C16G）
5. 操作系统：Alibaba Cloud Linux 3
6. 节点自定义数据：

```bash
#!/bin/bash
# 镜像站加速
mkdir -p /etc/containerd
cat >> /etc/containerd/config.toml <<'CONF'
[plugins."io.containerd.grpc.v1.cri".registry.mirrors]
  [plugins."io.containerd.grpc.v1.cri".registry.mirrors."docker.io"]
    endpoint = ["https://registry.cn-hangzhou.aliyuncs.com/breakfix-mirror"]
CONF
systemctl restart containerd
```

### 2.3 获取 kubeconfig

集群 → 连接信息 → 复制内网 endpoint → 生成 kubeconfig：

```bash
# 在网关 ECS 上
sudo mkdir -p /etc/breakfix
# 从阿里云控制台下载 kubeconfig 并传到网关 ECS
sudo mv kubeconfig /etc/breakfix/kubeconfig
sudo chmod 600 /etc/breakfix/kubeconfig
sudo chown breakfix:breakfix /etc/breakfix/kubeconfig
```

---

## 3. ACR 容器镜像仓库

### 3.1 创建个人版仓库

容器镜像服务 → 创建命名空间 `breakfix` → 记录仓库地址 `registry.cn-hangzhou.aliyuncs.com/breakfix`

### 3.2 推送基础镜像

在开发机上：

```bash
docker login registry.cn-hangzhou.aliyuncs.com
docker tag breakfix-base:latest registry.cn-hangzhou.aliyuncs.com/breakfix/base:latest
docker push registry.cn-hangzhou.aliyuncs.com/breakfix/base:latest
```

---

## 4. Teleport 对接 ACK

### 4.1 安装 Teleport K8s Service

```bash
# 生成加入 token
tctl nodes add --roles=kube --ttl=8760h

# Helm 安装
helm install teleport-k8s teleport/teleport-kube-agent \
  --set proxyAddr=teleport.your-domain.com:443 \
  --set authToken=<上面生成的 token> \
  --set kubeClusterName=breakfix-ack \
  --create-namespace -n teleport
```

### 4.2 验证

```bash
# 确认可以 exec 到 Pod
tsh kube login breakfix-ack
tsh kubectl exec -ti <pod> -n <ns> -- /bin/sh
```

---

## 5. 验证部署

### 5.1 检查组件

```bash
systemctl status breakfix-api teleport nginx tinyproxy
# 全部 active (running)
```

### 5.2 检查 gRPC

```bash
grpcurl -cacert /etc/teleport/ca.pub \
  -cert ~/.tsh/keys/teleport.your-domain.com/user-cert.pub \
  -key ~/.tsh/keys/teleport.your-domain.com/user \
  gateway-ip:443 breakfix.Breakfix/WhoAmI
```

### 5.3 完整流程测试

```bash
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
- [ ] Teleport：OAuth 登录正常，`tsh kube login` 成功
- [ ] ACK：节点池 Running，Pod 可创建
- [ ] ACR：镜像可 pull
- [ ] API Server：gRPC 可用，mTLS 验证通过
- [ ] CLI：`breakfix login` → `start` → `ssh` → `submit` 全链路通
