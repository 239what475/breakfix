# TODO

已完成的阶段见 git 历史(最近:环境重置的真实完成 80f66fd..本提交——reset 单路径语义、
代际号、回收退避死信、终端与球的真值绑定、e2e 强断言)。本文件保留当前阶段与未立项事项。

## 阶段:环境重置的真实完成——擦除、代际与回收退避(2026-09-22 交付)

### 交付记录

- **提交 1(86212c5)后端 reset 语义与代际**:Decide 在 `operation=Resetting` 期间持续选择
  DecisionReset,采纳式 Provision 不再参与 reset 生命周期(此前 DecisionReset 分支写完
  observed-reset-nonce 注解后即落入 Provision,对删除中的命名空间"存在即采纳",以陈旧
  终端的首次 ready 观察清除 Resetting——reset 请求约 4 秒即假完成,物理擦除 43 秒后才
  落地)。reset 全程一条路径:采纳先写 status(Operation=Resetting + observedResetNonce,
  崩溃窗口仍 decide Reset)再幂等写注解(nonce + reset-adopted-at);唯重建终端的 Ready
  观察清除 operation;超过 ResetTimeoutSeconds(blank vk8s 保持 900s,覆盖命名空间删除
  + 重建)以 reason=reset-timeout 转 Failed(可重建),不再假 Ready。CRD status 回显
  `observedResetNonce`(创建为 0,每次采纳递增),`GET /api/playground` 增加可选
  `generation`;operations 场景 reset 单测同提交改绑(删除未净持续 Resetting、删净重建
  方清 operation、超时转 Failed、采纳式 Provision 不可达)。
- **提交 2(01d5ee5)回收退避死信与前端消费**:runnable_reaps 失败重试由平铺 5s 改为
  指数退避(基数 5s、封顶 5min、尝试上限 20 次,约 75 分钟进入死信),超限经
  `Deadletter`(与 Complete 同租约栅栏)置 `dead` 终态(schema baseline 53→54,CHECK 与
  openapi 枚举同步);死信只做可见:claim 扫描永不认领,环境名按用户确定性派生,重建换
  UID 即新队列条目,只泄漏资源不阻塞用户。admin 回收队列 dead 徽标 + 死信行不再显示虚构
  的"下次尝试"。球的 resetWipePending 由时间性猜测改为代际判定(点击时钉住
  environment_id + generation,ready 且 generation 未变不算数,环境更换自动失效);终端
  会话 Connected 对真:本地拆除不再残留陈旧 connected、断开立即转重连提示、每次重连必
  铸新 ticket(组件测覆盖);My space playground 行在 Resetting 期间显示 Resetting 而非
  陈旧 Ready(openapi 增加可选 operation)。
- **提交 3 e2e 强断言、擦除栅栏与收口**:reset 段强断言——点击后先等球离开 ready(修掉
  断言在 POST 返回前对陈旧 ready 通过的竞态)、marker 文件 `tee /tmp/playground-proof`
  落盘、reset 后 `cat` 必得 "No such file or directory"(物理擦除证明,替代 shell
  scrollback 弱验证)、新 marker 回显、generation 严格大于 reset 前;时限按真实擦除放宽
  (套件 30→40 分钟)。**e2e 首跑暴露单路径 reset 的无限擦除缺陷**:Provider.Reset 的
  Delete-until-gone 无法区分"删除中的旧命名空间"与"本轮重建的新命名空间",控制器每
  2s 的重试把重建成果反复删除。修复为 provider 层代际栅栏:reset 请求携带
  binding.ResetNonce,k8s 重建命名空间时打 `breakfix.dev/reset-generation` 注解、Incus
  重建 project 时打 `user.breakfix.reset_generation` 配置,Delete 见到本代标记即认定擦除
  已完成、采纳重建(仅 reset 路径携带栅栏,provision/release 恒为 0;project 策略校验
  忽略栅栏键)。修复后复跑通过。

### 验收证据

- 快车道:`make test-unit`(postgres 全接,含退避序列与上限置 dead 的 postgres 单测)、
  `make test-race`、`make web-test-unit`(34 通过,含 useTerminalSession 状态真值与
  球的代际语义组件测)、`make verify-generated`、`make lint` 全绿。
- `make test-e2e-documentation`:2 通过(reader smoke + playground 全链路:create→ready→
  终端 marker 落盘→reset→generation 变化→marker 文件消失→新 marker 回显→第二用户
  429→close→none→admin 环境页 "/ 1"),3.0 分钟。
- 关闭后无残留复核:runtimeenvironments 清空、无 breakfix-vk8s 命名空间、对应
  `runnable_reaps` 行 `succeeded attempt=5`(5s→10s→20s→40s→80s 退避链路在真实目标
  上被观测);另一次 stuck 复盘中,卡住的 Resetting 环境由租约到期走 Drain→Reap 完整
  回收,无孤儿资源。
- 注:本机 docker 构建网络对 GitHub releases 大文件传输有截断(WSL2 桥接),vcluster
  CLI 镜像层改用 `docker build --network=host` 预热后缓存复用,不影响仓库内容。

### 已知后续(不属本阶段)

- 死信 reap 的人工重试动作(本阶段只做可见);容器级资源指标(Prometheus 接入后另立项)、
  平均使用时长等厚统计、按用户分层限额、前端轮询退避;playground 的 node/Incus 类型、
  多实例(集合 API)、终态断言(学习闭环)若做另行立项;
- 工作区崩溃孤儿沙箱/PVC 清扫(authoring 侧遗留);js-yaml ×3 等 Dependabot;ollama critical
  无上游修复;#15 typescript 7 等 vue-tsc 跟进;
- 首次真实发布后部署侧验证无凭证直拉。

## 挂起待决策(不排期)

- **内容治理/紧急下架**:场景侧非 authoring 内容无法下架、无管理员覆盖;索引 append-only 是
  刻意设计,与"紧急摘除"冲突。若做,方向是索引摘除/tombstone 而非删除数据——独立设计后另行
  立项,当前不做。
