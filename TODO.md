# TODO

已完成的阶段见 git 历史(最近:文档空白实践场景 b9424a2..0384541,含首跑加固批 809cf5a——
该阶段产物即本阶段的迁移对象,收口记录见 0384541)。本文件保留当前阶段与未立项事项。

## 阶段:用户 Playground(2026-09-22 定案,本文件即执行计划)

### 0. 定案原则(与用户逐条确认)

1. **Playground 与文档无关**。空白练习环境的所有权从"文档库"迁移到"用户":环境本来就不读
   任何文档内容,库绑定(`(user, 库钉住身份)`)是上一代产品框架的残留,且稀释了"每人一个"
   的成本栅栏(读两个库可持有两个 vcluster)。环境机器(RuntimeEnvironment `blankRuntime` 臂、
   reconciler、BlankRuntimePlan、provider、reaper、终端 attach、TTL)全部原样复用,**只改绑定层**。
2. **每用户一个**(定案):确定性名即配额,跨页面、跨库、全站携带;要干净开始用"重置"。
3. **入口是全站常驻悬浮球**(定案):登录用户在任意页面右下角可见,点击展开面板(状态徽标 +
   创建/重置/关闭 + 终端);匿名用户不渲染球(静默)。文档页右侧工具栏与空白终端面板退役。
4. **命名 playground**:UI 文案与 API 统一用 playground,与场景绑定的"环境"(operations
   learning)严格分词。现有空白场景端点不产生审计动作,无动词表负担。
5. **旧文档场景 API/前端同笔删除**:不做兼容、不并行,任何时刻只有一套实现。存量
   `documentation-blank` 环境与新命名不冲突,TTL(IdleTTL 900s/MaxLifetime 1800s)自然回收,
   无需迁移。

### 1. 契约定稿

- **绑定键**:`user_id`;确定性环境名 `playground-u-<uid>`(名字即配额);labels:`content-kind=
  playground`、`breakfix.dev/user`、`breakfix.dev/purpose=learning`;不再携带库身份
  (content-id/content-revision 从 playground 环境上消失)。按名直读 + 身份校验(user/purpose/
  blank/活跃相位)沿用现 `findBlankScenarioEnvironment` 语义,目标解析去库化。
- **状态机不变**:`none → creating → ready →(reset → creating | close → none)`;failed 可重建;
  TTL 回收视为 none。轮询 GET 在 Pending/Provisioning/Resetting 顺带续租(慢供给不被空闲 TTL
  饿死),Ready 后仍只认真实使用续租——两条均为现语义,平移。
- **API**:
  - `GET  /api/playground` → `{state: none|creating|ready|failed, environment_id?}`;
  - `POST /api/playground` → 创建(幂等:非终态返回现状;failed/none 可重建);
  - `POST /api/playground/reset` → Ready 态专用(回 creating);
  - `DELETE /api/playground` → 关闭释放(幂等);
  - `POST /api/playground/terminal` → 终端票据(Ready 后);
  - openapi 同步再生成;`/api/documentation/scenario*` 四端点 + `/reset` + 票据端点同笔删除。
- **前端**:
  - 全局悬浮球(AppShell 层):状态徽标(none 灰/creating 进行中/ready 就绪/failed 失败),
    固定右下角,登录可见、匿名不渲染、不发起任何请求;
  - 点击展开右下抽屉面板 = 三动词按钮(可用性矩阵沿用 none/creating/ready/failed)+ 终端
    (复用现 BlankScenarioPanel 的终端部分);Creating 轮询到 Ready;
  - `usePlayground` 组合式(由 `useBlankScenario` 改绑:非阻塞创建、轮询到就绪);
  - 键盘可达:球可 Tab 聚焦、面板 focus trap、Esc 收起;移动端悬浮球天然适配,面板为底部抽屉;
  - 移除:`BlankScenarioToolbar`、文档页面板挂载、`useBlankScenario`、文档场景 API client 方法。
- **My space**:配额计数按 `breakfix.dev/user` 标签统计,playground 天然计入;Overview 的
  ActiveEnvironments 展示留作后续(不属本阶段)。
- **e2e 归属**:悬浮球是全站功能,任意页面可驱动;套件沿用 `test-e2e-documentation` Kind 目标
  与登录辅助(沿用现有 prepare,不再为 playground 新开 e2e 工程),spec 更名
  `test/documentation/playground.e2e.spec.ts`。

### 2. 提交 1:playground 后端改绑

- [ ] `internal/transport/httpapi/documentation_scenario.go` → `playground.go`:目标解析去库化
      (不依赖库 PinnedContext 与文档库安装状态),环境名 `playground-u-<uid>`,labels
      content-kind=playground;`environment_runtime.go` 的 `learningEnvironmentName` 增加无目标
      派生或拆出 playground 变体;
- [ ] 路由五端点挂 `server.go`;删除 `/api/documentation/scenario*` 全部路由与 handler;
- [ ] openapi 再生成(`make generate`),web API client 同步再生成;
- [ ] 单测:状态机(none→creating→ready;reset;close;failed 重建;幂等创建)、轮询续租、
      供给期可见性(按名直读)、目标解析无库依赖(改绑现 documentation_scenario_test);
- 验证:`make test-unit`;`make verify-generated`;`make lint`。

### 3. 提交 2:前端悬浮球与文档页退役

- [ ] `web/src/features/playground/`:`PlaygroundFab`(悬浮球)+ `PlaygroundPanel`(抽屉面板,
      含终端)+ `usePlayground`;AppShell 挂载(登录可见、匿名静默);
- [ ] 移除 `BlankScenarioToolbar`/`BlankScenarioPanel` 挂载与组件、`useBlankScenario`、
      文档场景 API client 方法;
- [ ] web 组件测:球状态渲染、按钮可用性矩阵、面板 attach、匿名静默(不渲染零请求)、
      键盘可达(聚焦/Esc/focus trap);
- 验证:`make web-test-unit`。

### 4. 提交 3:e2e 改写与收口

- [ ] `scenario.e2e.spec.ts` → `playground.e2e.spec.ts`:注册登录→悬浮球创建→轮询 Ready→
      终端连接与命令回显(真实 vcluster)→重置回 Creating→再就绪→关闭回 None;关闭后 reap
      真正 succeeded(无残留 CR/namespace,沿用本阶段验证手法);
- [ ] `reader.smoke.spec.ts`:匿名断言改"悬浮球不渲染";文档页工具栏断言删除;
- [ ] 快车道(`make test-unit`/`test-race`/`web-test-unit`/`verify-generated`/`lint`)与
      `make test-e2e-documentation` 全绿;
- [ ] TODO 收口章:迁移记录(库绑定→用户绑定)、验收证据。

### 5. 风险与对策

- **悬浮球遮挡与可达性**:固定右下角、面板可收起;focus trap + Esc;移动端底部抽屉;组件测
  覆盖键盘路径。
- **全站轮询流量**:沿用现轮询间隔,登录用户才轮询、匿名零请求;退避留后续项。
- **存量环境回收**:命名不同不冲突,TTL ≤30 分钟自然回收;部署后经 admin 队列观测确认 reaper
  无积压。
- **两套端点短暂并存**:不允许——旧端点在提交 1 同笔删除,e2e 同目标内改写,仓库任何提交点
  全绿。
- **概念混淆**:playground 与场景环境在 UI/API 严格分词;文档页不再出现任何场景入口。

### 6. 已知后续(不属本阶段)

- playground 的 node/Incus 类型、多实例(集合 API 与真配额数字)、My space ActiveEnvironments
  展示、轮询退避、全站并发上限配置;终态断言(学习闭环)若做另行立项;
- 工作区崩溃孤儿沙箱/PVC 清扫(authoring 侧遗留);js-yaml ×3 等 Dependabot;ollama critical
  无上游修复;#15 typescript 7 等 vue-tsc 跟进;
- 首次真实发布后部署侧验证无凭证直拉。

## 挂起待决策(不排期)

- **内容治理/紧急下架**:场景侧非 authoring 内容无法下架、无管理员覆盖;索引 append-only 是
  刻意设计,与"紧急摘除"冲突。若做,方向是索引摘除/tombstone 而非删除数据——独立设计后另行
  立项,当前不做。
