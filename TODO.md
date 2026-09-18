# TODO

上一阶段"管理控制台重设计"已于 2026-09-18 完成并验收：三段提交（`9469f45`、
`a6494dc`、`087cb77`）每步 vue-tsc/build 通过、admin E2E 全绿，Incus 恢复后补跑
canonical `make test-e2e-admin` 亦全绿。实施规格与验收记录见 `b210feb` 起的 git
历史；更早阶段（管理控制面 v1、离线文档库生成器 docs-project-v10）同样见 git
历史。

## 阶段一：文档切库（已排期，确认后实施）

目标：离线文档库（`docs-site/documents`，docs-project-v10 产物）成为 Server 唯一
文档输入；运行时 HTML 解析与源码读取路径删除；证据绑定三元组；部署不再钉死单页。

核心原则（2026-09-18 与用户确认）：解析离线完成、版本化（generator_version）、
带 digest 固化为产物；运行时只消费产物、不再解析——流水线输入与证据同源，agent
看到的字节即证据 digest 覆盖的字节，解析器获得独立于 Server 构建的版本身份。

现状（2026-09-18 探查）：

- Server 输入是两个 ConfigMap 钉死的单页快照（`deploy/manifests/server.yaml`：
  `breakfix-documentation-snapshot`/`-source`），
  `internal/adapter/documentation/source.go` 在运行时解析渲染 HTML 提取章节
  文本，并可读上游源码片段（`ReadSource`/`ReadInclude`）；
- 库产物已是结构化 Markdown：全局 `manifest.json`（tree/upstream/stats）+ 每页
  `index.md`/`index.json`（页级 digest、anchors[] 逐锚点 digest、
  generator_version），共 854 页/12476 锚点，运行时零消费；
- 证据缺口：`document-context` artifact 与 PublicationManifest 只带 upstream
  commit 与章节文本 digest，无 parser 版本、无页级 digest。

### 提交 1 feat(docs): read the pinned page from the offline document library

- [ ] 新增库适配器（复用 `internal/docsproject` 的严格解码与
      `markdownAnchorSection`）：按 pinned `DocumentContext` 校验全局 manifest
      （upstream source/commit/version/locale、build_info.repository），
      `ReadPage(path, anchor)` 先验页级 digest（sha256 of index.md）再按
      `anchors[].digest` 验 anchor 切片，`ReadMetadata` 改读 PageManifest
- [ ] config 增加 `library_root`，与旧 key 并存、配置即优先；`make docs-smoke`
      增加真实库冒烟（库路径断言；源码读取断言留待提交 4 删除）
- [ ] 单测：页/锚点 digest 不匹配、manifest 与 pinned context 不符 → 拒绝

### 提交 2 feat(docs): bind pipeline evidence to library digests

- [ ] `DocumentContext` 增加 `ParserVersion`（= 库 generator_version）与
      `PageDigest`，写入 document-context artifact payload 与
      PublicationManifest.Context；`ContentID` 计算式不变，存量 workflow ID
      稳定——ContentID 只锚定上游身份（source+commit+page+anchor），解析器
      升级不自动分叉新工作流，是否重生成由阶段二批次决定；同 ID 下不同
      parser 版本的运行以 ledger 三元组区分，可审计
- [ ] postgres `validatePublicationLedger` 校验三元组一致；agent 输入
      （DocumentData、EvidenceReference.Digest）改由库切片提供；若输入语义
      变化，同步提升 prompt/tool/policy version 常量
- [ ] admin system info 端点暴露库标识（generator_version、upstream commit）

### 提交 3 test(e2e): drive documentation E2E from a real generated library

- [ ] `e2e-documentation-prepare.sh` 改用真实生成器（`cmd/docs-project
      -pages` 子集，含 pod-lifecycle 页；可基于 `make docs-fixture` 的真实
      渲染页子集）产出迷你库，替代 `test/fixtures/documentation-e2e` 手工快照；
      ≤1MiB 走 ConfigMap，超限走卷挂载/镜像层（按实测尺寸定）
- [ ] 文档 E2E 断言 publication manifest 含三元组；admin E2E 回归；两套全绿

### 提交 4 refactor(docs): retire the runtime HTML parsing path

- [ ] 删除 `snapshot_root`/`source_root` 配置、`ReadSource`/`ReadInclude` 与
      `renderedSectionText`/`renderedMainContent` 等运行时 HTML 路径，reader
      仅剩库实现；docs-smoke 删除源码读取断言
- [ ] `deploy/manifests/server.yaml` 移除两个 documentation ConfigMap 与卷，
      库以镜像层/只读卷进入部署（Makefile 接线 docs-project 为前置）
- [ ] go test/build 绿；E2E 不受影响（fixture 已在库上）

### 边界

- SPA 阅读器 iframe、docs-site nginx 镜像、`VITE_DOCS_ORIGIN` 机制不动；
  阅读器切解析产物（阶段三）不在本阶段
- ops 场景侧、admin 控制面零变化；API 契约除新增字段外不变
- 多语言维持 2026-09-16"暂不实施"决定

### 验收

- `make docs-smoke`（真实库）、`go test ./...`、`make test-e2e-documentation`、
  `make test-e2e-admin` 全绿
- 部署物不再存在单页 ConfigMap；Server 运行时路径无 HTML 解析（解析仅存在于
  离线生成器一侧）

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

## 阶段三：阅读器切换解析产物（设计共识，阶段一落地后排期）

"解析结果作为输入"原则的普遍推论：阅读器同样消费库产物，而非 iframe 上游
渲染 HTML。本阶段是 NEXT.md"阅读器内的实践体验"线的第一步，与阶段二无相互
依赖，先后或并行排期时定。

- Server 新增文档读取 API：按页返回解析产物（markdown + anchors + digest），
  下发前校验页级 digest；SPA 内置 Markdown 渲染（代码高亮）替代 iframe
- 读者看到的与流水线据以生成的是同一份字节；锚点即库 anchors[]，实践入口
  按锚点挂载成为一等公民（该线后续步骤）
- docs-site nginx 镜像退到生成器输入侧，`VITE_DOCS_ORIGIN` 与 iframe 同步
  （postMessage 校验、URL 镜像）退场，改为 SPA 内部路由
- 保真度依据（2026-09-18 实测）：854 页保留 5818 个代码块；表格降级 132 页
  共 462 处（15%）；dropped_elements 均值约 2/页，逐页记账于 index.json，
  可审计、可监控

## 挂起待决策（不排期）

- **内容治理/紧急下架**：场景侧非 authoring 内容无法下架、无管理员覆盖;实践内容
  PracticeRevision 一侧 append-only(索引只进不改)是刻意设计,与"紧急摘除"冲突。若做,
  方向是索引摘除/tombstone 而非删除数据——独立设计决策后另行立项,当前不做。
