# TODO

上一阶段"管理控制台重设计"已于 2026-09-18 完成并验收：三段提交（`9469f45`、
`a6494dc`、`087cb77`）每步 vue-tsc/build 通过、admin E2E 全绿，Incus 恢复后补跑
canonical `make test-e2e-admin` 亦全绿。实施规格与验收记录见 `b210feb` 起的 git
历史；更早阶段（管理控制面 v1、离线文档库生成器 docs-project-v10）同样见 git
历史。

## 阶段一：切库与阅读器统一切换（已排期，确认后实施）

目标：离线文档库（`docs-site/documents`，docs-project-v10 产物）成为产品唯一
文档输入——流水线与阅读器同源；运行时 HTML 解析与源码读取路径删除；证据绑定
三元组；部署不再钉死单页；阅读器退役 iframe、直接渲染解析产物。

核心原则（2026-09-18 与用户确认）：解析离线完成、版本化（generator_version）、
带 digest 固化为产物；运行时只消费产物、不再解析——流水线输入与证据同源，agent
看到的字节即证据 digest 覆盖的字节，解析器获得独立于 Server 构建的版本身份。
阅读器并入本阶段（同日与用户确认）：原独立排期实际等于不排，产品会长期停在
两套文档表示（流水线吃库、读者看 iframe）上；合并为 A/B 两波推进，A 波完成
即是全绿检查点。

现状（2026-09-18 探查）：

- Server 输入是两个 ConfigMap 钉死的单页快照（`deploy/manifests/server.yaml`：
  `breakfix-documentation-snapshot`/`-source`），
  `internal/adapter/documentation/source.go` 在运行时解析渲染 HTML 提取章节
  文本，并可读上游源码片段（`ReadSource`/`ReadInclude`）；
- 库产物已是结构化 Markdown：全局 `manifest.json`（tree/upstream/stats）+ 每页
  `index.md`/`index.json`（页级 digest、anchors[] 逐锚点 digest、
  generator_version），共 854 页/12476 锚点，运行时零消费；
- 证据缺口：`document-context` artifact 与 PublicationManifest 只带 upstream
  commit 与章节文本 digest，无 parser 版本、无页级 digest；
- 阅读器（SPA）经 iframe 加载 docs-site nginx 渲染的上游 HTML
  （`VITE_DOCS_ORIGIN` + postMessage 同步），文档 E2E 以 1314/1315 fixture
  服务器驱动——产品整体存在两套文档表示。

**A 波（流水线侧，提交 1–4；完成即是全绿检查点）**

### 提交 1 feat(docs): read the pinned page from the offline document library

- [x] 新增库适配器（复用 `internal/docsproject` 的严格解码与
      `markdownAnchorSection`）：按 pinned `DocumentContext` 校验全局 manifest
      （upstream source/commit/version/locale、build_info.repository），
      `ReadPage(path, anchor)` 先验页级 digest（sha256 of index.md）再按
      `anchors[].digest` 验 anchor 切片，`ReadMetadata` 改读 PageManifest
- [x] config 增加 `library_root`，与旧 key 并存、配置即优先；`make docs-smoke`
      增加真实库冒烟（库路径断言；源码读取断言留待提交 4 删除）
- [x] 单测：页/锚点 digest 不匹配、manifest 与 pinned context 不符 → 拒绝

### 提交 2 feat(docs): bind pipeline evidence to library digests

- [x] `DocumentContext` 增加 `ParserVersion`（= 库 generator_version）与
      `PageDigest`，写入 document-context artifact payload 与
      PublicationManifest.Context；`ContentID` 计算式不变，存量 workflow ID
      稳定——ContentID 只锚定上游身份（source+commit+page+anchor），解析器
      升级不自动分叉新工作流，是否重生成由阶段二批次决定；同 ID 下不同
      parser 版本的运行以 ledger 三元组区分，可审计
- [x] postgres `validatePublicationLedger` 校验三元组一致；agent 输入
      （DocumentData、EvidenceReference.Digest）改由库切片提供；若输入语义
      变化，同步提升 prompt/tool/policy version 常量
- [x] admin system info 端点暴露库标识（generator_version、upstream commit）

### 提交 3 test(e2e): drive documentation E2E from a real generated library

- [x] `e2e-documentation-prepare.sh` 改用真实生成器（`cmd/docs-project
      -pages` 子集，含 pod-lifecycle 页；可基于 `make docs-fixture` 的真实
      渲染页子集）产出迷你库，替代 `test/fixtures/documentation-e2e` 手工快照；
      ≤1MiB 走 ConfigMap，超限走卷挂载/镜像层（按实测尺寸定）
- [x] 文档 E2E 断言 publication manifest 含三元组；admin E2E 回归；两套全绿

### 提交 4 refactor(docs): retire the runtime HTML parsing path

- [ ] 删除 `snapshot_root`/`source_root` 配置、`ReadSource`/`ReadInclude` 与
      `renderedSectionText`/`renderedMainContent` 等运行时 HTML 路径，reader
      仅剩库实现；docs-smoke 删除源码读取断言
- [ ] `deploy/manifests/server.yaml` 移除两个 documentation ConfigMap 与卷，
      库以镜像层/只读卷进入部署（Makefile 接线 docs-project 为前置）
- [ ] go test/build 绿；E2E 不受影响（fixture 已在库上）

**B 波（阅读器侧，提交 5–7；保真度依据 2026-09-18 实测——5818 个代码块保留、
表格降级 132/854 页、dropped 均值约 2/页且逐页记账于 index.json）**

### 提交 5 feat(api): serve parsed document pages with digest checks

- [ ] Server 新增文档读取 API：按页返回解析产物（markdown、title、page_kind、
      anchors[]、页级 digest），下发前校验 digest；OpenAPI 契约与 web client
      同步生成；JWT 可读，页级 digest 作 ETag 协商缓存
- [ ] 目录树端点：库全局 manifest tree（title/path/children），分区懒加载；
      静态资产端点：按 assets[]（sha256）供图，页面 markdown 内相对路径随
      响应改写
- [ ] 单测：digest 不匹配拒绝、未知页 404、anchors 与 PageManifest 一致

### 提交 6 feat(web): render the reader from parsed pages

- [ ] SPA 内置 Markdown 渲染替代 iframe（选型 2026-09-18 调研后与用户确认）：
      markdown-it（html:false，GFM 表格内置；自定义规则渲染 `> [!NOTE]` 系
      提示块、标题 ID 映射库 anchors[]）+ Shiki 细粒度高亮（实测 12 语言
      shell/yaml/json/go/console/powershell/toml/http 等全覆盖）；tabs/details
      已被生成器压平为 `**Panel:**`/加粗摘要，无需交互组件；documentation 页
      懒加载分块，主 bundle 不显著膨胀
- [ ] 布局（2026-09-18 与用户确认）：左侧可收起目录（库 tree 懒加载、当前页
      高亮、移动端进抽屉）+ 中间文档主体；网格预留右侧实践栏位，本期不渲染
      ——实践入口属 NEXT.md 阅读器线（右栏默认收起、移动端退化为章节内联；
      当前 853/854 页无已发布实践）；滚动位置 ↔ 锚点同步（IntersectionObserver，
      为实践线识别当前章节打底）
- [ ] URL 语义原样承接（source/version/path/hash → SPA 路由与锚点滚动），
      移除 postMessage 校验；加载失败重试、移动端菜单平移
- [ ] 阅读器 E2E 重写为 API 驱动（URL 保持/前进后退/重试平移；防伪造
      postMessage 测试随机制消亡移除），同提交全绿

### 提交 7 refactor(docs): retire the docs-site runtime surface

- [ ] 退役 nginx 运行时镜像（Hugo 构建保留为生成器输入）、context 注入脚本、
      1314/1315 fixture 服务器与 `VITE_DOCS_ORIGIN`；Makefile 目标清理
- [ ] 全套回归：docs-smoke、go test、两套 E2E 绿

### 边界

- docs-site 的 Hugo 构建保留（生成器输入侧）；其运行时件（nginx 镜像、
  `VITE_DOCS_ORIGIN`、iframe/postMessage 同步、context 注入脚本、1314/1315
  fixture 服务器）随 B 波退役
- ops 场景侧、admin 控制面零变化；API 契约除新增文档读取 API 与新增字段外
  不变
- 多语言维持 2026-09-16"暂不实施"决定

### 验收

- `make docs-smoke`（真实库）、`go test ./...`、`make test-e2e-documentation`、
  `make test-e2e-admin` 全绿
- 部署物不再存在单页 ConfigMap；Server 运行时路径无 HTML 解析（解析仅存在于
  离线生成器一侧）
- 阅读器：保真度对上游抽查（含 tabs/表格/提示块页）；URL 行为
  （source/version/path/hash、前进后退）回归通过；阅读器 E2E 以 API 驱动
  重写后全绿

## 阶段二：多页铺开与批次控制（设计稿，阶段一落地后细化为提交拆解）

前提：阶段一切库落地——部署携带全量库，树/页/锚点元数据在 manifest 里现成。

### 管理台"文档"分区：语料目录树

- 数据源 = 库全局 manifest 的 `tree`（上游侧栏结构：Concepts 185 页、Tasks
  211、Reference 344、Tutorials 43…共 854 页/12476 锚点）；管理台镜像上游
  结构，不建第二套层级、不做编辑策展（守住 NEXT.md"不建知识图谱"红线）
- 章节行 = 服务端聚合行（"已发布 x/N · 失败 y · 进行中 z"），子节点展开
  懒加载；单分区数百页，禁止一次性全量拉取
- 页面行复用既有行模式：标题 + 五色状态徽章 + 当前阶段/停留时长 + 行尾 ⋯
  菜单（重试/重启/强制失败/跳转工作流详情）；锚点级信息仅行展开（平均约
  15 锚点/页）
- "只看失败/卡住"过滤 + 标题搜索为日常主视图
- 与"工作流"标签的关系：文档分区 = documentation 工作流按语料组织的投影；
  工作流标签保留队列/救援视角，854 规模下自身需要分页与过滤

### 批次：范围控制的唯一载体

- 铺开不逐页手工触发：范围（页面/章节/全集）在树上勾选后以批次发起；批次 =
  声明式对象（范围 + 并发/限速/重试策略 + 状态）进流水线
- 单页操作 = 现有救援语义（重试/重启/强制失败）+ 发起单页批次
- 批次发起/暂停/取消/重试失败页均为人操作，全部进 admin 审计；不维护持久
  页面黑白名单——范围只是批次声明的一部分，用完留痕于审计
- 调度：并发/限速对齐 Incus 物化与验证环境容量；section 优先级仅作调度顺序，
  不构成用户可见排序，不滑向内容策展

### 后端语义与死代码

- 看门狗：runnable action 失败（attempts_exhausted/action_failed）自动映射
  工作流 Failed，替代人工 force-fail 兜底
- 删除 `ReviseAt`/`Revise` 与 `AcquireWorkflowLease`（含 LeaseOwner/
  LeaseExpiresAt 字段）：修订循环无生产路径，并发已由 StateVersion 乐观锁覆盖
- 工作流按页可查：创建时落 page_path/anchor 冗余列，或按 ContentID 建索引
  （细化时定）

### 前置 API 缺口（细化时补）

- 语料树 + 分区聚合端点；admin 工作流列表项补 page/anchor 身份字段（现仅在
  publication manifest 里）
- 批次对象端点（发起/暂停/取消/重试失败页）与审计 action 枚举扩展

## 挂起待决策（不排期）

- **内容治理/紧急下架**：场景侧非 authoring 内容无法下架、无管理员覆盖;实践内容
  PracticeRevision 一侧 append-only(索引只进不改)是刻意设计,与"紧急摘除"冲突。若做,
  方向是索引摘除/tombstone 而非删除数据——独立设计决策后另行立项,当前不做。
