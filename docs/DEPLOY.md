# Breakfix 部署指南

## 前置条件

- 阿里云账号（科研包 2000 元抵扣金）
- 一台 ECS（2C2G，已备案域名）运行 Server 和 Controller
- 域名解析到该 ECS

---

## 0. 本地开发

### 0.1 一次性初始化

```bash
cp config/breakfix.example.yaml config/breakfix.yaml
make dev-config
```

前置依赖：Docker、Kind（集群名 `breakfix-dev`）。

### 0.2 日常命令

```bash
make dev            # 启动完整环境（registry + 镜像 + 编译 + Server + Controller）
make dev-server     # 改 Server 后重编译+重启（不动 DB，不用重登录）
make dev-controller # 改 Controller 后重编译+重启
make dev-down       # 停 Server、Controller 和 registry
make dev-reset      # 停所有 + 清 DB（需要重新 register → login）

make docker-challenge NAME=xxx  # 重建单个题目镜像
```

### 0.3 首次使用

```bash
make dev
```

打开 `http://localhost:9090`，在 Web UI 里完成注册、登录和启动题目；检查点全部通过后会自动完成。

> `make dev` 保留 DB 和 CA，之后 `make dev-server` 重启不需要重新登录。需要全新开始时用 `make dev-reset`。

---

## 1. Server 与 Controller 主机

### 1.1 创建用户和数据目录

```bash
sudo useradd -r -s /bin/false breakfix
sudo mkdir -p /var/lib/breakfix/challenges
sudo tee /var/lib/breakfix/breakfix.yaml <<EOF > /dev/null
data_dir: /var/lib/breakfix
kubeconfig: /var/lib/breakfix/kubeconfig
registry_addr: crpi-xxxx-vpc.cn-hangzhou.personal.cr.aliyuncs.com/breakfix
registry_insecure: false
server_host: <server-ecs-private-ip>
port: 9090
proxy_port: 3128
EOF
sudo chown -R breakfix:breakfix /var/lib/breakfix
```

### 1.2 安装 kubeconfig

集群 → 连接信息 → 复制 kubeconfig（内网接入）→ 上传到 Server 与 Controller 主机：

```bash
# 本地
scp kubeconfig <服务器名>:/tmp/kubeconfig

# Server 与 Controller 主机
sudo mv /tmp/kubeconfig /var/lib/breakfix/kubeconfig
sudo chown breakfix:breakfix /var/lib/breakfix/kubeconfig
```

### 1.3 安装二进制

**前置**：在本地仓库创建 `.breakfix-server` 文件，一行写服务器 SSH 主机名：
```bash
echo myserver2 > .breakfix-server
```

**方式一：GitHub Releases（推荐）**

```bash
sudo curl -Lo /usr/local/bin/breakfix-server \
  https://github.com/your-org/breakfix/releases/latest/download/breakfix-server-linux-amd64
sudo curl -Lo /usr/local/bin/breakfix-controller \
  https://github.com/your-org/breakfix/releases/latest/download/breakfix-controller-linux-amd64
sudo chown breakfix:breakfix /usr/local/bin/breakfix-server /usr/local/bin/breakfix-controller
```

**方式二：make deploy（本地构建 + 自动部署）**

```bash
make deploy             # 编译 → scp → 重启 Server 与 Controller
```

> 之后可用 `make deploy-server` 或 `make deploy-controller` 单独更新对应进程。

### 1.4 systemd

```bash
sudo tee /etc/systemd/system/breakfix-server.service <<'EOF' > /dev/null
[Unit]
Description=Breakfix Server
After=network.target

[Service]
Type=simple
User=breakfix
ExecStart=/usr/local/bin/breakfix-server -config /var/lib/breakfix/breakfix.yaml
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF

sudo systemctl daemon-reload
sudo systemctl enable --now breakfix-server

sudo tee /etc/systemd/system/breakfix-controller.service <<'EOF' > /dev/null
[Unit]
Description=Breakfix Controller
After=network.target

[Service]
Type=simple
User=breakfix
ExecStart=/usr/local/bin/breakfix-controller -config /var/lib/breakfix/breakfix.yaml
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF

sudo systemctl daemon-reload
sudo systemctl enable --now breakfix-controller
```

### 1.5 同步题目

```bash
# 推荐：直接用仓库内的 data/challenges 作为权威来源同步到远程
make deploy-catalog
```

这会把本地仓库中的 `data/challenges` 原子替换到远程 `data_dir/challenges`。

---

## 2. ACK 集群

### 2.1 创建集群

阿里云控制台 → 容器服务 → 创建 Kubernetes 集群 → ACK 托管版 Standard。

| 选项 | 选择 | 原因 |
|------|------|------|
| 集群类型 | ACK 托管版 Standard | 控制面免费 |
| Auto Mode | 关闭 | 需要 Pro 版，且 ContainerOS 不兼容 |
| VPC | Server 与 Controller 主机所在 VPC | 同内网通信 |
| Gateway API | 仅内网 | 安全，不暴露公网 |
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

记录实例的 **VPC 内网地址**（实例概览页）和命名空间，填入 `config/breakfix.yaml` 的 `registry_addr` 字段。格式：

```
crpi-xxxx-vpc.cn-hangzhou.personal.cr.aliyuncs.com/breakfix
```

### 3.2 推送镜像

```bash
# 首次需要 docker login（使用公网地址，去掉 -vpc）:
docker login crpi-xxxx.cn-hangzhou.personal.cr.aliyuncs.com

# 之后用 make 自动构建 + 推送:
make deploy-image NAME=cleanup-logs      # 推送单个镜像
make deploy-images                       # 推送全部镜像
```

`deploy-image` 自动读取 `config/breakfix.yaml` 中的 VPC 地址，去掉 `-vpc` 得到公网推送地址。

> 注意：题目 `challenge.yaml` 中的 `image` 只需写 `cleanup-logs:v1`，Controller 会自动拼上 `registry_addr` 前缀。

---

## 4. 日常远程部署

```bash
make deploy               # 全量部署（镜像 + challenge catalog + 二进制）
make deploy-server        # 只推 Server 二进制
make deploy-controller    # 只推 Controller 二进制
make deploy-catalog       # 只同步题目目录
make deploy-image NAME=xxx  # 只推单个镜像
make deploy-images        # 只推全部镜像
make deploy-cleanup       # 清理 ACK 中残留的 break* 命名空间
make deploy-reset         # 清理 K8s + 清远程 DB + 重启（彻底重置）

make status               # 查看远程服务状态
make logs                 # 查看远程实时日志
```

> 远端 `/var/lib/breakfix/breakfix.yaml` 只在服务器上维护，不通过仓库中的 Makefile 覆盖。

---

## 5. 面试者入口

面试者直接通过浏览器访问 Server 地址，使用 Web UI 完成注册、登录和启动题目；检查点全部通过后会自动完成。

---

## 验证

```bash
make status                                  # active
```

然后用浏览器打开 `http://<ecs-ip>:9090` 验证注册、登录、启动题目、终端连接和检查点自动完成流程。
