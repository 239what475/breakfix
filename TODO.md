# TODO

已完成的阶段见 git 历史(最近:队列两阶段——死信 reap 退场 b96515e 与动作队列诚实失败
040cf72,均于 2026-09-22 单提交交付;交付记录与验收证据见各自提交的 TODO 收口章)。当前
阶段:admin 动作队列观测面。本文件保留当前阶段计划、未立项事项与挂起决策。

## 阶段:admin 动作队列观测面——补齐两条队列的观测对称

### 定案

- **服务端契约现成**:`GET /admin/runnable-actions`(openapi `listAdminRunnableActions`)
  返回整队列汇总(byState/byAttempt)与条目列表——含 failure 三列与 `attempt-high` 旗标
  (runnable_admin.go);条目级 state/phase 过滤可选,汇总始终覆盖全队列。
- **前端零消费**:web 无 fetch、无页面区块(client.ts 只有 `listAdminRunnableReaps`)——
  reap 队列有观测表,动作队列没有,观测不对称。
- **时机**:动作阶段(040cf72)刚让队列状态变得有语义——`attempts-exhausted` 的 failed
  行、退避中的 infra 重试行、缓存 fail-fast 的 artifact 失败行——但管理员只能隔着
  工作流/安装失败间接感知,排障时看不到队列本体。
- **后端零改动**:契约、handler、仓储观测查询全部现成;`attempt-high` 旗标阈值
  (attempt≥4,runnable_admin.go:102)在新上限(8)语义下恰为"预算过半",不过时。卡住
  高亮直接消费服务端旗标,不做前端派生——比 reap 侧的前端派生更权威的数据来源。
- 边界:条目的 state/phase 过滤查询参数本阶段不接 UI(区块先做全量列表+汇总;过滤等有
  真实排障需求再说)。

### 任务

- **client**:`web/src/api/client.ts` 增手写方法 `listAdminRunnableActions()`(镜像
  `listAdminRunnableReaps` 的 request 封装);生成物类型
  (AdminRunnableActionPage/Item/Summary)已存在,零 generate。
- **admin.ts**:拉取并入既有 `Promise.all`,新增动作列表与汇总 ref,随 refresh 一起刷新。
- **AdminEnvironmentsPage.vue**:回收队列区块之后增"动作队列"区块——汇总行(按状态
  计数)+ 表格(动作 key(shortId)、phase、状态徽标、尝试、失败 code(title 挂
  class+summary)、下次运行(终态行示 —))+ 行高亮消费 `flag === 'attempt-high'`;
  区块注解写明产品语义(failed 为显式失败;重装对 infra 失败自动开新周期,artifact
  失败缓存 fail-fast)。
- **admin.css**:视需要补动作状态徽标配色(running/completed;failed 复用既有样式)。
- **测试**:AdminEnvironmentsPage.spec 增动作区块断言——汇总渲染、failed 徽标、
  attempt-high 行高亮、失败列 title、空态。

### 提交切分

单提交交付(`feat(admin): surface the runnable action queue in the console`):纯前端 +
测试 + TODO 收口章。后端、openapi、schema、生成物零改动。

### 验收门槛

- 快车道全套:`make test-unit`、`make test-race`、`make verify-generated`、`make lint`、
  `kubectl kustomize .`、`make web-test-unit` 全绿。
- Go 侧零改动,不排 PG 门控(编译与既有套件已覆盖);行为断言由组件测试承担
  (徽标/高亮/汇总/title/空态)。

## 未立项事项

- 容器级资源指标(Prometheus 接入后另立项)、平均使用时长等厚统计、按用户分层限额、
  前端轮询退避;
- playground 的 node/Incus 类型、多实例(集合 API)若做另行立项;
- #15 typescript 7 等 vue-tsc 跟进(上游阻塞);ollama 警报若在模块图升至 v0.34.2 后仍有
  残留,按"传递依赖、未链接进产物"显式忽略——go mod why 证实无任何 ollama 包被引用
  (js-yaml ×3 与 shiki 遗留已随 2026-09-22 卫生批次解决);
- 首次真实发布后部署侧验证无凭证直拉。

## 挂起待决策(不排期)

- **内容治理/紧急下架**:场景侧非 authoring 内容无法下架、无管理员覆盖;索引 append-only 是
  刻意设计,与"紧急摘除"冲突。若做,方向是索引摘除/tombstone 而非删除数据——独立设计后另行
  立项,当前不做。
