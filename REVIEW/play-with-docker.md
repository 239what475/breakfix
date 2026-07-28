# Play with Docker 对照审查

## 审查对象

- 本地路径：`/home/what/myproject/breakfix-similar-projects/play-with-docker`
- 审查提交：`62cb072`
- 定位：MIT 许可的浏览器临时 Docker/Kubernetes playground。其 README 已声明服务自
  2026-03-01 起停止可用，因此仅作为历史实现参考。

## 已确认的设计

1. `api.go` 启动本地 event broker、文件存储、Docker/Kubernetes factory、scheduler 和
   Playground；默认 session duration 固定为两小时。
2. 同文件注册端口、Swarm、Kubernetes cluster 和 stats 的周期任务，说明 playground
   需要额外维护“用户服务是否可被浏览器访问”的状态。
3. `provisioner/dind.go`、`pwd/` 和 handlers 显示它以 privileged DinD/overlay session
   提供多实例终端；session 元数据是本地文件，event broker 也是进程内实现。

## 与 Breakfix 的对照

Breakfix 已经修正了该类系统的关键不足：PostgreSQL 保存领域状态，Environment CRD 可被
Controller 恢复，终端由一次性 ticket/Origin 保护，运行时有明确 idle/drain/destroy，且
题目构建和验证不依赖用户的 privileged Docker。Play with Docker 的二小时固定过期也不
能表达“已完成后回收、活跃时续租、断开后 grace”的学习环境语义。

## 可以吸收

### 题库具备数据后再做

- **受控服务预览**：当真实题目需要学习者访问自己启动的 HTTP 服务时，设计按 Environment
  和端口绑定的受控预览 URL。它应只代理白名单 TCP/HTTP 端口，经过用户身份和环境租约
  校验，并在 Environment 回收时删除；不能让用户任意创建 Ingress/LoadBalancer。
- **容量指标**：借鉴 scheduler 的端口/实例/stats 轮询思路，记录活跃环境数、启动耗时、
  终端连接数、检查点延迟和资源使用，作为未来容量规划而非完成判定依据。

## 不采用

- 不采用 privileged DinD、overlay session、宿主 Docker socket、文件存储或进程内 event
  broker。这些都破坏 Builder/Publisher/Verifier 与用户环境的现有安全和恢复边界。
- 不采用固定两小时 session TTL 或以端口扫描作为环境健康/完成状态。环境阶段与检查点
  仍须由 Controller 按 CRD 和题目语义决定。
- 不将 playground 当作题目运行时。它没有 challenge revision、真实验证、taxonomy 或
  学习记录语义。

## 结论

该项目主要是反例：临时终端本身不足以构成可恢复、安全的学习平台。唯一可后续借鉴的是
受控服务预览和容量观测，但必须建立在现有 CRD/Server 权威边界之上。
