# 端口、TCP 与 HTTP 服务

**Topic ID：**`ports-tcp-and-http`
**Domain：**Linux 系统与网络运维
**状态：**首轮核心场景设计，需 Nginx 和固定 HTTP 应用 capability probe

## 范围

本 Topic 从 socket、监听地址、TCP 连通性和 HTTP 状态中定位服务路径故障。题目以 `client -> gateway -> app` 的固定内部链路为边界，不引入公网入口、防火墙或复杂负载均衡。

## 建议前置知识

DNS 与名称解析；进程与 systemd 服务；基础网络诊断。

## 参考资料

- [`ss(8)`](https://man7.org/linux/man-pages/man8/ss.8.html)
- [`socket(7)`](https://man7.org/linux/man-pages/man7/socket.7.html)
- [HTTP Semantics](https://www.rfc-editor.org/rfc/rfc9110)
- [Nginx Beginner's Guide](https://nginx.org/en/docs/beginners_guide.html)

## 学习导读

### 学习目标

- 将 HTTP 5xx、TCP 连接错误、socket 监听地址和进程归属分层解释。
- 区分 app 本机成功、gateway 到 upstream 失败和 client 端到端失败。
- 在既有服务链路中修复监听或 upstream，而不是绕过 proxy。

### 核心心智模型

一个 HTTP 请求至少经过 client、gateway、TCP socket 和 app。502 往往表明 gateway 工作但 upstream 不可用；本机成功、远端失败通常指向 listener 的绑定范围，而非应用业务逻辑。

### 诊断证据与工具

从 client 的 HTTP 状态、gateway error log、gateway 到 app 的连接和 app 的 socket 状态逐段观察。每一段只证明自己这一跳，不能由一个 502 直接假定 Nginx 或 app 是唯一根因。

### 常见误区

- 将 app 改到旧端口来迎合错误 gateway 配置。
- 在 gateway 直接启动另一个 API 或返回静态成功响应。
- 用 DNS 或 hosts 修改掩盖 upstream/监听问题。

### 面试追问

- `Connection refused`、timeout 与 502 分别最可能位于请求路径的哪一段？
- 如何用 socket 状态确认服务绑定到 loopback 而不是私网接口？

### 推荐关系

建议在 DNS、systemd 和基础网络诊断之后学习；TLS Topic 建立在这条 HTTP 链路之上。

## 核心场景

### `repair-loopback-only-listener`：修复只绑定 loopback 的 API

- **环境地图：**`client -> gateway -> app`；app 上的 `orders-api` 应在私网 `8080` 提供服务，gateway 通过该地址反向代理。
- **本题相关知识：**监听地址、loopback、socket、upstream、HTTP 502。
- **学习目标：**从“本机成功、远端失败”识别监听范围错误，并恢复指定私网接口的服务。
- **初始状态：**`app` 上的 `orders-api` 健康，但其运行配置将 `8080` 绑定到 `127.0.0.1`；gateway 必须经 app 的私网接口访问该服务。
- **用户看到的症状：**在 app 本机请求 API 成功，gateway 的 upstream 连接被拒绝，client 经 `api.training.internal` 收到 502。
- **任务边界：**把服务暴露到规定的私网接口；不能改为任意外网监听、修改 DNS、在 gateway 启动 API 副本或跳过反向代理。
- **完成条件：**gateway 能连通 app 的规定私网监听，client 经既有 HTTP 链路获得正确响应。
- **检查点：**API listener 位于规定私网地址或接口；gateway 到 app:8080 连接成功；client 请求返回 `orders-api` 的成功响应。
- **提示层级：**方向：比较 app 本机与 gateway 请求的差异；证据：检查 socket 的本地地址和 gateway upstream 错误；概念：绑定 `127.0.0.1` 只接受同一网络命名空间的连接。
- **解答与复盘：**由本机成功、远端拒绝的组合排除应用逻辑，将根因收敛到监听范围，修复 app 的权威配置后用三段请求验证。生产中应将外部可达性探测放在服务所在主机之外。
- **作者 verifier：**未扩展到不允许的外部接口；未修改 DNS 或绕过 gateway；服务重启后监听范围保持正确。
- **能力状态：**可在“内部 Web 服务链路”实现。

### `repair-nginx-upstream`：修复 Nginx upstream 端口

- **环境地图：**`client -> gateway -> app`；app 正确监听私网 `8080`，gateway 的 Nginx 为 `api.training.internal` 反向代理。
- **本题相关知识：**Nginx upstream、端口、HTTP 502、配置测试、反向代理。
- **学习目标：**按请求路径将 502 收敛到 upstream 定义，而不改变健康 app 的监听行为。
- **初始状态：**app 正确监听私网 `8080`，但 gateway 的 Nginx upstream 配置仍指向旧端口 `8081`。Nginx 自身正常运行并接受 client 请求。
- **用户看到的症状：**client 经 `api.training.internal` 请求得到 502；gateway error log 指向 upstream connection refused，而 app 的健康端点在正确端口可用。
- **任务边界：**修复 gateway 的权威 upstream 定义并安全生效；不能修改 app 到旧端口、把 gateway 配置成静态响应，或绕过 Nginx。
- **完成条件：**Nginx 将请求转发到 app 的实际监听端口，端到端响应恢复。
- **检查点：**Nginx 配置校验成功；gateway 到 app:8080 连通；client 通过服务名称获得成功响应。
- **提示层级：**方向：把 client->gateway 与 gateway->app 分开确认；证据：对照 Nginx error log、upstream 定义和 app socket；概念：502 是代理无法从上游获得有效响应，不等同于 client 到代理的连接失败。
- **解答与复盘：**确认 gateway 与 app 各自健康后，定位旧端口残留在 upstream，校验配置并平滑生效。生产发布应把 app listener 与 proxy upstream 一起进行端到端回归检查。
- **作者 verifier：**app 的规定监听端口未被改动；未使用静态响应绕过 upstream；reload 后配置仍有效。
- **能力状态：**可在“内部 Web 服务链路”实现。

## 扩展范围

可增加端口冲突、Host 路由、健康端点和 graceful reload。每题都应把 TCP 层与 HTTP 层的可观察证据区分清楚。
