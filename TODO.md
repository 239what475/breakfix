# TODO

已完成的阶段见 git 历史(最近:环境重置的真实完成 80f66fd..efddb32——reset 单路径语义与
代际号、回收退避死信、终端与球的真值绑定、e2e 强断言与擦除栅栏;交付记录与验收证据见
efddb32 的 TODO 收口章)。本文件保留当前阶段与未立项事项。

## 阶段:文档入口换轨——退役解析管道,换 admin 维护的 key+url iframe 聚合页(2026-09-22 定案,本文件即执行计划)

### 0. 背景与定案(与用户逐条确认)

1. **现状是失去消费者的重型管道**:`cmd/docs-project`+`internal/docsproject/`(约 3500 行)
   把 Hugo 渲染的 k8s 网站镜像解析成 digest 语料,`internal/adapter/documentation/`(约 1050
   行)做 pinned identity 校验,语料打进 FROM scratch OCI 镜像挂给 server 暴露 3 个只读端点,
   前端是完整 markdown 阅读器(约 1000 行,源硬编码单一 k8s snapshot)。digest/evidence 机制
   当初服务文档练习生成,该产品已删(9809890);今日 catalog 明确排除此库、playground 已解绑、
   assistant/authoring/generation 均不依赖,数据库零表。加新文档源=再写一个站点专属
   extractor,不可持续。
2. **iframe 是回头路但理由成立**:docs-site/README.md 记载 nginx 镜像+iframe 注入曾刻意换成
   解析页,当时动机是给 Agent 注入受 pin 的文档上下文——该需求已随产品删除,读者只剩"浏览"。
3. **产品定案**:全局共享列表,admin 写(增/改/删)、所有人可读;页面仍是 /documentation,
   主体=左侧单列列表+右侧内容区;列表末尾"添加"按钮(仅 admin 可见)弹对话框收名称+URL;
   点击项右侧 iframe 展示;嵌入失败不可自动探测(跨域 load 事件无法区分成败)→ 每条记录带
   "允许嵌入"开关(默认开,admin 添加时自行验证)+ 右侧常驻"在新窗口打开"入口兜底;项上
   设置按钮 hover 显示、小屏常驻,可改名称/URL/嵌入与删除;小屏(<900px)列表走抽屉(沿用
   reader 的 toc drawer:Contents 按钮+遮罩+选中即收起),桌面保留折叠按钮(全宽 iframe);
   iframe keep-alive(v-show 保活,LRU 上限约 5)避免切换重载;刷新经 ?doc=<key> 恢复选中;
   URL 仅允许 http/https;地址栏不随 iframe 内部导航变化、"新窗口打开"打开的是入口 URL——
   iframe 固有属性,接受。
4. **明确不动**:`documentation-example` scenario 类型枚举与
   MySpaceScenarioContentSource="documentation" 是已删文档练习产品的遗留词汇,牵 DB
   CHECK/catalog/generation/my-space,是否清理独立决策,本阶段不碰。

### 1. 契约定稿

- **表 `documentation_links`**(schema baseline 54→55):`key` PK(服务端生成短随机 id,改名
  不失效)、`title`(非空,非唯一,重名由前端提示)、`url`(非空,仅 http/https)、`embed`
  boolean 默认 true、`created_at/updated_at`;列表按 created_at 升序。
- **API(openapi 先行,make generate 前后端生成物同步)**:公开 `GET
  /api/documentation/links`(optionalJWTMW,返回 key/title/url/embed);admin 写三件套(照
  environment_admin.go 挂 jwtMW+RequireAdmin,记审计):POST/PATCH/DELETE
  `/api/admin/documentation/links[/{key}]`,服务端同规则校验(title 非空、URL scheme)。旧三
  端点(page/tree/asset)保留至提交 3 再删,保证每个提交全绿。
- **前端**:骨架沿用 reader 的两列 grid(264px+body)与 900px 媒体查询;右侧窄工具栏
  (文档名+常驻"在新窗口打开",rel=noopener noreferrer;小屏加 Contents);embed=false 的项
  右侧渲染 URL 卡+跳转按钮(同样 noopener);admin 控件随身份显隐(复用 AppShell/AdminPage
  的 admin 判定);AppShell 路由与顶栏入口不变,仅换页面实现。

### 2. 提交 1:新增侧后端——表、CRUD 与单测

- [ ] openapi 新增公开列表+admin 写三件套,`make generate` 前后端生成物同步(纯增量);
- [ ] schema_*.go:documentation_links 表,baseline 54→55,注册 schemaStatements;
- [ ] postgres repository(照 HumanActionRepository 风格)+ handler(公开列表 optionalJWT、
      写三件 RequireAdmin+审计),服务端校验 title/URL、key 服务端生成;
- [ ] 单测:handler 权限拒止与校验、repository CRUD/排序/唯一 key;
- 验证:`make test-unit`;`make verify-generated`;`make lint`。

### 3. 提交 2:前端聚合页替换 reader

- [ ] 新 `web/src/features/documentation/` 页面:列表(active 态+末尾添加[admin])、对话框
      (名称/URL/嵌入开关,编辑含改名)、项上设置(hover 显示、小屏常驻)、删除当前选中项后
      右侧回空态;
- [ ] 右侧工具栏+iframe 区:v-show keep-alive(LRU 上限 5,以节点复用保证切换不重载)、
      常驻"在新窗口打开"(noopener noreferrer)、embed=false 渲染 URL 卡+跳转;
- [ ] 响应式:900px 抽屉(Contents+遮罩+选中即收起)、桌面折叠按钮;`?doc=<key>` 恢复选中;
      空列表/加载失败空态;提交前 http/https 与名称非空校验;
- [ ] 旧 reader 组件、组件测与 fixtures 退役,新组件测覆盖上述行为;reader e2e 冒烟同步删除;
      AppShell.vue 从旧模块导入 documentationSource 拼入口 URL(source/version/path 参数),
      换轨后入口改直链 /documentation,AppShell.spec.ts 同步改绑;
- 验证:`make web-test-unit`;`npm run --prefix web build`;`make verify-generated`。

### 4. 提交 3:删除旧链路、e2e 与收口

- [ ] openapi 删旧三端点+AdminDocumentationDeployment,`make generate` 同步;client.ts 的
      getDocumentationPage/Tree 是手写层(make generate 只覆盖 generated/),同提交删除;
- [ ] 后端删除:internal/docsproject/、internal/adapter/documentation/、cmd/docs-project/、
      documentation_read.go(+test)、server.go 三条路由与 handlers.go DI、bootstrap 接线
      (documentation.go 与 server.go 两处)、config documentation 块与校验及其单测
      (config_test 的 DocumentationConfig 用例)、报告链收尾(system.go、system_types.go 的
      SystemDocumentationReport 类型与字段、system_admin.go 投影段、environment_admin_test
      的 documentation 断言改绑);
- [ ] 顺带更新仅注释级引用(environment_service.go、playground_test.go、controller/
      blank.go 中提及 documentation library 的注释);
- [ ] 构建部署清理:Makefile docs-*/documentation-library-image 目标、
      build/images/documentation-library/、deploy/manifests/server.yaml 挂载与 init 镜像、
      config/app 两份样例的 documentation 块;
- [ ] docs-site/ 整目录退役:git rm 三个跟踪文件(README.md/manifest.yaml/scripts/
      docs-site.sh,即 Makefile 的 DOCS_SITE_SCRIPT 与 documentation-library-image 构建上下
      文);根 .gitignore 的 docs-site/public/、docs-site/documents/ 两条目同步删除;未跟踪
      的 public/documents(约 1.5GB)git 不管,rm -rf 本地清理;
- [ ] 测试语料:test/fixtures/docs-project/、prepare 脚本只保留 playground 所需(max_active
      等);playground.e2e.spec.ts 移出 test/documentation/(它属 playground,居此仅历史);e2e
      目标更名 test-e2e-playground,执行时核实 Makefile/CI/脚本引用;
- [ ] 新聚合页 e2e 冒烟:首账号即 bootstrap admin,经 API 造一条链接→列表渲染→iframe src
      断言;
- [ ] 文档同步:system-architecture.md(/api/documentation 命名空间与已不存在的
      internal/application/documentpractice 引用)、api-contracts.md、NEXT.md"多文档源扩展"
      改写;
- [ ] 快车道(test-unit/test-race/web-test-unit/verify-generated/lint)+e2e 全绿;
      `kubectl kustomize .`;TODO 收口章(交付记录、验收证据)。

### 5. 风险与对策

- **iframe 被拒(X-Frame-Options/CSP frame-ancestors)**:不做自动探测,靠嵌入开关+常驻
  新窗口入口兜底;docker.com 之类基本必拒,admin 添加时自行试。
- **openapi 破坏性演进横跨两侧**:新端点先行、旧端点后删,每提交生成物同步(仓库既有惯例)。
- **离线/内网部署**:iframe 依赖用户浏览器可达目标站;key+url 模型天然支持指向内网镜像。
- **误伤遗留词汇**:documentation-example 枚举与 MySpaceScenarioContentSource 不动(见 0.4)。
- **URL 注入面**:服务端 scheme 校验+前端 noopener/noreferrer 双保险。

## 未立项事项

- 死信 reap 的人工重试动作(观测页已可见,处置留人工);
- 容器级资源指标(Prometheus 接入后另立项)、平均使用时长等厚统计、按用户分层限额、
  前端轮询退避;
- playground 的 node/Incus 类型、多实例(集合 API)、终态断言(学习闭环)若做另行立项;
- 工作区崩溃孤儿沙箱/PVC 清扫(authoring 侧遗留);
- js-yaml ×3 等 Dependabot;ollama critical 无上游修复;#15 typescript 7 等 vue-tsc 跟进;
- 首次真实发布后部署侧验证无凭证直拉;
- documentation-example 枚举与 MySpaceScenarioContentSource 的遗留词汇清理(见本阶段 0.4)。

## 挂起待决策(不排期)

- **内容治理/紧急下架**:场景侧非 authoring 内容无法下架、无管理员覆盖;索引 append-only 是
  刻意设计,与"紧急摘除"冲突。若做,方向是索引摘除/tombstone 而非删除数据——独立设计后另行
  立项,当前不做。
