# 下一阶段:结构化文档库与多页实践流水线

上一阶段"公共运行底座与文档实践自动流水线"已于 2026-09-16 完成全部任务并通过验收,收口提交为
`docs(plan): close legacy runtime removal`;完整任务列表、提交清单与勾选证据见该时点的 TODO.md 历史。

目标:把文档内容输入从"运行时解析渲染 HTML + 可读源码树"收敛为**单一离线编译产物**。固定上游
commit 经 Hugo 构建得到渲染树,再由离线解析程序把整棵渲染树投影为结构化文档库;Server、
规划/生成/审核 Agent 与人只读文档库,不再接触 HTML 解析和上游源码。

```text
固定上游 commit
    ↓  make docs-build(Hugo,确定性)
渲染树 docs-site/public(完整站点)
    ↓  make docs-project(解析程序,完全离线,确定性)
结构化文档库(每页一份格式化文档 + digest 清单 + 页面清单)
    ↓
├── 人直接读(documents/**/*.md:调试、审阅)
└── Agent 流水线读(规划 / 生成 / 审核的唯一内容输入)
```

已确认的设计决策:

- 渲染树是唯一内容输入,不读上游源码;证据粒度为页面 + 锚点 + 页面 digest + 解析器版本,
  不引入源码文件 / include 级别的证据链。
- 身份仍只认上游 commit;文档库是证据输入,不参与身份。
- 文档库生成完全离线、确定性:同一 commit + 同一解析器版本 → 同一文档库。
- 测试 fixture 直接取自真实编译产物的冻结拷贝(`docs-site/public` 不进 git;fixture 由
  make 目标从本地构建拷贝生成,可重复再生)。合成 HTML 仅保留用于错误路径单测。

勾选状态按当前代码与测试证据维护;未勾选项表示仍有实现或验收工作未完成。

## 1. 离线文档库生成器(实施规格)

本节是交给实施者的完整契约:1.1–1.11 为设计与不变量,1.12 区分"必须遵守"与"实现自由度"。

### 1.1 代码位置与命令

- 新增 `cmd/docs-project`(薄 main)与 `internal/docsproject`(全部逻辑);**不得依赖**
  Server、数据库、运行时配置或网络;HTML 解析用 `golang.org/x/net/html`(仓库已在用,
  可选 goquery/cascadia)。
- Makefile 新增两个目标,归入现有 `docs-*` 族:
  - `docs-project`:执行生成(见下方 CLI 默认值),目标内固定传当前 `-version` 值
    (当前为 `docs-project-v4`),版本递增时同步修改 Makefile;
  - `docs-fixture`:把 1.11 清单中的页面从本地构建冻结拷贝到 `test/fixtures/docs-project/`。

CLI 参数:

| 参数 | 默认 | 说明 |
|---|---|---|
| `-root` | `docs-site/public` | 渲染树根 |
| `-out` | `docs-site/documents` | 输出根(追加进 `.gitignore`) |
| `-workers` | `8` | 页面提取并行度 |
| `-version` | 必填 | 生成器版本串(当前为 `docs-project-v4`),写入全局 manifest;提取规则变更时必须递增 |
| `-resume` | 关 | 跳过已存在且 generator_version 匹配的页面输出;版本不匹配强制全量 |
| `-pages` | 空 | 逗号分隔站点路径,子集模式(调试/测试用) |

退出码:`0` 全部成功;`1` 存在页面级失败(细节写 `report.json`);`2` 输入错误(root 缺失、
目录树跨页校验失败、树页面文件映射缺失)。

### 1.2 输出目录布局

```text
docs-site/documents/                # 输出根(-out)
├── manifest.json                   # 全局 manifest
├── report.json                     # 仅存在失败时生成
└── docs/<站点路径>/index.md         # 页正文;同目录还有 index.json,一页两文件
```

页面文件路径 = 站点路径在输出根下原样映射(v1 收录范围均为 `/docs/` 前缀,故顶层恒为
`docs/`)。完整示例:站点路径 `docs/concepts/workloads/pods/pod-lifecycle/` 对应

```text
docs-site/documents/docs/concepts/workloads/pods/pod-lifecycle/index.md
docs-site/documents/docs/concepts/workloads/pods/pod-lifecycle/index.json
```

一页两文件,**原子写**(临时文件 + rename),保证并行安全与断点续跑安全。`*.md` 即人读
视图,不另设渲染产物。

### 1.3 页 manifest(<输出根>/<站点路径>/index.json)字段

```jsonc
{
  "format_version": 1,
  "generator_version": "docs-project-v4",
  "upstream": {"source": "kubernetes", "commit": "<sha>", "version": "snapshot-ce98a43", "locale": "en"},
  "path": "docs/concepts/workloads/pods/pod-lifecycle/",
  "page_kind": "content",            // content | index(在目录树中有子页面者为 index)
  "title": "Pod Lifecycle",
  "digest": "sha256:...",            // 对该页 index.md 全部字节的 sha256
  "anchors": [                       // 按文档顺序
    {"id": "pod-lifetime", "level": 2, "title": "Pod lifetime",
     "digest": "sha256:...",          // 对该章节在 index.md 中字节段的 sha256
     "parent": ""}                    // 上一级(更小 level)标题的 id,顶级为空
  ],
  "feature_states": [                // 章节内的 feature-state 标注
    {"anchor": "container-restart-rules", "gate": "ContainerRestartRules",
     "stage": "beta", "since": "v1.35", "note": "enabled by default"}
  ],
  "assets": [{"path": "docs/images/ingress.svg", "digest": "sha256:..."}],
  "links": {"internal": 120, "page_internal": 6, "external": 6, "to_orphans": 2,
            "out_of_tree": ["docs/unknown/path/"], "dropped": 0},
  "code_blocks": 7,
  "degraded_tables": 0,              // 含 rowspan/colspan 而降级为文本的表格数
  "dropped_elements": 0              // 未知元素丢弃计数
}
```

严格 JSON、拒绝未知字段(读取方按 schema 校验);页面 `<h1>` 即页标题,不进 `anchors`,
章节锚点从 `<h2>` 起;`feature_states.stage` 取 notice class 的小写形式
(alpha/beta/stable);`locale` 字段为多语言预留,v1 恒为 `en`。

### 1.4 全局 manifest(manifest.json)字段

```jsonc
{
  "format_version": 1,
  "generator_version": "docs-project-v4",
  "upstream": {/* 同页 manifest;commit 取自 docs-site/build-info.json,一并记录其内容 */},
  "tree": {"nodes": [                 // 目录树,来源见 1.7
    {"title": "Getting started", "path": "docs/setup/", "children": [ /* 递归 */ ]}
  ]},
  "pages": ["docs/concepts/...", /* 按字典序 */],
  "orphans": ["docs/reference/generated/kubernetes-api/v1.23/", /* 字典序;218 基线 */],
  "redirects": {"count": 433, "digest": "sha256:..."},   // _redirects 文件摘要
  "stats": {"pages": 854, "index_pages": 0, "anchors": 0, "assets": 0},
  "warnings": ["cross-page sidebar validation skipped for -pages subset"] // 仅 -pages 子集模式
}
```

### 1.5 Markdown 生成规则(documents/<路径>/index.md)

块级元素:

| 来源 | 产物 |
|---|---|
| `<h1>`–`<h6>`(取 `id` 属性为锚点,**不重新推导 slug**) | `#` × level + 规范空白后的标题文本 |
| 段落 / 列表 | 规范空白正文;列表保留嵌套与序号 |
| `<pre><code>` | 围栏代码块;语言取 `language-*` class;内容**逐字**(首尾各去一次空行);围栏长度必须大于内容中最长反引号串 |
| `<table>` | GFM 管道表;单元格内的竖线字符转义,块级内容并入单行文本;出现 `rowspan`/`colspan` → 降级为按行列顺序的文本块,计 `degraded_tables` |
| `div.alert`(info/warning/danger/success) | `> [!NOTE]` / `> [!WARNING]` / `> [!CAUTION]` / `> [!TIP]` 块,内容行加 `> ` 前缀 |
| tab 面板 | 每面板一行 `**Panel: <label>**` + 面板内容 |
| feature-state notice | 一行 `**[FEATURE STATE: Beta | gate: X | since: v1.35 | enabled by default]**`,缺省字段省略;同时结构化进 `feature_states` |
| `<blockquote>` / `<details>` | `> ` 前缀;details 的 summary 加粗一行 + 内容 |

行内元素:`code` → `` `x` ``;`strong` → `**`;`em` → `*`;`a` → `[text](target)`;
`img` → `![alt](path)`;`br` → 空格。

上游渲染树中若内容区 `<h1>` 为空(v4 已知 3 页),使用非通用 `og:title`；若该值同样为
`Kubernetes` 或缺失,则使用正文第一个非空章节标题,并在 Markdown 顶部合成 `# <title>`。这仅是
固定渲染输入的容错,不重新推导 slug 或锚点。

标题后的 `td-heading-self-link` 控件属于站点交互 chrome,不进入标题文本或 Markdown 链接。

**库内路径形式**:`.md` 中的站内链接与图片一律使用**以输出根为基准的站点路径**
(如 `docs/concepts/overview/`、`docs/images/ingress.svg`,页面路径保留尾部斜杠);
它不是文件系统相对路径,消费方一律按库根解析。

转义:正文中对反斜杠、连续反引号、行首 `#`/`-`/`>` 做最小转义,保证不破坏结构。
未知元素:丢弃节点、计 `dropped_elements`,不中断。

### 1.6 内容提取规则

- **main 定位**:复用现有 `findMain` 语义(`<main>` 元素),找不到即该页失败。
- **剥离清单**:面包屑、Feedback/评分组件、`icon-copycode` 图标及一切带 `onclick` 的图片、
  页内 TOC 导航块(class 含 `toc` 的 nav)、chroma 行号槽(`ln`/`lineno` 类 span)。
- **章节切分**:章节块 = 标题行 → 下一个 level ≤ 自身的标题行之前;anchors 依此计算 digest
  与 parent 链。
- **index 页**:在目录树中有子页面者分类为 `index`;卡片网格提取为"链接清单块"
  (每项 label + target),其余规则相同。

### 1.7 目录树提取

- **权威来源**:固定从 `<root>/docs/home/index.html` 的 `nav#td-section-nav` 提取
  (Docsy 服务端渲染,每页相同;该文件缺失即退出码 2)。
- **剥离语言切换块**:侧边栏顶部指向 `/xx/docs/`(非 `/docs/`)的 locale 链接。
- **跨页校验**:以固定随机种子抽 ≥20 页比对树,必须完全一致,否则退出码 2;可用页面
  不足 20 时改为校验全部可用页;`-pages` 子集模式跳过跨页校验并在输出中记录警示。
- **文件映射**:树路径 → `<root>/<路径>/index.html`,全部必须存在(基线 854/854 零缺失)。
- **收录范围**:仅 `/docs/` 树;排除 `_print/`、`/en/` 镜像树(规范根为 `/docs/`,与
  kubernetes.io 规范 URL 一致)及 blog/careers 等非 docs 内容。
- **孤儿页**:docs 树内、在磁盘但不在树中的页面(基线 218,如 v1.23–v1.36 API 参考归档),
  记入全局 manifest,不处理。

### 1.8 链接与资产规范化(页面提取时内联完成)

规范化是纯函数,输入全部静态(树、`_redirects`、base origin),因此在阶段②内联执行,
不依赖处理顺序:

1. 剥离构建期 base origin(取 `<root>/build-info.json` 的 `base_url` 字段,实测
   `http://localhost:1313/`;渲染树中部分绝对链接被烧入);
2. 查 `_redirects` 解析旧路径(433 条 docs 重定向;单跳解析 + 环路保护,解析结果再剥 origin);
3. 分类:
   - 站内(`/docs/` 前缀)→ 库内站点路径引用;目标在树中计 `internal`,指向孤儿页照常
     输出并计 `to_orphans`,既不在树也非孤儿 → 计入 `out_of_tree`;
   - 页内(`#anchor`)→ 原样;
   - 外部(`http(s)`)→ 原样惰性文本,不提供任何跟随工具;
   - 其他 scheme(`javascript:` 等)→ 丢弃 href 只留文本,计 `dropped`;
4. 图片:校验本地文件存在于渲染树,记 sha256 digest 进 `assets`;SVG 不解析。远程图片不能成为
   离线库输入,丢弃节点并计 `dropped_elements`,不下载也不保留远程 URL。

### 1.9 digest 与确定性

- 页 digest = `index.md` 字节 sha256;章节 digest = 该章节在 `index.md` 中字节段的 sha256;
  资产 digest = 文件字节 sha256;redirects digest = `_redirects` 字节 sha256。
- 生成物中**禁止墙钟时间、随机数、map 遍历序**;所有集合输出前排序。
- 输出与 worker 数、处理顺序无关(纯函数 + 原子写 + 汇编按路径排序读取)。
- 同一渲染树 + 同一 `-version` → 字节级相同输出(验收项)。

### 1.10 生成流程(四阶段)

```text
① 树提取(串行):td-section-nav → 树;跨页校验;文件映射;加载重定向表
② 页面提取(并行):goroutine 池,一页一任务,产出 index.md + index.json
   链接/资产规范化在此内联完成;单页失败记录后继续
③ 汇编(串行):聚合页 manifest、统计、悬空链接与资产校验、写全局 manifest
④ 验收:由 CI / make docs-project-golden 做字节级比对
```

### 1.11 测试与验收

- **单元**:合成 HTML 错误路径(main 缺失、文件超限、畸形 HTML、锚点缺失),沿用现有测试风格。
- **golden fixture**(`test/fixtures/docs-project/`,由 `make docs-fixture` 冻结,清单固定):
  - `docs/home/`(index 页、卡片、base_url 泄漏样本);
  - `docs/concepts/workloads/pods/pod-lifecycle/`(feature-state、警示框、代码、glossary 链接);
  - `docs/concepts/workloads/autoscaling/horizontal-pod-autoscale/`(长文、公式、多级章节);
  - `docs/concepts/services-networking/ingress/`(内容图片);
  - `docs/tasks/configure-pod-container/configure-liveness-readiness-startup-probes/`(tabs、多代码块)。
- **确定性**:同输入连跑两次 `diff -r` 为空;`-workers 1` 与 `-workers 8` 结果一致。
- **断点续跑**:删除一半页面输出后 `-resume` 重跑,其余文件字节不变。
- **规模**:854 页全量在 8 worker 下分钟级完成,`report.json` 零失败。

### 1.12 实现契约:不变量与自由度

- **不变量(必须遵守)**:1.2 输出布局、1.3/1.4 schema 与字段语义、1.5 生成规则、1.8 规范化
  顺序、1.9 digest 与确定性定义、1.1 退出码、禁止网络与运行时依赖、`-version` 语义。
- **实现自由度**:内部包结构与类型设计、是否引入 goquery、HTML 遍历细节、错误信息措辞、
  单测组织方式。

### 实施任务

- [x] `cmd/docs-project` + `internal/docsproject` 骨架:CLI、Makefile 目标
      (`docs-project` / `docs-fixture`)、`.gitignore` 追加输出目录。
- [x] 目录树提取:语言块剥离、跨页一致性校验、文件映射、孤儿报告。
- [x] 页面提取器:块级与行内规则、剥离清单、防御项(行号槽等)。
- [x] 链接与资产规范化:base origin、重定向解析、分类与消毒、资产存在性与 digest。
- [x] 页 manifest 与全局 manifest、全部 digest 计算。
- [x] 断点续跑与失败报告(`report.json`、退出码语义)。
- [x] 单元测试、golden fixture、确定性与并行一致性测试、断点续跑测试。
- [x] 854 页全量生成验证:零失败、`diff -r` 复跑一致、统计与基线数(854/218/433)吻合。

### 1.13 提交计划与提交纪律

**纪律(先于计划,必须遵守)**:

- **做完一个提交单元就立即提交,不攒批**。每个提交是一个独立可审查的边界;每提交后
  `go build ./...` 必须通过,当次提交涉及的包其 `go test` 必须通过;不允许出现"先留缺口、
  等后续提交补回"的不可用中间态。
- **勾选跟着提交走**:某条任务的实现与验证落在哪个提交,对应 TODO 勾选就随该提交(或其
  紧随的 `docs(plan)` 提交)更新;禁止收尾时批量补勾。
- **设计与代码同步变更**:实施中发现映射规则、schema 或输出布局需要调整时,必须先更新
  1.2–1.10 的对应条目并递增 `-version` 语义说明,文档与代码在同一提交内完成,保持本规格
  与实现一致。
- 提交信息沿用仓库格式:`类型(范围): 摘要` + 结构化正文(Boundary / Contracts / Validated /
  Remaining);涉及 schema、输出布局或映射规则变化的提交必须在正文注明。

**提交序列**(按序执行;一个单元允许多次提交,不允许跨单元合并):

1. `feat(docsproject): add generator skeleton with CLI and Makefile wiring`
   `cmd/docs-project`、`internal/docsproject` 包骨架、参数解析、输入错误退出路径、
   Makefile `docs-project`/`docs-fixture`、`.gitignore` 追加输出目录。
   验证:参数与退出码单测。
2. `feat(docsproject): extract sidebar tree with cross-page validation`
   `td-section-nav` 提取、语言切换块剥离、跨页一致性校验、文件映射、孤儿清单。
   验证:合成侧边栏单测(真实页的树提取测试随提交 7 的冻结 fixture 补齐)。
3. `feat(docsproject): extract page content into restricted Markdown`
   main 定位、剥离清单、块级/行内映射、`index` 页链接清单块。
   验证:逐元素规则的合成 HTML 单测。
4. `feat(docsproject): normalize links and assets`
   base origin 剥离、重定向解析与环路保护、链接分类与消毒、资产存在性与 digest。
   验证:表驱动单测(含 `javascript:` scheme、`out_of_tree`、重定向环路)。
5. `feat(docsproject): emit page and global manifests with digests`
   两级 manifest schema、digest 计算、原子写、汇编阶段。
   验证:manifest schema 校验单测、digest 稳定性单测。
6. `feat(docsproject): support resume and failure reporting`
   `-resume` 语义与版本匹配、`report.json`、退出码 1/2 全量接线。
   验证:断点续跑单测(删一半输出重跑,其余文件字节不变)。
7. `test(docsproject): freeze real-page fixtures and add golden determinism suite`
   `make docs-fixture` 冻结 1.11 清单的 5 页、golden 字节级比对、双跑 `diff -r`、
   `-workers 1/8` 一致性。fixture 体积大,单独成提交便于审查。
   验证:golden 套件在 CI 通过。
8. `docs(plan): close document library generator acceptance`
   记录 854 页全量运行证据(零失败、复跑一致、统计与 854/218/433 基线吻合),
   勾选第 1 节全部任务;此后进入第 2 节运行时切换。

### 验收记录(2026-09-16)

- `go run ./cmd/docs-project -root docs-site/public -out <tmp> -workers 8 -version docs-project-v4`
  连续运行两次,`diff -r` 为空;同一输入以 `-workers 1` 运行后与 8 worker 输出的 `diff -r` 也为空。
- 两次全量运行均无 `report.json`;全局清单为 854 pages、218 orphans、433 docs redirects,
  另有 127 index pages、13,326 anchors、69 assets。

## 2. 运行时切换到文档库

- [ ] Server 端文档 Reader 改为读取文档库并校验 digest,移除运行时 HTML 解析路径。
- [ ] 从生产装配与部署配置中移除 `source_root` 挂载及 `ReadSource`/`ReadInclude` 能力。
- [ ] 文档库进入部署:以只读卷(或镜像层)方式提供给 Server——854 页产物约数十 MB,
      超出 ConfigMap 1MiB 上限,不使用 ConfigMap;`snapshot_root` 随切换一并退场,Server
      的唯一文档输入为文档库。
- [ ] 流水线证据切换为结构化章节文本(正文 + 围栏代码块);生成 Agent 从投影获得逐字代码,
      替换当前的压平文本。
- [ ] 工作流证据绑定 = 页面 digest + 解析器版本 + 上游 commit,三元组进入 artifact ledger 与
      PublicationManifest;AgentRun 审计继续只记录模型、提示词与工具面版本。
- [ ] 更新 `docs-smoke` 与文档 E2E:fixture 换为真实渲染页(注意 ConfigMap 1MiB 上限,
      必要时改卷挂载)。

## 3. 多页实践工作流(依赖第 1、2 项的文档库与页面清单)

- [ ] 工作流按页面/锚点铺开,解除当前"单页单锚点"的部署钉死;状态机、门禁、恢复逻辑复用。
- [ ] 页面处理调度策略待定(按 section 优先级、失败重试、限速),在设计确认后拆解为独立任务。
