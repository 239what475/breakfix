# Breakfix 部署指南

## 前置条件

- 阿里云账号（科研包 2000 元抵扣金）
- 一台 ECS（2C2G，已备案域名）作为网关
- 域名解析到网关 ECS

---

## 1. 网关 ECS

### 1.1 安装 Go

```bash
wget https://go.dev/dl/go1.24.linux-amd64.tar.gz
sudo tar -C /usr/local -xzf go1.24.linux-amd64.tar.gz
export PATH=$PATH:/usr/local/go/bin
```

### 1.2 编译 API Server

```bash
git clone https://github.com/your-org/breakfix.git
cd breakfix
go build -ldflags "-s -w -X github.com/breakfix/breakfix/internal/build.Version=v0.1.0" -o /usr/local/bin/breakfix-api ./cmd/server
```

### 1.3 配置

```bash
mkdir -p /var/lib/breakfix/challenges
cat > /var/lib/breakfix/breakfix.yaml <<EOF
data_dir: /var/lib/breakfix
port: 9090
mtls_port: 9533
proxy_port: 3128
EOF
```

将 ACK kubeconfig 放到 `/var/lib/breakfix/kubeconfig`（或任意路径，在 yaml 中指定）。

### 1.4 systemd

```bash
cat > /etc/systemd/system/breakfix-api.service <<'EOF'
[Unit]
Description=Breakfix API Server
After=network.target

[Service]
Type=simple
User=root
ExecStart=/usr/local/bin/breakfix-api -config /var/lib/breakfix/breakfix.yaml
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable --now breakfix-api
```

### 1.5 同步题目

```bash
git clone https://github.com/your-org/breakfix-challenges.git /var/lib/breakfix/challenges
```

---

## 2. ACK 集群

### 2.1 创建集群

阿里云控制台 → 容器服务 → 创建 ACK 托管版 (Standard)，仅内网 API Server。

### 2.2 创建节点池

- 扩缩容：自动，最少 1 台
- 规格：`ecs.u1-c1m4.xlarge`（4C16G）按量付费
- OS：Alibaba Cloud Linux 3

### 2.3 获取 kubeconfig

下载 kubeconfig 放到网关 ECS 指定路径。

---

## 3. ACR 镜像仓库

创建命名空间 `breakfix`。

```bash
docker login registry.cn-hangzhou.aliyuncs.com
docker tag breakfix-base:latest registry.cn-hangzhou.aliyuncs.com/breakfix/base:latest
docker push registry.cn-hangzhou.aliyuncs.com/breakfix/base:latest
```

---

## 4. CLI 分发

GitHub Releases 发布编译好的二进制：

```bash
# 用户下载
curl -Lo /usr/local/bin/breakfix https://github.com/your-org/breakfix/releases/latest/download/breakfix-cli-linux-amd64
chmod +x /usr/local/bin/breakfix
```

---

## 验证

```bash
systemctl status breakfix-api            # active
breakfix register -u test -p test123     # QR 码出现
breakfix login -u test -p test123 -t <totp>
breakfix list
breakfix start cleanup-logs
breakfix ssh <id>
breakfix submit <id>
```
