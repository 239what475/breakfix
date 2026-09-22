# TODO

已完成的阶段见 git 历史(最近:队列三阶段——死信 reap 退场 b96515e、动作队列诚实失败
040cf72 与本提交的 admin 动作队列观测面,均于 2026-09-22 单提交交付;交付记录与验收
证据见各自提交的 TODO 收口章)。本文件保留未立项事项与挂起决策。

## 阶段:admin 动作队列观测面——补齐两条队列的观测对称(2026-09-22 交付)

### 交付记录

- **client**:`web/src/api/client.ts` 增手写方法 `listAdminRunnableActions()`(GET
  /admin/runnable-actions,镜像 `listAdminRunnableReaps` 的 request 封装);生成物类型
  AdminRunnableActionPage/Item/Summary 已存在,零 generate,后端零改动。
- **admin.ts**:`useAdminEnvironments` 的 `Promise.all` 增第四路拉取,新增 `actions` 与
  `actionSummary` ref,随既有 refresh(进入页面、refreshRequest、释放后)一并刷新。
- **AdminEnvironmentsPage.vue**:回收队列区块之后增"动作队列"区块——按状态汇总 chip 行
  (queued/running/completed/failed 生命周期序,未知态按字典序殿后,零计数不渲染)+
  表格(动作 key shortId、phase、状态徽标、尝试、失败 code 列 title 挂 class+summary、
  下次运行:活行为倒计时标签"Ns/m/h 后"+clock 悬浮,终态行示 —,不借用只会回望的
  relative());行高亮直接消费服务端 `flag === 'attempt-high'`,不做前端派生(与 reap
  侧 attempt≥4 前端派生不同,数据来源更权威);区块注解写明产品语义:failed 为显式
  终态,infra 失败 8 次预算耗尽记 attempts-exhausted,重装对 infra 失败自动开新周期、
  artifact 失败缓存 fail-fast。
- **admin.css**:补 `[data-state="running"]`(蓝,在飞)与 `[data-state="completed"]`
  (绿,完成)徽标配色,遵循五色徽标纪律;failed 复用既有红、queued 走中性默认;增
  `admin-action-summary` chip 行与 `admin-action-table` 等宽字体列。
- **测试**:AdminEnvironmentsPage.spec 增三组断言——汇总 chip 按生命周期序渲染(by_state
  键序打乱验证排序不受 JSON 序影响);行级失败诊断(queued 行保留上次 infra 失败的
  code+title、failed 徽标 data-state、终态行下次运行示 —、活行示倒计时)、attempt-high
  高亮唯一性(running 行 attempt 6 无旗标不高亮,证明消费服务端旗标而非前端派生阈值);
  空态(空 items 无表格无 chip,回收队列区块不受影响)。

### 验收证据

- 快车道全套:`make test-unit`、`make test-race`、`make verify-generated`、`make lint`
  (0 issues)、`kubectl kustomize .`、`make web-test-unit`(8 files / 54 tests)全绿;
  另跑 `vue-tsc -b` 类型检查通过(非门槛,build 路径背书)。
- Go 侧零改动(git diff 仅 web/src 五文件),不排 PG 门控(编译与既有套件已覆盖);
  行为断言由组件测试承担(徽标/高亮/汇总/title/空态)。
- 条目级 state/phase 过滤查询参数本阶段未接 UI(与计划边界一致:先做全量列表+汇总,
  过滤等有真实排障需求再说)。

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
