# TODO

已完成的阶段见 git 历史(最近:文档空白实践场景 b9424a2..0384541——其产物即本阶段的迁移对象)。
本文件只保留最近一个阶段的收口记录与未立项事项。

## 阶段收口:用户 Playground(2026-09-22 定案,2026-09-22 完成)

### 方向变更决策

空白练习环境的所有权从"文档库"迁移到"用户":环境从不读取任何文档内容,库绑定
(`(user, 库钉住身份)`)是上一代产品框架的残留,且稀释成本栅栏(读两个库可持有两个
vcluster)。定案:绑定键 user_id、每用户一个、全站常驻悬浮球入口、命名 playground、
旧文档场景 API/前端同笔删除不做兼容。环境机器(blankRuntime 臂、reconciler、
BlankRuntimePlan、provider、reaper、终端、TTL)全部原样复用,只改绑定层。

### 交付(四笔提交)

1. `6f3e02a` 后端改绑+旧世界退役:确定性名 `playground-u-<uid>`(名字即配额),labels
   content-kind=playground 且不携带任何内容身份;目标解析去库化(库未安装也可用);
   `/api/playground` 五端点替换 `/api/documentation/scenario*`(票据经固定 playground
   标识、以环境 UID+空白臂为栅栏);`environmentMatchesTarget` 学会无内容身份的围栏。
   web 旧世界(工具栏/面板/useBlankScenario/rail 栅格/客户端方法/终端通道)同笔退役——
   openapi 再生成横跨两侧,保证提交点全绿。
2. `e7f6591` 前端悬浮球:PlaygroundDock(球+抽屉面板,状态即外观:creating 转圈/ready
   圆点/failed 重试图标;Close 属于一切非 none 会话,用于放弃卡住的创建);键盘路径
   (球可聚焦、面板 focus trap、Esc 归焦);移动端底部抽屉;usePlayground 组合式。
   匿名访客由 AppShell 会话门控:不渲染球、零请求。
3. `77d2391` e2e 改写+reset 竞态修复:`playground.e2e.spec.ts` 从首页驱动悬浮球(摆位即
   证明"绑用户而非绑页面"),scenario→playground 工程与脚本同步;reader.smoke 匿名
   断言改为"球不渲染"。**竞态**:reset 的 POST 乐观返回 creating 而控制器尚未动作,
   首个 GET 可能读到旧 Ready 而终止轮询,随后真重置开始、球卡死在 creating——该缺陷
   自 useBlankScenario 继承、被新 e2e 真实命中;修复为 reset 后的 GET 必须先观察到
   creating,其后的 ready 才可信(单测覆盖说谎的 ready)。
4. 收口提交(本笔):本记录。

### 验收证据

- 快车道:`make test-unit`、`make test-race`、`make verify-generated`、`make lint`、
  `make web-test-unit`(20 passed)、`npm run --prefix web build` 全绿。
- Kind 验收:`make test-e2e-documentation` PASS(playground-e2e + reader-smoke
  2 passed,2026-09-22)。
- 关闭闭环:套件关闭后 playground 环境 reap 两跳内 succeeded(attempt 2),集群无残留
  RuntimeEnvironment/vk8s namespace。
- 迁移:存量 documentation-blank 环境与新命名不冲突,TTL 自然回收,破坏式重置随 e2e
  prepare 清场,无需迁移路径。

### 设计落点备注

- Close 属于一切非 none 会话(含 creating):放弃卡住的创建是它的正当用途;
- 悬浮球在移动端保留(底部抽屉),与旧文档工具栏的"移动端隐藏"不同——练习场现在是
  用户级功能,不再随阅读器布局让位。

## 已知后续(不属本阶段)

- playground 的 node/Incus 类型、多实例(集合 API 与真配额数字)、My space
  ActiveEnvironments 展示、轮询退避、全站并发上限配置;终态断言(学习闭环)若做另行立项;
- 工作区崩溃孤儿沙箱/PVC 清扫(authoring 侧遗留);js-yaml ×3 等 Dependabot;ollama critical
  无上游修复;#15 typescript 7 等 vue-tsc 跟进;
- 首次真实发布后部署侧验证无凭证直拉。

## 挂起待决策(不排期)

- **内容治理/紧急下架**:场景侧非 authoring 内容无法下架、无管理员覆盖;索引 append-only 是
  刻意设计,与"紧急摘除"冲突。若做,方向是索引摘除/tombstone 而非删除数据——独立设计后另行
  立项,当前不做。
