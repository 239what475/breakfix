# JupyterHub 对照审查

## 审查对象

- 本地路径：`/home/what/myproject/breakfix-similar-projects/jupyterhub`
- 审查提交：`ad0f8cd`
- 定位：多用户 Jupyter server 的 Hub、Proxy、Authenticator、Spawner 框架，不是题目或
  自动评测平台。

## 已确认的设计

1. `README.md` 将系统拆成 Hub、可配置 Proxy 和多个 single-user server。
2. `jupyterhub/spawner.py` 将 `start`、`poll`、`stop`、`get_state`、`load_state` 和
   `clear_state` 作为 Spawner 生命周期契约；Hub 重启后按保存状态恢复 server 视图。
3. `jupyterhub/app.py` 提供可插拔 Authenticator、Spawner、Proxy 和 scoped token/RBAC，
   并将 culler 作为受管理服务而非 user server 内逻辑。

## 与 Breakfix 的对照

Breakfix 的 Controller 已是 Kubernetes 专用 Spawner：Server 写不可变 Environment spec，
Controller 调和 namespace、workspace Pod、vcluster 与 cleanup，CRD status 是可恢复状态。
它还区分 challenge revision 和用户 attempt；这比“一用户一个长期 notebook server”更准确。
终端 ticket 也已经短期、一次性且绑定 Environment/window。

## 可以吸收

### 现在吸收

- **恢复契约作为接口测试**：将 JupyterHub 的 `start/poll/stop/load_state` 思路映射到
  Environment 生命周期，维护一个固定测试矩阵：Server 重启、Controller 重启、终端断开、
  已完成等待清理、失败清理。现有 `server-recovery` 真实验收是基础，题库扩展时要使其覆盖
  container 与 vcluster 的所有终态，而非只验证浏览器重新连上。
- **明确的权限面**：继续坚持 terminal ticket 只绑定 user/environment/challenge/window，
  不将通用 Kubernetes bearer token 放进 workspace 或 WebSocket URL。JupyterHub 的 scoped
  token 设计证明这不是实现细节，而是多用户 runtime 的核心边界。

### 题库具备数据后再做

- 当同一用户确实需要并行做多题时，再定义“活动环境列表”和上限策略。它应以 Environment
  UID 和 challenge revision 为单位，不照搬 JupyterHub named server 语义。

## 不采用

- 不以 Hub/Proxy/Spawner 替换 Server、Controller 和 CRD。那会失去 VerifyTask、vcluster
  和 Controller-only status 写入的领域约束。
- 不把用户工作目录做成长久 home directory。Breakfix 的用户环境是可回收的题目 attempt；
  学习事实和作者 artifact 分别已有 PostgreSQL 与 Server PVC 权威来源。
- 不引入其通用插件体系。当前明确的 Go 接口和 CRD 契约更易保证不可信题目不能扩展平台。

## 结论

JupyterHub 提供的是运行时可靠性与最小权限参考。当前架构方向正确，应增加恢复矩阵的
覆盖，不应发生平台替换或持久 workspace 迁移。
