# TODO

已完成的阶段见 git 历史(最近:文档入口换轨 c5e6bc0..本提交——退役解析管道,换 admin
维护的 key+url iframe 聚合页;交付记录与验收证据见本提交的 TODO 收口章)。本文件保留
未立项事项与挂起待决策。

## 阶段:文档入口换轨——退役解析管道,换 admin 维护的 key+url iframe 聚合页(2026-09-22 交付)

### 交付记录

- **提交 1(c5e6bc0)新增侧后端——表、CRUD 与单测**:`documentation_links` 表
  (schema baseline 54→55)落全局共享列表:key 为服务端生成的短随机 id(`doc-` + hex,
  改名不失效)、title 非空非唯一、url 仅绝对 http(s)、embed 布尔默认开。领域规则收在
  新包 `internal/domain/doclinks`(包名避开了 Go 工具链的保留包名
  `documentation`——包名为 `documentation` 的文件会被构建系统整体忽略,这是本次踩到的
  冷坑)。openapi 纯增量:公开 `GET /documentation/links` + admin 写三件套
  `POST/PATCH/DELETE /admin/documentation/links[/{key}]`,`make generate` 前后端生成物
  同步;handler 照 environment_admin 惯例挂 jwtMW+RequireAdmin,create/update/delete
  各记一条 `documentation.link.write` 审计(audit 封闭动词表扩员,detail 携带
  op/title/url),服务端与仓储两处同规则校验 title/URL。单测覆盖权限拒止(匿名 401、
  member 403)、校验 400、未知 key 404、CRUD/创建序排序/唯一 key。
- **提交 2(ceaf166)前端聚合页替换 reader**:`/documentation` 换新页面——左侧单列
  列表(active 态,admin 末尾"添加"行与项上 hover 设置按钮、小屏常驻)+右侧内容区;
  可嵌入条目进 iframe keep-alive 池(v-show 保活、LRU 上限 5、节点复用切换不重载);
  `?doc=<key>` 刷新与历史遍历恢复选中;embed=false 渲染 URL 卡;工具栏常驻"在新窗口
  打开"(noopener noreferrer,iframe 另加 referrerpolicy=no-referrer);900px 抽屉
  (Contents+遮罩+选中即收起)、桌面折叠按钮;admin 对话框收名称/URL/嵌入开关,提交前
  同服务端规则校验、重名仅提示不阻塞、失败回显在对话框内不丢已输内容,删除当前选中项
  后右侧回空态;URL hash `?doc=<key>` 随选中 pushState。旧 reader(markdown-it+Shiki
  渲染、toc、fixtures、组件测)与 reader e2e 冒烟同提交退役;AppShell 入口改直链
  `/documentation` 并向页面传 isAdmin。
- **提交 3(本提交)删除旧链路与收口**:openapi 删旧三端点(page/tree/asset)与
  `AdminDocumentationDeployment`(`AdminSystemStatus.documentation` 字段随之移除);
  后端删除 `internal/docsproject/`(约 3500 行)、`internal/adapter/documentation/`、
  `cmd/docs-project/`、`documentation_read.go`(+test)、server.go 三条路由、
  handlers.go 的 documentationLibrary DI、bootstrap 接线(documentation.go 与
  server.go 两处)、config 的 DocumentationConfig 块与校验及单测、报告链收尾
  (system.go、SystemDocumentationReport、system_admin.go 投影段、environment_admin_test
  断言改绑);仅注释级引用同步改写(environment_service.go、playground_test.go、
  controller/blank.go、e2e-prepare.sh、e2e-bootstrap-core.sh)。构建部署清理:Makefile
  的 docs-* 与 documentation-library-image 目标、DOCS_SITE_SCRIPT/DOCS_PROJECT 变量、
  build/images/documentation-library/、server.yaml 的库挂载与镜像卷、两份 config 样例
  的 documentation 块、.gitignore 的 docs-site 条目;docs-site/ 整目录退役(三个跟踪
  文件 git rm,未跟踪的 public/documents 约 1.5GB 本地 rm)。CI 侧 nightly/release 剔除
  文档管道步骤,docs-upstream-canary 工作流整体删除。测试链更名:e2e-documentation
  prepare 改造为 playground 专用(只保留 max_active 钉定、busybox 预热、Catalog 投影
  等待与 prepared 标记),`test/fixtures/docs-project/` 删除,playground.e2e.spec.ts 移
  至 `test/playground/`,playwright 配置与 Makefile/脚本/npm script 全部更名
  test-e2e-playground;新增聚合页 e2e 冒烟(见验收证据)。admin 控制台腿随首账号语义
  移入冒烟:playground 套件的注册用户不再假设自己是 admin。文档同步:
  system-architecture.md(聚合页与 documentation_links)、api-contracts.md(公开读 +
  admin 写三件套契约)、NEXT.md(产品叙述改为运维现场焦点,删除"多文档源扩展"——
  聚合页模型下新增文档源即 admin 增加一条链接)、operations/testing.md(套件更名与
  playground 冒烟说明)。遗留词汇 `documentation-example` 枚举与
  MySpaceScenarioContentSource 按 0.4 决策未动。

### 验收证据

- 快车道:`make test-unit`(postgres 全接,含 documentation_links 仓储 CRUD/排序/唯一
  key 与 handler 权限/校验/审计链路)、`make test-race`、`make web-test-unit`(49 通过,
  含 keep-alive LRU 驱逐、?doc 恢复、对话框校验/重名提示/失败回显、抽屉与折叠、
  AppShell isAdmin 转发)、`make verify-generated`、`make lint` 全绿。
- `make test-e2e-playground`:2 通过(聚合页冒烟 + playground 全链路)2.3 分钟。冒烟:
  首账号经 API 注册即 bootstrap admin,POST 两条链接(embed 开/关)→ 公开列表返回
  2 条→ 匿名浏览器渲染列表且 admin 控件隐藏→ iframe src=录入 URL、referrerpolicy
  就位→"在新窗口打开"noopener noreferrer→ embed=false 渲染 URL 卡→ `?doc=<key>`
  重载恢复选中→ 注入 admin token 重载后添加行与设置按钮出现→ 管理台环境观测渲染且
  容量卡显示 "/ 1"。playground:create→ready→终端 marker 落盘→reset→generation 严格
  递增→marker 文件消失→新 marker 回显→第二用户 create 429→close→none。
- `kubectl kustomize .` 渲染通过;`make build`(嵌入前端)通过。

## 未立项事项

- 死信 reap 的人工重试动作(观测页已可见,处置留人工);
- 容器级资源指标(Prometheus 接入后另立项)、平均使用时长等厚统计、按用户分层限额、
  前端轮询退避;
- playground 的 node/Incus 类型、多实例(集合 API)、终态断言(学习闭环)若做另行立项;
- 工作区崩溃孤儿沙箱/PVC 清扫(authoring 侧遗留);
- js-yaml ×3 等 Dependabot;ollama critical 无上游修复;#15 typescript 7 等 vue-tsc 跟进;
- 首次真实发布后部署侧验证无凭证直拉;
- documentation-example 枚举与 MySpaceScenarioContentSource 的遗留词汇清理(牵 DB
  CHECK/catalog/generation/my-space,独立决策后另行立项)。

## 挂起待决策(不排期)

- **内容治理/紧急下架**:场景侧非 authoring 内容无法下架、无管理员覆盖;索引 append-only 是
  刻意设计,与"紧急摘除"冲突。若做,方向是索引摘除/tombstone 而非删除数据——独立设计后另行
  立项,当前不做。
