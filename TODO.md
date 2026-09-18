# 下一阶段:管理控制台重设计

上一阶段"管理员控制面 v1"已于 2026-09-18 完成实现与验收:角色与账号管理(首用户
admin、TOTP 重置)、append-only 人操作审计、工作流观测与解卡/重启(force-fail/restart)、
队列观测与指标、管理界面、环境与系统信息 API 全部落地,`make test-e2e-admin` 在 Kind
集群通过全链路验收;实施期按"先改规格再改实现"纪律修订了三处设计假设(重启重跑的工件
命名空间、ledger 同 digest 约束、runnable revision digest 幂等)。提交序列(`60727b1`…
`bc48cb3` + 复核修复 `33f624a`)、实施规格与验收记录见 git 历史。

更早的"离线文档库生成器"阶段同样完成并验收(版本至 **docs-project-v10**,全量基线
854 页 / 217 孤儿 / 433 重定向 / 12,476 锚点 / 63 个入库资产,dropped 链接 294→179);
文档工作流的后续推进(切库、多页批次铺开)见 NEXT.md。

本阶段只重做管理界面的信息设计与视觉:**功能面、API 契约、权限行为零变化**。

## 背景:为什么重做

v1 管理界面是"能用的运维表格",不是设计过的控制台,且与全站既有模式脱节:

- 顶部胶囊 tab 自成一套;全站既定模式是 my-space 的**左侧 250px 图标 tab + 居中
  1060px 内容区**双栏结构。
- "队列积压摘要"四格大数字卡占据页面黄金位置,但队列是次级信息。
- 每行平级挂"详情 / Force-fail / Restart"三个按钮——危险操作与普通操作同视觉权重;
  base.css 早已提供 `danger-button`,Force-fail 却用普通 `compact-button`。
- 12 个状态徽标 12 种颜色,没有视觉重点;时间线与审计是纯文字列表,没有用全站已有的
  `history-row` 图标行模式。

对照业界控制台实践(状态可视化、操作分级、色彩纪律、渐进披露)与全站设计语言
(tokens.css 变量 + my-space/catalog 的既有组件模式),按以下决策重做。

## 设计决策(2026-09-18 与维护者确认)

- **结构对齐 my-space 双栏**:左侧 250px sidebar(管理标识 + 队列数据格 + 图标 tab:
  工作流/用户/审计),右侧居中 1060px 内容区;760px 断点折叠为横条 + mobile-tabs,
  复用既有响应式模式。
- **队列摘要是次级信息**:从页顶大卡降为 sidebar 的 `space-summary` 式数据格
  (queued/running/completed/failed 四格 + flag 计数格),有 flag 才着色(琥珀/红),
  默认全中性。
- **管线即状态可视化**:工作流卡片内画 **9 阶段横向 stepper**(本阶段唯一新组件),节点
  图标、颜色、标签字号全部取自既有 token;把"状态靠读文字"变成"状态一眼可见"。
- **操作分级**:整卡可点击展开详情;Force-fail / Restart 收进行尾 `⋯` 菜单(菜单内红字),
  确认框"确认执行"用 `danger-button`;确认框的审计预览与 reason 必填设计保留。
- **色彩五档纪律**:中性灰(默认/未到)、蓝(进行中/主操作)、绿(成功/已发布)、琥珀
  (stuck / attempt-high)、红(失败/破坏性);废除 12 色徽标。
- **行模式统一**:时间线与审计行用 `history-row` 式图标列(22px lucide 语义图标 + 内容 +
  右侧 mono 时间);审计 detail JSON 默认收起、行点击展开。
- **stuck 异常优先置顶**:存在 stuck 工作流时内容区顶部出现异常横幅,直达处置入口;
  无异常不渲染——健康时页面安静。
- **纯前端重构**:API、状态管理、权限行为零变化;admin E2E 的 UI 选择器随重构同步更新
  并保持全绿。

提交纪律(全程有效):做完一个可审查单元立即提交,不攒批;每提交保持构建与当包测试通过;
TODO 勾选随对应提交更新,禁止收尾批量补勾;规格与实现变更在同一提交内同步;提交信息沿用
仓库格式(`类型(范围): 摘要` + Boundary/Validated 结构化正文)。

勾选状态按当前代码与测试证据维护;未勾选项表示仍有实现或验收工作未完成。

## 1. 实施规格

### 1.1 布局骨架

行为规格:

- `AdminPage.vue` 改为 my-space 式 grid(`250px minmax(0,1fr)`):sidebar 含三块——
  管理标识(`space-identity` 模式:盾形图标 + "管理控制面" + role 小字)、队列数据格
  (`space-summary` 2×2 模式:queued/running/completed/failed,另起一行 flag 计数格,
  琥珀/红着色)、图标 tab(`space-tabs` 模式:lucide `Workflow`/`Users`/`ScrollText` +
  工作流/用户/审计)。
- 内容区:`space-content-header` 模式——eyebrow(`Admin console`)+ h1(随 tab 变化)+
  右侧刷新 icon-button;内容列宽沿用 `max-width: 1060px` 居中。
- stuck 工作流存在时,内容区顶部**异常横幅**(左侧红/琥珀粗边框卡):"N 个工作流卡在
  <state> — <reason>",附"查看"按钮直达该卡展开;无 stuck 不渲染。
- `admin.css` 重写:优先复用 base.css 全站类(`compact-button`/`danger-button`/
  `text-button`/`icon-button`/`eyebrow`/dialog.css)与 tokens.css 变量;新增类按
  my-space 的命名与间距习惯。

验收标准:

- [x] 与 my-space 并排截图对比:结构、留白、字阶一致,像同一个产品。
- [x] 队列格:有 flag 的格子琥珀/红着色,无 flag 时全中性灰。
- [x] 760px 断点:sidebar 折叠为横条 + mobile-tabs,行为同 my-space。

### 1.2 工作流节(核心)

行为规格:

- 工作流卡片 = `space-row` 升级:标题行(h3 工作流 ID、DM Mono、截断)+ 状态徽标(五档
  色)+ **stepper** + meta 行(`space-row-meta` 模式:revision · state_version · dwell ·
  更新时间)+ 行尾 `⋯` 菜单;整卡 hover 边框加深(全站 hover 模式),点击展开详情。
- **stepper 规格**:9 节点——规划→计划评审→生成→工件评审→物化→验证→验证评审→发布→
  已发布,节点间连线。节点状态映射:已过=`✓` 绿(`--green`);当前=`●` 蓝(`--blue`);
  当前且 stuck=`◐` 琥珀(`--amber`)+ 轻微脉冲动画;未到=`○` 灰(`#9ca7a2`,同
  `history-state` 默认色)。标签 9px DM Mono;当前阶段停留时长标注在标签下。终态
  Failed/Rejected/NoPractice 不渲染 stepper,以徽标 + meta 表达。
- `⋯` 菜单:Force-fail(仅非终态显示)/ Restart(仅 Failed/Rejected 显示,与后端 409
  语义一致——前端隐藏不承担权限);菜单项红字;点开进既有确认框(审计预览 + reason
  必填 ≤500),"确认执行"改用 `danger-button`。
- **详情 = 行展开**(卡片下方缩进区块,同页展开,不做路由跳转):
  - 元数据 `dl`(`published-card dl` 模式,9px dt + 14px DM Mono dd):revision /
    state_version / dwell / 更新时间 / stuck 原因;
  - 三小节(17px h2 标题模式):时间线、AgentRun 审计、发布清单;
  - **时间线** = `history-row` 模式:22px 图标列 + 内容(kind 语义名 + mono 短 ID +
    owner_role/policy)+ 右侧相对时间。kind→lucide 图标映射:`document-context`=
    FileText、`learning-unit-plan`=Compass、`plan-gate`=Shield、`practice-candidate`=
    Package、`artifact-gate`=ShieldCheck、`admin.force_fail`=Zap(红)、`admin.restart`=
    RotateCcw(琥珀)、verification 相关=FlaskConical、publication=Rocket;未映射 kind
    回退 CircleDot。
  - AgentRun 审计同模式(图标 `Bot`,role 作文案)。

验收标准:

- [x] stepper 四种节点状态(已过/当前/当前且 stuck/未到)渲染正确;Published 卡与
      Failed 卡形态差异符合规格。
- [x] `⋯` 菜单按工作流状态显示可用动词;确认框 reason 必填与禁用逻辑不回退;执行键
      danger 样式。
- [x] 时间线图标映射齐全且含回退;长 ID/digest mono 截断不破行。
- [x] 键盘可达:焦点序合理,菜单与行展开带 `aria-expanded`。

### 1.3 用户节与审计节

行为规格:

- 用户:标题行 + `space-row` 列表——名称 h3 + 角色小方格(`section-count` 式:admin 用
  品牌绿系、user 用中性灰;**不得**用红色表达 admin)+ subject/创建时间 meta + 行尾
  `text-button`"重置 TOTP";密码确认弹窗与一次性 secret 展示保留(dialog.css)。
- 审计:`history-row` 模式——动作图标列 + 动作名(DM Mono)+ `actor → target` + 右侧
  相对时间;过滤沿用 `history-filters` 模式(DM Mono 小标签 + 输入框);detail JSON 默认
  收起,行点击展开;"加载更多"游标分页保留。

验收标准:

- [ ] 用户行/审计行与 my-space 对应行模式一致;admin 徽标为绿系而非红。
- [ ] 审计过滤、展开、分页行为不回退;detail 展开为格式化 JSON。

### 1.4 E2E 同步与回归

行为规格:

- `test/admin/admin.spec.ts` 的 UI 选择器随重构同步更新,全链路(空库注册→登录→点火→
  卡死→force-fail→restart→再点火→发布)保持绿;新增断言:stepper 当前阶段节点、`⋯`
  菜单入口、审计行展开。
- 回归边界:diff 限于 `web/src/features/admin/`、AppShell 管理入口样式、admin E2E;
  其他页面零改动;`vue-tsc --noEmit` 与 `npm run build` 通过。

提交计划(每步构建与 admin E2E 绿):

1. `feat(web): align the admin console shell with the app layout patterns`
   ——骨架/sidebar/队列数据格/异常横幅 + E2E 适配。
2. `feat(web): visualize documentation workflow stages in the admin console`
   ——stepper/工作流卡片/`⋯` 菜单/行展开详情。
3. `feat(web): rebuild admin users and audit views on shared row patterns`
   ——用户/审计/移动端断点 + E2E 收尾。

## 2. 挂起待决策(不排期)

- **内容治理/紧急下架**:场景侧非 authoring 内容无法下架、无管理员覆盖;实践内容
  PracticeRevision 一侧 append-only(索引只进不改)是刻意设计,与"紧急摘除"冲突。若做,
  方向是索引摘除/tombstone 而非删除数据——独立设计决策后另行立项,当前不做。
