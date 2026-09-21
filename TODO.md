# TODO

已完成的阶段见 git 历史(最近:用户 Playground 03eed97..c9a7f39——空白练习环境从文档库改绑
到用户,全站悬浮球入口)。本文件保留当前阶段与未立项事项。

## 阶段:Playground 治理——闸门与可见性(2026-09-22 定案,本文件即执行计划)

### 0. 定案原则(与用户逐条确认)

1. **并发上限是本阶段核心**。悬浮球把"一个 vcluster"开放给每个登录用户,现有护栏只有
   每用户一个 + TTL 兜底,没有全站总量闸门。上限配置驱动、全站一刀切;不做按用户分层
   限额(有滥用证据再立项)。
2. **admin 观测页重建,吃现有端点为主**。`GET /api/admin/environments`(playground 已在列)、
   `POST /api/admin/environments/:name/release`、`GET /api/admin/runnable-actions` 都是幸存
   端点但没有 UI;只补一个缺口:`runnable_reaps` 无观测端点(两次 stuck 排障都靠直连
   postgres)。
3. **资源监控的抽象层级是环境级**:占用数、相位、年龄、到期——不是容器 CPU/内存。单环境
   资源上限已有(vk8s QuotaCPU/Memory/EphemeralStorage);容器级指标等接 Prometheus 另立项。
4. **My space 用户侧可见性一并做**:ActiveEnvironments 显示 playground 条目,配额卡片对
   用户变得可解释。
5. **使用统计做薄**:总览卡三个数字(今日创建/占用用户/占用上限),全部前端从环境列表
   聚合;平均使用时长等厚分析不做。

### 1. 契约定稿

- **并发上限**:
  - 配置:server 侧 `playground.max_active`(config/app 装配进 Handler,默认 10;Kind e2e
    由 prepare 显式设 1);
  - 语义:`POST /api/playground` 时统计全站活跃 playground 环境(content-kind=playground、
    非 Deleting、活跃相位——与 findPlaygroundEnvironment 同口径),达到上限返回
    `429`+ErrorResponse("playground is at capacity");GET/reset/close 不受限;存量环境不受
    影响,只挡新创建;上限是**软闸门**——并发创建的计数竞态允许少量超发,TTL 兜底,不引入
    分布式锁;
  - 前端:创建被拒走既有错误通知 + 按钮保持可重试,不新增环境状态机状态。
- **admin 环境观测页**(第三个页签"环境",路由 `/admin/environments`):
  - 总览卡:playground 占用/上限、按相位计数(creating/ready/draining/failed)、占用用户数、
    今日创建数——前端聚合自环境列表,零新后端;
  - 环境表格:name、用户、kind 徽标(playground/operations)、相位、创建时间、到期倒计时、
    失败原因展开;相位过滤;**卡住高亮**:Draining 或 Failed 持续超过 10 分钟标红(纯 UI
    启发式,不做告警);
  - 每行"强制释放"(现有端点,带确认;审计已有 recordEnvironmentRelease);
  - 页面下方"回收队列"小节:新端点 `GET /api/admin/runnable-reaps`(reap_key/state/attempt/
    last_error/next_attempt_at/updated_at,按 updated_at 倒序,封顶 100 条)。
- **My space**:`ActiveEnvironments` 条目增加 `kind: playground|operations` 字段(openapi
  破坏性演进,前端同步);playground 行显示状态徽标与到期时间,无 checkpoint 进度;配额
  occupied 早已按 user 标签计入,无需改。
- **e2e**:playground 套件尾段注册第二个用户,在 `max_active=1` 配置下断言创建被 429 拒、
  状态保持 none;admin 环境页冒烟(驱动方式执行时核实:admin e2e 工程已在删除提交中移除,
  prepare 是否保留 admin 凭据待查——若无则 admin 页以组件测为准,不新开 e2e 工程)。

### 2. 提交 1:后端闸门与端点

- [ ] 并发上限:config/app 增加 `playground.max_active`;bootstrap 装配;`StartPlayground`
      创建路径前检查(复用按 content-kind 的全站列表计数)并返回 429;单测:上限内放行、
      达限拒绝、Deleting/终态不计入、GET 不受限;
- [ ] `GET /api/admin/runnable-reaps`:postgres 查询 runnable_reaps,admin 鉴权,openapi
      定义与再生成(`make generate`);
- [ ] My space `ActiveEnvironments` 增加 kind 字段并追加 playground 条目(按 user 标签 +
      content-kind=playground + 活跃相位直读 k8s);单测改绑;
- 验证:`make test-unit`;`make verify-generated`;`make lint`。

### 3. 提交 2:admin 环境页与用户侧可见性

- [ ] `AdminEnvironmentsPage.vue`:总览卡 + 表格 + 相位过滤 + 卡住高亮 + 强制释放(确认
      对话框)+ 回收队列小节;AdminPage 第三页签与路由,AppShell adminSection 扩展;
- [ ] My space playground 行(kind 徽标、状态、到期,无进度);
- [ ] 球的容量拒绝提示文案;
- [ ] web 组件测:总览卡聚合、卡住高亮阈值、过滤、释放确认流、My space playground 行、
      admin 匿名/非管理员不可见;
- 验证:`make web-test-unit`;`npm run --prefix web build`。

### 4. 提交 3:e2e 与收口

- [ ] e2e prepare 为 server 显式设 `playground.max_active=1`;playground 套件尾段第二用户
      创建被拒断言;reader.smoke 增加 admin 环境页冒烟或按核实结果降级为组件测;
- [ ] 快车道(`test-unit`/`test-race`/`web-test-unit`/`verify-generated`/`lint`)与
      `make test-e2e-documentation` 全绿;
- [ ] TODO 收口章:交付记录、验收证据(含关闭后无残留的复核)。

### 5. 风险与对策

- **上限计数竞态**:软闸门语义,允许少量超发,TTL 兜底回收;不追求精确一致。
- **kind 字段破坏性演进**:openapi 再生成横跨 Go/web 两侧,My space 前端同提交同步,任何
  提交点全绿(沿用上一阶段的提交切分经验)。
- **卡住阈值误导**:10 分钟是 UI 启发式;判定为"卡住"不自动动作,只高亮,处置仍走人工
  强制释放(带审计)。
- **admin e2e 空窗**:admin 页面工程在删除提交中已移除;本阶段不重建独立工程,冒烟并入
  文档目标或降级组件测,执行时核实后二选一。

### 6. 已知后续(不属本阶段)

- 容器级资源指标(Prometheus 接入后另立项)、平均使用时长等厚统计、按用户分层限额、
  轮询退避;playground 的 node/Incus 类型、多实例(集合 API)、终态断言(学习闭环)若做
  另行立项;
- 工作区崩溃孤儿沙箱/PVC 清扫(authoring 侧遗留);js-yaml ×3 等 Dependabot;ollama critical
  无上游修复;#15 typescript 7 等 vue-tsc 跟进;
- 首次真实发布后部署侧验证无凭证直拉。

## 挂起待决策(不排期)

- **内容治理/紧急下架**:场景侧非 authoring 内容无法下架、无管理员覆盖;索引 append-only 是
  刻意设计,与"紧急摘除"冲突。若做,方向是索引摘除/tombstone 而非删除数据——独立设计后另行
  立项,当前不做。
