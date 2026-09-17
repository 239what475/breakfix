# 下一阶段:结构化文档库与多页实践流水线

上一阶段"公共运行底座与文档实践自动流水线"已于 2026-09-16 完成全部任务并通过验收,收口提交为
`docs(plan): close legacy runtime removal`;完整任务列表与勾选证据见该时点的 TODO.md 历史。

本阶段第一步"离线文档库生成器"亦已完成并通过独立验收(版本 docs-project-v6,全量基线
854 页 / 217 孤儿 / 433 重定向,双跑字节一致);实施规格、提交序列与验收记录见 git 历史
(`docs(plan): record v6 acceptance for the document library generator`)。

目标:把文档内容输入从"运行时解析渲染 HTML + 可读源码树"收敛为**单一离线编译产物**。固定上游
commit 经 Hugo 构建得到渲染树,再由离线解析程序把整棵渲染树投影为结构化文档库;Server、
规划/生成/审核 Agent 与人只读文档库,不再接触 HTML 解析和上游源码。

```text
固定上游 commit
    ↓  make docs-build(Hugo,确定性)
渲染树 docs-site/public(完整站点)
    ↓  make docs-project(解析程序,完全离线,确定性)
结构化文档库(每页一份格式化文档 + digest 清单 + 页面清单 + 引用资产)
    ↓
├── 人直接读(documents/**/*.md:调试、审阅)
└── Agent 流水线读(规划 / 生成 / 审核的唯一内容输入)
```

已确认的设计决策:

- 渲染树是唯一内容输入,不读上游源码;证据粒度为页面 + 锚点 + 页面 digest + 解析器版本,
  不引入源码文件 / include 级别的证据链。
- 身份仍只认上游 commit;文档库是证据输入,不参与身份。
- 文档库生成完全离线、确定性:同一 commit + 同一解析器版本 → 同一文档库。
- **多语言暂不实施**(2026-09-16 决定):`locale` 元数据已预留、生成器语言无关;真要做时需要
  构建参数化(docs-site 当前 `--renderSegments en` 钉死 en,中文页面目前不存在)、
  `/zh-cn/docs/` 路径前缀与重定向表参数化(现有 11 条 zh-cn 重定向被 `/docs/` 过滤器排除)、
  各语言独立基线与库根;产品侧(实践索引按语言隔离、翻译滞后呈现、阅读入口)另行决策。

提交纪律(全程有效):做完一个可审查单元立即提交,不攒批;每提交保持构建与当包测试通过;
TODO 勾选随对应提交更新,禁止收尾批量补勾;规格与实现变更在同一提交内同步;提交信息沿用
仓库格式(`类型(范围): 摘要` + Boundary/Validated 结构化正文)。

勾选状态按当前代码与测试证据维护;未勾选项表示仍有实现或验收工作未完成。

## 1. 资产入库:把被引用图片拷进文档库(实施规格)

背景:当前库内 md 引用图片(`![alt](docs/images/ingress.svg)`),manifest 记录了 digest,
但文件本体仍在渲染树,库不自包含;第 2 节的 `snapshot_root` 退场依赖库完全自包含。本节把
**被引用的**资产按站点路径拷入库。全量实测:被引用资产约 60 余个、约 5 MB(以 SVG 为主、
少量 PNG),体积代价可忽略。

### 1.1 行为规格

- 拷贝发生在**阶段③汇编**:汇总全部页 manifest 的 `assets` 引用集合,把每个资产从渲染树
  按原站点路径拷入库根(如 `documents/docs/images/ingress.svg`、`documents/images/docs/pod.svg`)。
- **只拷被引用的资产**,不拷贝渲染树整个 `/images`(其中含孤儿页与其他语言的资产)。
- 拷贝前校验源文件字节 sha256 与页 manifest 记录的 digest 一致;不一致即该次运行失败,
  退出码 1 并写入 `report.json`。
- 资产为字节原样拷贝,不做任何转换;SVG 仍不解析。不使用 base64 内嵌进 md。
- 原子写(临时文件 + rename),与页面输出同一纪律。
- `-resume` 语义覆盖资产:已存在且 digest 匹配则跳过;缺失则重拷;存在但不匹配即失败。
- 全局 manifest `stats` 新增 `assets_copied` 字段(拷入库的资产数);不新增 `warnings`。
- **页面 md/json 输出不得因本变更改变字节**(库内只新增资产文件)。
- 版本串升 **docs-project-v7**(输出布局变化),Makefile `DOCS_PROJECT_VERSION` 与测试同步。

### 1.2 验收标准

- [x] 全量运行后,库内资产文件集合与页 manifest 引用集合**双向一致**(无缺失、无多余文件);
      `stats.assets_copied` 与实际拷贝数一致。
- [x] 双跑 `diff -r` 一致(含资产文件);`-workers 1/8` 一致。
- [x] `-resume` 删除全部资产后重跑:资产恢复且字节不变,页面文件不受影响。
- [x] golden fixture 更新并断言资产出现在输出(fixture 目录已含真实图片文件,含站点根路径
      下的 `images/docs/pod.svg`)。
- [x] 单元测试:digest 不匹配的失败路径、resume 跳过与重拷、只拷被引用集合。

### 1.3 提交计划

1. [x] `feat(docsproject): copy referenced assets into the document library`
   规格同步(TODO 本节)、实现、单测、golden 更新、全量验收证据同提交;提交信息注明
   输出布局变化与版本 v7。验证:`go test ./internal/docsproject/`、双跑 `diff -r`、
   resume 测试、全量零失败。

### 验收记录(2026-09-17,docs-project-v7)

- `go test ./internal/docsproject/ -count=1` 通过;新增单测覆盖仅拷贝页 manifest 引用集合、
  源资产 digest 失配写入 `report.json` 并以退出码 1 失败、`-resume` 命中跳过与删除资产后的原样恢复。
  real-page golden 已纳入 `docs/images/{ingress.svg,ingressFanOut.svg,ingressNameBased.svg}` 与
  站点根 `images/docs/pod.svg`;页面 Markdown 内容未改。
- 从 `docs-site/public` 各执行两次 `-workers 8` 与一次 `-workers 1`,三个 v7 输出的
  `diff -r` 均为空;全量清单为 854 pages、217 orphans、433 redirects、127 index pages、
  12,476 anchors、69 页资产引用、63 个去重后拷入资产,无 `report.json`。
- 从全量页 manifest 枚举并删除全部 63 个库内资产后以 `-resume` 重跑,资产集合和全部字节恢复;
  与完整基线 `diff -r` 为空,1,708 个页面 `index.md`/`index.json` 文件摘要不变。

### 1.4 库内路径改为文件相对路径(docs-project-v8)

背景:v5 规格把 `.md` 内链接与图片定为"以输出根为基准的站点路径"。该形式只有在消费方
知晓库根时才能解析——直接用编辑器或 GitHub 打开 md 时,站内链接与已入库图片全部显示为
断链。既然资产已入库(1.1–1.3),"人直接读"要求 md 在任意渲染器中自洽,路径形式改为
**相对当前 md 文件所在目录**。

行为规格:

- `.md` 中的站内链接与图片改为相对路径:由页面自身站点路径(如 `docs/a/b/page/`)计算
  到目标站点路径的相对引用(如 `../../../reference/glossary/#term-cluster`);目标为页面
  时保留尾部斜杠;指向自身目录时输出 `./`;站点根路径资产(如 `images/docs/pod.svg`)
  同样按相对路径计算。外部链接与页内 `#anchor` 不受影响。
- **JSON manifest 不变**:`links` 统计与 `out_of_tree` 等字段继续记录库根规范站点路径,
  机器消费方无需解析相对路径;需要 md 内位置时按"md 文件目录 + 相对路径"解析。
- 所有页面 digest 随之变化,版本串升 **docs-project-v8**;golden 重新生成。

验收标准:

- [x] 抽查:pod-lifecycle 内 glossary 链接为相对形态;ingress 页图片引用从 md 文件位置
      可解析到库内实际资产文件;相对链接经"md 目录 + 相对路径"解析后回到目标规范路径。
- [x] JSON manifest 的链接统计与 v7 完全一致(仅路径形式变化,不改分类)。
- [x] 双跑 `diff -r` 一致;资产集合仍为 63 且双向一致;单测覆盖相对路径边界
      (同目录 `./`、深层嵌套、根路径资产)。
- [x] golden 按 v8 重新生成并通过。

提交:`fix(docsproject): emit file-relative references in page markdown`,规格同步、
实现、单测、golden 同提交;提交信息注明 md 输出契约变化与版本 v8。

### 1.5 定义列表 `<dl>` 的无损提取(docs-project-v9)

背景:v8 及之前把 `dl`/`dt`/`dd` 当作透明容器,块级遍历会**丢弃 `<dd>` 内的纯文本节点**,
仅行内元素侥幸成为独立段落——实测 `docs/concepts/containers/images/` 的
`imagePullPolicy` 定义列表三条描述全部丢失,全库 **45 页、78 处 `<dl>`** 受影响。

行为规格:

- `<dl>` 映射为:每个 `<dt>` 渲染为一行 `**术语**`(术语内保留行内规则,如
  `` **`IfNotPresent`** ``);每个 `<dd>` 渲染为其后的独立段落——`<dd>` 仅含行内内容时
  按行内规则合成为一段,含块级子元素(`p`/`ul`/`pre`/`table` 等)时按块规则展开。
- 孤立于 `<dl>` 之外的 `dt`/`dd` 按同样规则单独渲染,不丢弃、不计 `dropped_elements`。
- 版本串升 **docs-project-v9**;仅含 `<dl>` 的页面 digest 变化,其余页面字节不变;
  链接分类与统计不受影响。

验收标准:

- [x] `docs/concepts/containers/images/` 的三条 `imagePullPolicy` 描述完整出现在
      `index.md`,术语为加粗行、描述为后续段落、行内链接正常;
- [x] 全库 78 处 `<dl>` 所在页面的 `<dd>` 文本不再丢失(抽样若干页比对渲染 HTML 正文);
- [x] 不含 `<dl>` 的页面输出与 v8 字节一致;双跑 `diff -r` 一致;golden 按 v9 重新生成。
- [x] 单测:行内型 dd、块级子元素型 dd、孤立 dt/dd、多组 dt-dd 交错。

提交:`fix(docsproject): render definition lists without dropping term text`。

### 1.6 全库逐页审查修复(docs-project-v10)

背景:按 `docs-site/document-checklist.md` 的任务规格完成了**全库 854 页**逐页对照审查
(2026-09-17,基线 v9)。审查确认内容面总体可靠(链接改写、代码块、feature-state、图片
入库、genref 大表等大面积通过),同时暴露出一批**共性问题模式**。本节把确认为生成器
缺陷的模式统一修复;审查中的误报经复核后不予修改,记录在案。

确认的缺陷模式与修复规格:

1. **块级裸文本与行内碎片化(①②④⑤ 的共同根因)**:`blocks()` 直接丢弃块级 `TextNode`,
   行内元素(`a`/`code`/`strong`/`span`…)又各自成段。后果:h3 后无 `<p>` 包裹的正文整段
   丢失(architecture/controller);`div.lead` 副标题丢失(多数索引页);
   `alert-secondary` 第三方 callout 正文丢失(addons 等十余页);`dd` 内裸文本丢失
   (statefulset `Recreate`、taint `NoExecute` 引导句);`metric_name`/`metric_help` 丢失
   (instrumentation/metrics 全部 605 项);glossary "Also known as:" 标签丢失;警示框内
   裸文本+链接被拆成多段(全库高频"碎片化")。修复:`blocks()` 把**连续的行内节点与
   文本节点合成为一个段落**,块级元素仍按块渲染;`alert()`/`details()` 的内容遍历使用
   同样的分组规则。
2. **自闭合锚吞块**:上游使用 XHTML 风格 `<a id="x" />`,HTML5 解析器视为未闭合标签,
   其后的标题/段落被吞入该 `<a>`,经行内路径渲染后粘连(授权页 "Warning:Enabling")。
   修复:块级遍历把**无 `href` 的 `<a>` 视为透明容器**展开处理。
3. **列表项内块级子元素粘连与乱序(⑥ 及概念框粘连)**:`list()` 把 `li` 的块级子元素
   (`p`/`pre`/警示框/子列表)拼接进同一行——围栏与 `> [!NOTE]` 粘在条目文字行尾、围栏
   配对错位;子列表统一后移导致"尾段并入首段、子列表乱序"。修复:重写列表渲染,`li`
   内容按**文档序**渲染为块序列,首行挂条目标记,后续行按内容列缩进;嵌套列表保持原位。
4. **同源非 `/docs/` 链接被丢弃(③)**:`/blog/…`、`/releases/…`、`/dockershim` 等站点
   根路径(含重定向目标跳出 `/docs/` 树的链接,如 `setup/release/version-skew-policy/`)
   落入 `referenceLocal` 分类后被 `link()` 丢弃,全库数十处博客/版本偏差策略链接
   退化为纯文本。修复:此类链接以**站点正式 origin** 拼成绝对外部链接保留(计入
   `external`);origin 由生成器参数 `-site-origin` 显式钉住(Makefile 传
   `https://kubernetes.io`,记入 manifest 的 `site_origin` 字段)——渲染树自带的
   `build-info.json` base_url 是本地 Hugo 地址,仅在未传参时兜底。空文本锚点
   (glossary permalink 等)不再输出 `[](#…)` 噪音。
5. **重定向目标自带锚点丢失(⑧)**:`_redirects` 目标本身携带锚点(如
   `…/storage/csi-driver-v1/#TokenRequest`)时,路径规范化把 `#…` 一并吞掉。修复:
   重定向解析分离路径与锚点——**目标锚点优先,无目标锚点时保留来源锚点**(与 Netlify
   语义一致)。
6. **glossary 术语链接化(已知问题 1)**:`a.glossary-tooltip` 在官网视觉上是纯文本+悬停
   释义(294 页、1,333 处)。修复:该类锚点渲染为**纯术语文本**,不输出链接。
7. **不换行空格丢失**:块边界/行内拼接用首**字节**判断空白,U+00A0(及任何多字节空白)
   首字节非空白导致前导空格被吞("step **2**in")。修复:按**码点**判断边界。
8. **上标/下标丢失(⑩)**:`2<sup>26</sup>` 退化为 "226"。修复:`sup`/`sub` 内容映射为
   Unicode 上/下标字符(数字、`+` `-` `(` `)` `=` `n` `i` 等),无法映射时回退纯文本。
9. **degraded 表格不可渲染(⑪)**:含 rowspan/colspan 的表格降级输出为无表头分隔行的
   竖线文本,渲染器不识别为表格。修复:降级路径同样输出 GFM 形态(首行作表头 +
   `| --- |` 分隔行 + 单元格转义),合并单元格信息仍按既有规则拉平,
   `degraded_tables` 统计不变。
10. **metrics 标签连写**:`label`/`span` 相邻无源码空格时(CSS 徽章式排版)值粘连
    ("nameoperationrejectedtype")。修复:行内拼接在相邻 `label`/`span` 边界且两侧
    无空白时补一个空格;`sup`/`sub`/`code` 边界不补(避免破坏 `2²⁶` 与行内码)。

复核后的审查误报(不改,记录结论):cluster-administration/networking 的图片相对路径
`../../../images/` 实际正确解析到 `docs/images/`(审查者层级算错);workload-api 尾部
列表标记存在;cpu-management-policies 的 node-resource-managers 链接被 `_redirects`
正确改写为新路径;`kubectl-commands#命令` 类锚经 `_redirects` 改写后悬空与线上行为
一致(重定向表即如此指定),视为上游等效。表格 `<caption>` 仍不输出——caption 常为
`display:none`,渲染树内无法可靠判断可见性,记为已知限制。

验收标准:

- [x] 审查记录中标注①②④⑤⑥⑧⑩⑪ 与 glossary/metrics/博客链接问题的代表页
      (architecture、addons、cloud-controller、lead 索引页、statefulset、
      instrumentation/metrics、authorization、secret、cluster-ip-allocation、
      system-metrics、flow-control、endpoint-slices、glossary)逐一复验通过;
- [x] v9→v10 全库 diff 中**变化的行均可归因**于上述模式之一(抽样比对),无意外页面变化
      (854 页集合不变,唯一字节不变页为 `docs/home` 纯索引页);
- [x] 链接统计口径更新:同源非 `/docs/` 链接从 `dropped` 移入 `external`
      (全库 dropped 294→179);internal 减少约 1,289 为 glossary tooltip 转纯文本;
      资产集合与 v9 一致;锚点总数 12,476 不变;
- [x] 双跑 `diff -r` 一致;workers 1/8 一致;golden 按 v10 重新生成;
      新增单测覆盖各模式(裸文本合段、自闭合锚、列表块序、tooltip 纯文本、
      多字节空格、上/下标、降级表格、越树重定向、空锚丢弃);
- [x] `make docs-project` 刷新本地产物,`document-checklist.md` 末尾追加修复闭环记录。

提交:`fix(docsproject): restore bare text, list blocks, and site links from review`,
规格、实现、单测、golden 同提交;版本串升 **docs-project-v10**。

## 2. 运行时切换到文档库(前置:第 1 节资产入库完成)

- [ ] Server 端文档 Reader 改为读取文档库并校验 digest,移除运行时 HTML 解析路径。
- [ ] 从生产装配与部署配置中移除 `source_root` 挂载及 `ReadSource`/`ReadInclude` 能力。
- [ ] 文档库进入部署:以只读卷(或镜像层)方式提供给 Server——854 页产物加资产约数十 MB,
      超出 ConfigMap 1MiB 上限,不使用 ConfigMap;库自包含后 `snapshot_root` 随切换一并
      退场,Server 的唯一文档输入为文档库。
- [ ] 流水线证据切换为结构化章节文本(正文 + 围栏代码块);生成 Agent 从投影获得逐字代码,
      替换当前的压平文本。
- [ ] 工作流证据绑定 = 页面 digest + 解析器版本 + 上游 commit,三元组进入 artifact ledger 与
      PublicationManifest;AgentRun 审计继续只记录模型、提示词与工具面版本。
- [ ] 更新 `docs-smoke` 与文档 E2E:fixture 换为真实渲染页(注意 ConfigMap 1MiB 上限,
      必要时改卷挂载)。

## 3. 多页实践工作流(依赖第 1、2 项的文档库与页面清单)

- [ ] 工作流按页面/锚点铺开,解除当前"单页单锚点"的部署钉死;状态机、门禁、恢复逻辑复用。
- [ ] 页面处理调度策略待定(按 section 优先级、失败重试、限速),在设计确认后拆解为独立任务。
