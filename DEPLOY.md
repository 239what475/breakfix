# Breakfix 部署指南

## 前置条件

- 阿里云账号（科研包 2000 元抵扣金）
- 一台 ECS（2C2G，已备案域名）作为网关
- 域名解析到网关 ECS

---

## 1. 网关 ECS

### 1.1 安装 API Server

```bash
sudo curl -Lo /usr/local/bin/breakfix-api \
  https://github.com/your-org/breakfix/releases/latest/download/breakfix-api-linux-amd64
sudo chmod +x /usr/local/bin/breakfix-api
sudo chown breakfix:breakfix /usr/local/bin/breakfix-api
```

### 1.3 创建用户和数据目录

```bash
sudo useradd -r -s /bin/false breakfix
sudo mkdir -p /var/lib/breakfix/challenges
sudo tee /var/lib/breakfix/breakfix.yaml <<EOF > /dev/null
data_dir: /var/lib/breakfix
	kubeconfig: /var/lib/breakfix/kubeconfig
port: 9090
mtls_port: 9533
proxy_port: 3128
EOF
sudo chown -R breakfix:breakfix /var/lib/breakfix
```

将 ACK kubeconfig 放到 `/var/lib/breakfix/kubeconfig`。

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
sudo git clone https://github.com/your-org/breakfix-challenges.git /var/lib/breakfix/challenges
sudo chown -R breakfix:breakfix /var/lib/breakfix/challenges
# 或手动解压题目 tar 包
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
| 可观测监控 | 开基础版 | 免费指标 |

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

> 自动扩缩容模式下无需设置期望节点数，直接配最小 1 最大 2 即可。

### 2.3 获取 kubeconfig

集群 → 连接信息 → 复制 kubeconfig（内网接入）→ 拷贝到网关 ECS：

```bash
# 本地
scp ~/.kube/config <服务器名>:~/.kube/config

# 或放到指定路径
sudo mkdir -p /var/lib/breakfix
scp kubeconfig <服务器名>:/tmp/kubeconfig
sudo mv /tmp/kubeconfig /var/lib/breakfix/kubeconfig
sudo chown breakfix:breakfix /var/lib/breakfix/kubeconfig
```

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
sudo systemctl status breakfix-api            # active
breakfix register -u test -p test123     # QR 码出现
breakfix login -u test -p test123 -t <totp>
breakfix list
breakfix start cleanup-logs
breakfix ssh <id>
breakfix submit <id>
```
