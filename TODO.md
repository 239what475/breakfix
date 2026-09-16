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
