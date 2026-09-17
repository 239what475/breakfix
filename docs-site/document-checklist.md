# 文档库逐文件对照审查任务

## 目标

对照钉住渲染树 `docs-site/public` 与生成文档库 `docs-site/documents`,逐页核对生成内容
忠实无误,找出所有残存的提取问题。本文件同时是进度记录:每检查完一页,立即在末尾追加
该页路径与审查结论。

## 范围与顺序

- 共 854 页,顺序以 `docs-site/documents/manifest.json` 的 `pages` 列表为准(字典序),
  一次只检查一个页面。
- 审查基线为 **docs-project-v9**;若审查期间生成器升版重生成,已完成且结论为"通过"的页
  无需重查(可从 manifest 的 generator_version 确认基线)。

## 对照基准(先读清楚再动手)

- **内容忠实度以本地 `docs-site/public/<路径>/index.html` 为准**——它是钉住的上游版本。
  官网 kubernetes.io 与钉住版本存在正常的内容差异,这类差异**不是问题**。
- 官网只用于辅助判断"视觉呈现"类问题(例如:某个词在官网是纯文本还是链接、悬停有没有
  释义提示)。无法本地判断视觉时,到官网对应页面看一眼。
- 查看 HTML:浏览器直接打开本地 index.html 文件即可;正文都在 `<main>` 元素内,忽略侧边栏、
  面包屑、页脚、Feedback 组件等站点外壳。

## 每页检查步骤

1. 同时打开 `docs-site/public/<路径>/index.html` 与 `docs-site/documents/<路径>/index.md`。
2. 从页面标题(h1)开始**逐节对照**,每一项都过一遍:
   - **文本完整性**:段落、列表项、定义列表描述、表格单元格的文字,不丢失、不多出、
     不串节;
   - **链接**:站内链接是文件相对路径且目标正确;外部链接保留;**官网视觉上是
     "纯文本 + 悬停释义"的术语**(HTML 中为 `a.glossary-tooltip`)不应渲染成链接
     (见已知问题 1,页级标注即可,不必逐条抄录);
   - **代码块**:与渲染页逐字一致、语言标记正确、无行号槽等杂质;
   - **表格**:行列完整,单元格内容不缺;
   - **定义列表**:术语与描述完整成对;
   - **警示框 / feature-state / 图片 / tab 面板**:形态正确、位置正确。
3. (可选)`index.json` 的 `anchors` 与 `links` 统计与 md 实际内容一致。

## 记录格式

检查完一页,立即在本文件末尾追加一行,三档结论:

```text
- [x] docs/concepts/architecture/controller/ — 2026-09-17 — 通过
- [x] docs/concepts/containers/images/ — 2026-09-17 — 问题:①"Image pull policy"一节
      的 IfNotPresent 描述缺失(HTML <dd> → md 无此段);②……
- [x] docs/xxx/ — 2026-09-17 — 无法判断:<具体原因,附 HTML 与 md 的摘录>
```

"有问题"必须写清三要素:HTML 位置 → md 现状 → 期望表现。写不清的问题按"无法判断"记录。

## 已知问题(审查前必读)

1. **glossary 术语被渲染为链接(已确认待修,不必逐条记录)**:HTML 中
   `<a class='glossary-tooltip' …>cluster</a>` 在官网视觉上是纯文本、悬停显示释义,
   不是链接;当前 md 输出为普通链接(如 `[cluster](../../../reference/glossary/#term-cluster)`),
   且 `?all=true` 查询被剥离。全库 294 页共 1,333 处。遇到时在页级结论标注
   "含 glossary 术语链接化"即可,后续由生成器统一修复。
2. **历史已修、若再出现须详细记录的问题类型**:定义列表(`<dl>`)描述丢失;代码块输出为
   单反引号行内码;旧路径链接未按 `_redirects` 改写;Feedback/面包屑/copycode 残留。

## 纪律

- 一次一页,查完即写,不攒批;本文件就是唯一进度记录。
- 发现问题**不要直接修改 `documents/` 下的产物**——重新生成会覆盖;所有问题记录在案,
  由生成器统一修复并递增版本。
- 以本地渲染树为准,不引入官网新版本的差异作为"问题"。

## 审查记录

- [x] docs/concepts/ — 2026-09-17 — 通过,含 glossary 术语链接化
- [x] docs/concepts/architecture/ — 2026-09-17 — 问题:①"cloud-controller-manager"一节首段丢失
      (HTML h3 后紧跟正文 "A Kubernetes control plane component that embeds cloud-specific control
      logic.…only interact with your cluster." 整段缺失,md 该节仅剩一行 glossary 链接
      `[control plane](…)`,疑因该段是 h3 后裸文本、无 <p> 包裹而漏提取);
      ②"kube-proxy (optional)"一节末句丢失(HTML "If you use a network plugin that implements packet
      forwarding for Services by itself,…run kube-proxy on the nodes in your cluster." 缺失,
      md 仅剩一行 `[network plugin](#network-plugins)`);
      ③h1 下方 lead 副标题 "The architectural concepts behind Kubernetes."(div.lead)未提取,
      md 标题后直接进入正文;另含 glossary 术语链接化
- [x] docs/concepts/architecture/cgroups/ — 2026-09-17 — 通过,含 glossary 术语链接化
- [x] docs/concepts/architecture/cloud-controller/ — 2026-09-17 — 轻微问题:Design 节 Note 警示框
      (HTML alert-info 内单一段落 "You can also run the cloud controller manager as a Kubernetes
      addon rather than as part of the control plane.")在 md 的 `> [!NOTE]` 中被拆为三段
      ("…as a Kubernetes" / `[addon](…)` 独占一段 / "rather than as part of the control plane."),
      文字无缺失、位置正确,仅段落结构碎片化;另含 glossary 术语链接化
- [x] docs/concepts/architecture/control-plane-node-communication/ — 2026-09-17 — 轻微问题:
      SSH tunnels 节 Note 警示框在 md `> [!NOTE]` 中被拆为三段("…you know what you are doing. The" /
      `[Konnectivity service](#konnectivity-service)` 独占一段 / "is a replacement…"),文字无缺失;
      另含 glossary 术语链接化
- [x] docs/concepts/architecture/controller/ — 2026-09-17 — 问题:导语第 4 段丢失(HTML h1 后
      裸文本段落 "In Kubernetes, controllers are control loops that watch the state of your
      cluster, then make or request changes where needed. Each controller tries to move the
      current cluster state closer to the desired state." 整段缺失,md 仅剩一行
      `[cluster](../../../reference/glossary/#term-cluster)`,与 architecture 页同类问题:
      无 <p> 包裹的裸文本+glossary 链接被漏提取);另含 glossary 术语链接化
- [x] docs/concepts/architecture/garbage-collection/ — 2026-09-17 — 轻微问题:"Garbage collection
      for unused container images" 节 Note 警示框被拆为三段("…causing the kubelet to wait the full" /
      `imageMaximumGCAge` 独占一段 / "duration before qualifying…"),文字无缺失;
      另含 glossary 术语链接化
- [x] docs/concepts/architecture/leases/ — 2026-09-17 — 通过,含 glossary 术语链接化
- [x] docs/concepts/architecture/mixed-version-proxy/ — 2026-09-17 — 轻微问题:"Peer-aggregated
      discovery" 节 Note 警示框被拆为 6 段(链接与行内码各独占一段:"…only supported for" /
      `[Aggregated Discovery](…)` / "requests to the" / `/apis` / "endpoint and not for" /
      `[Unaggregated (Legacy) Discovery](…)` / "requests."),文字无缺失、位置正确;
      另含 glossary 术语链接化
- [x] docs/concepts/architecture/nodes/ — 2026-09-17 — 轻微问题:3 处 Note 警示框段落碎片化
      (①DaemonSet 一段拆为 3 段;②"reserve resources for system daemons" 拆为 3 段且句尾
      "." 单独成段;③Self-registration 的 --node-labels Note 中链接独占段),文字均无缺失;
      另含 glossary 术语链接化
- [x] docs/concepts/architecture/self-healing/ — 2026-09-17 — 通过
- [x] docs/concepts/cluster-administration/ — 2026-09-17 — 轻微问题:h1 下方 lead 副标题
      "Lower-level detail relevant to creating or administering a Kubernetes cluster."(div.lead)
      未提取(同 architecture 页);其余列表与链接逐项一致
- [x] docs/concepts/cluster-administration/addons/ — 2026-09-17 — 问题:页面顶部 third-party-content
      警示框(alert callout)正文丢失:HTML 中 "This section links to third party projects that
      provide functionality required by Kubernetes. The Kubernetes project authors aren't
      responsible for these projects, which are listed alphabetically. To add a project to this
      list, read the content guide before submitting a change." 整段缺失,md 仅剩三个碎片:
      "**Note:**"、`[content guide](…)`、`[More information.](#third-party-content-disclaimer)`
      (与 controller/cloud-controller 页同因:警示框 div 内裸文本+链接未包 <p> 被漏提取);
      页尾 disclaimer 两段完整;其余 20+ 列表项与链接逐项一致
- [x] docs/concepts/cluster-administration/admission-webhooks-good-practices/ — 2026-09-17 — 问题:
      ①"Choose an admission control mechanism"表格的 caption "Mutating and validating admission
      control in Kubernetes" 丢失(md 表格无此行);同表 Use cases 单元格在 HTML 为 <ul> 列表,
      md 拉平成句(文字无缺失);②"Test minor version upgrades…"节两处链接丢失:HTML
      `<a href="/releases/">Kubernetes release notes</a>`、`<a href="/blog/">Kubernetes blog</a>`,
      md(306-307 行)退化为纯文本;③"Examples of good implementations"节 third-party-content
      警示框正文丢失(与 addons 页同类,仅剩 "**Note:**"+content guide+More information 三个碎片);
      ④h1 下 lead 副标题 "Recommendations for designing and deploying admission webhooks in
      Kubernetes." 未提取;含 glossary 术语链接化
- [x] docs/concepts/cluster-administration/certificates/ — 2026-09-17 — 通过
- [x] docs/concepts/cluster-administration/compatibility-version/ — 2026-09-17 — 通过
- [x] docs/concepts/cluster-administration/coordinated-leader-election/ — 2026-09-17 — 通过,含 glossary 术语链接化
- [x] docs/concepts/cluster-administration/dra/ — 2026-09-17 — 通过,含 glossary 术语链接化
- [x] docs/concepts/cluster-administration/flow-control/ — 2026-09-17 — 问题:①HTML 中 3 处
      嵌在列表项内的 Note 警示框(handSize 说明、request_wait_duration_seconds 说明、
      request_queue_length_after_enqueue 说明),在 md 中 `> [!NOTE]` 标记直接粘在列表项句子末尾
      (如 126 行 "…overload situation. > [!NOTE]"),后续 `>` 引用行未按列表嵌套缩进,警示框
      脱离所属列表项成为独立块;②其余 7 个 Caution/Note 警示框均被拆为多段(链接、行内码、
      斜体各自独占段),文字无缺失;③两张表(11 行数据表无可见 caption,HTML caption 为
      display:none)、两个 YAML 代码样本(含文件名下载链接)逐行核对一致;
      含 glossary 术语链接化
- [x] docs/concepts/cluster-administration/kube-state-metrics/ — 2026-09-17 — 问题:①页面顶部
      third-party-content 警示框正文丢失:HTML "This item links to a third party project or
      product that is not part of Kubernetes itself." 缺失,md 仅剩 `[More information](…)`
      一行(同类警示框裸文本漏提取);②h1 下 lead 副标题 "kube-state-metrics, an add-on agent
      to generate and expose cluster-level metrics." 未提取;两个代码块与页尾 disclaimer 完整;
      含 glossary 术语链接化
- [x] docs/concepts/cluster-administration/logging/ — 2026-09-17 — 轻微问题:①"Log locations"
      节 Linux/Windows tab 面板转为 "- Linux / - Windows" 两个列表项 + "**Panel: Linux/Windows**"
      粗体标记,面板内容完整、顺序正确(形态转换,可接受);②"Sidecar container with a logging
      agent" 节 Note 被拆为 3 段(`kubectl logs` 独占段),文字无缺失;5 个 YAML 样本、4 张图片、
      2 个 feature state 逐项一致;含 glossary 术语链接化
- [x] docs/concepts/cluster-administration/networking/ — 2026-09-17 — 问题:"Kubernetes IP address
      ranges" 节图片断链:HTML src 为 /docs/images/kubernetes-cluster-network.svg,md 写作
      `../../../images/kubernetes-cluster-network.svg`,自 docs/concepts/cluster-administration/
      解析到根 images/ 目录,该文件不存在(正确应为 `../../images/kubernetes-cluster-network.svg`);
      其余文本、有序/嵌套列表、链接逐项一致;含 glossary 术语链接化
- [x] docs/concepts/cluster-administration/node-autoscaling/ — 2026-09-17 — 轻微问题:①h1 下 lead
      副标题 "Automatically provision and consolidate the Nodes…" 未提取;②3 处 Note 碎片化
      (*scale-up*、[vertical workload autoscaling](…)、*scale-down* 各独占段),文字无缺失;
      ③mermaid 图(pre.mermaid)转为无语言标注的代码块,源码完整(形态可接受);
      其余段落、锚点与外链逐项一致;含 glossary 术语链接化
- [x] docs/concepts/cluster-administration/node-shutdown/ — 2026-09-17 — 问题:①"What's next"节
      链接丢失:HTML "Blog: <a href="/blog/2023/08/16/kubernetes-1-28-non-graceful-node-shutdown-ga/">
      Non-Graceful Node Shutdown</a>." 的链接被剥离,md(220 行)为纯文本 "Blog: Non-Graceful Node
      Shutdown."(与 admission-webhooks 页 /blog/ 链接丢失同类);②"Enabling graceful node shutdown"
      节 Linux/Windows tab 内的 feature-state 原始 HTML 代码块系上游 HTML 本身以 <pre><code> 转义
      形式存在,md 忠实保留(非提取问题);③3 处 Note 碎片化(Ready、inhibitor locks、
      service control handler),文字无缺失;3 张表、YAML 配置逐项一致;含 glossary 术语链接化
- [x] docs/concepts/cluster-administration/observability/ — 2026-09-17 — 问题:①"Common observability
      tools" 节 third-party-content 警示框正文丢失(addons 页同类,md 仅剩 "**Note:**"+content guide+
      More information 碎片;紧随其后的完整段落版说明 "Note: This section links to third-party
      projects that provide observability capabilities…" 已保留);②h1 下 lead 副标题未提取;
      4 个 mermaid 图以源码块保留、图片与链接逐项一致
- [x] docs/concepts/cluster-administration/swap-memory-management/ — 2026-09-17 — 问题:"What's next"
      节链接丢失:HTML `<a href="/blog/2025/03/25/swap-linux-improvements/">blog post about
      Kubernetes and swap</a>` 被剥离,md(253 行)为纯文本 "You can check out a blog post about
      Kubernetes and swap"(/blog/ 链接丢失第 3 处);其余正文、dl(NoSwap/LimitedSwap)、代码块、
      警示框(2 处碎片化)逐项一致
- [x] docs/concepts/cluster-administration/system-logs/ — 2026-09-17 — 轻微问题:HTML 4 处
      alert-danger(Warning)在 md 中以 `> [!CAUTION]` 呈现(级别映射,内容完整),其中 3 处
      段落碎片化(*log output*/*not*、`nodes/proxy`/**get**、`Command line tool reference` 链接
      各独占段);两张表、4 个 feature state、全部代码块与链接逐项一致
- [x] docs/concepts/cluster-administration/system-metrics/ — 2026-09-17 — 轻微问题:"Metric
      lifecycle" 节示例代码与列表项粘连:HTML 为两个列表项("Before deprecation" / "After
      deprecation")各自内嵌 <pre> 代码块,md(54-63 行)写作 "- Before deprecation ```",围栏
      与列表文字同行,后续 ``` 配对错位、代码块结构破坏(全部文字仍在);其余正文、feature
      state(KubeletPSI)、代码块与链接逐项一致
- [x] docs/concepts/cluster-administration/system-traces/ — 2026-09-17 — 通过
- [x] docs/concepts/configuration/ — 2026-09-17 — 轻微问题:h1 下 lead 副标题 "Configuration
      mechanisms within Kubernetes." 未提取;其余正文、What's next(含嵌套列表)与 section-index
      列表逐项一致;含 glossary 术语链接化
- [x] docs/concepts/configuration/configmap/ — 2026-09-17 — 轻微问题:3 处警示框碎片化
      (Caution 中 `[Secret](../secret/)`、Note 中 `spec`/[static Pod]、Note 中
      `[subPath](…#using-subpath)` 各独占段),文字无缺失;9 个代码块(含 2 个带文件名下载链接
      的样本)、Immutable feature state、What's next 逐项一致;含 glossary 术语链接化
- [x] docs/concepts/configuration/manage-resources-containers/ — 2026-09-17 — 轻微问题:①"Container
      resources example" 节 HTML `2<sup>26</sup> bytes` 的上标丢失,md(128 行)写作 "226 bytes",
      语义由 2²⁶ 退化为 226;②多处 Note/Caution 段落碎片化(MemoryQoS、hugepages、sizeLimit、
      `~1` 编码、`kubernetes.io` 等),文字均无缺失;③2 处 NOTE 内嵌的 feature-state 原始 HTML
      代码块系上游 HTML 本身以 <pre><code> 转义存在,md 忠实保留(非提取问题);quantity、
      pod-v1 等链接改写经验证正确,表格与全部代码块一致;含 glossary 术语链接化
- [x] docs/concepts/configuration/organize-cluster-access-kubeconfig/ — 2026-09-17 — 轻微问题:
      开头 Note 警示框碎片化(*kubeconfig file* 与 `kubeconfig` 各独占段),文字无缺失;
      合并规则 6 条编号列表(含嵌套)、Proxy YAML、What's next 逐项一致
- [x] docs/concepts/configuration/secret/ — 2026-09-17 — 轻微问题:①"ServiceAccount token
      Secrets"节 TokenRequest 链接:HTML 指向 authentication-resources/token-request-v1/,按
      _redirects(593 行)应改写为 `storage/csi-driver-v1/#TokenRequest`,md 只写了
      `…/storage/csi-driver-v1/`,目标页面正确但重定向规则携带的 #TokenRequest 锚点丢失;
      ②多处 Note/Caution 碎片化(ls -l、stringData×2、known_hosts、subPath、privileged、
      immutable 等),文字均无缺失;内置类型表、8+ 代码样本、Immutable feature state 一致;
      含 glossary 术语链接化(9 处)
- [x] docs/concepts/configuration/windows-resource-management/ — 2026-09-17 — 通过,含 glossary 术语链接化
- [x] docs/concepts/containers/ — 2026-09-17 — 轻微问题:h1 下 lead 副标题 "Technology for
      packaging an application along with its runtime dependencies." 未提取;正文与 3 个子页
      卡片链接一致;含 glossary 术语链接化
- [x] docs/concepts/containers/container-environment/ — 2026-09-17 — 通过
- [x] docs/concepts/containers/container-lifecycle-hooks/ — 2026-09-17 — 轻微问题:PostStart 节
      Note 碎片化(`Running` 独占段),文字无缺失;其余正文、事件输出代码块、链接逐项一致
- [x] docs/concepts/containers/cri/ — 2026-09-17 — 通过,含 glossary 术语链接化
- [x] docs/concepts/containers/images/ — 2026-09-17 — 轻微问题:多处 Note 碎片化
      ([Download Kubernetes]、`:latest`、imagePullPolicy 创建时、pre-pulled、namespace 各链接/代码
      独占段),文字无缺失;imagePullPolicy 三种策略(IfNotPresent/Always/Never)术语与描述完整成对
      (历史 dl 问题未复发);Pre-pulled images 节 NOTE 内嵌的 KubeletEnsureSecretPulledImages
      feature-state 原始 HTML 代码块系上游固有,md 忠实保留;"the`imagePullCredentialsVerificationPolicy`"
      缺空格亦为上游原文;表格、JSON/heredoc 代码块与链接逐项一致;含 glossary 术语链接化(12 处)
- [x] docs/concepts/containers/runtime-class/ — 2026-09-17 — 轻微问题:2 处 Note 碎片化
      ([Scheduling](#scheduling)、[Authorization Overview] 各独占段),文字无缺失;3 个 feature
      state、YAML 代码块、CRI 配置节(标题级 containerd/CRI-O 为 glossary 链接化)、What's next
      逐项一致
- [x] docs/concepts/extend-kubernetes/ — 2026-09-17 — 轻微问题:h1 下 lead 副标题 "Different ways
      to change the behavior of your Kubernetes cluster." 未提取;两张页面级图片(extension-points.png、
      flowchart.svg)已随页复制到文档库且相对路径一致,figcaption 文字保留;其余正文、Key to the
      figure 编号列表与链接逐项一致;含 glossary 术语链接化
- [x] docs/concepts/extend-kubernetes/api-extension/ — 2026-09-17 — 通过
- [x] docs/concepts/extend-kubernetes/api-extension/apiserver-aggregation/ — 2026-09-17 — 通过
- [x] docs/concepts/extend-kubernetes/api-extension/custom-resources/ — 2026-09-17 — 轻微问题:
      "Should I use a ConfigMap"节 Note 碎片化([Secret] 独占段),文字无缺失;4 张表
      (聚合对比 7 行、易用性对比 4 行、高级特性 12 行、公共特性 14 行)逐单元格核对一致;
      `API Aggregation</a>(AA)` 缺空格为上游原文,md 忠实保留;代码样本、feature state、
      What's next 一致;含 glossary 术语链接化(6 处)
- [x] docs/concepts/extend-kubernetes/compute-storage-net/ — 2026-09-17 — 通过,含 glossary 术语链接化
- [x] docs/concepts/extend-kubernetes/compute-storage-net/device-plugins/ — 2026-09-17 — 问题:
      ①"Device plugin implementation"节工作流列表中代码块与警示框结构破坏:HTML 为列表项内嵌
      <pre> 代码块与 Note div,md(63、90、116、118 行)出现 "…interfaces: ```gRPC"、
      "``` > [!NOTE]"、"…kubelet.sock`. > [!NOTE]"、"…include:  > [!NOTE]" 等围栏/NOTE 标记
      与列表文字同行粘连,结构破坏(全部文字仍在);②"Device plugin examples"节第三方 callout
      正文丢失(同 addons 页);③"What's next"节 /blog/2019/04/24/ 链接丢失,md(386 行)为纯文本;
      ④h1 下 lead 副标题未提取;6 个 gRPC/proto 代码块、5 个 feature state、13 个示例列表项
      与页尾 disclaimer 完整;含 glossary 术语链接化
- [x] docs/concepts/extend-kubernetes/compute-storage-net/network-plugins/ — 2026-09-17 — 通过
- [x] docs/concepts/extend-kubernetes/operator/ — 2026-09-17 — 问题:"Writing your own operator"
      节第三方 callout 正文丢失(addons 页同类,md(64-68 行)仅剩 "**Note:**"+content guide+
      More information 三个碎片);其余正文、7 步示例编号列表(含嵌套)、shell 代码块、
      10 个框架链接与页尾 disclaimer 一致
- [x] docs/concepts/overview/ — 2026-09-17 — 轻微问题:h1 下 lead 长副标题("Kubernetes is a
      portable, extensible, open source platform…widely available.")未提取;其余正文
      (10 项特性、7 项非目标、容器演进 3 阶段与 10 项优点)、Container_Evolution.svg、
      What's next 逐项一致
- [x] docs/concepts/overview/components/ — 2026-09-17 — 问题:①Node Components 列表后的
      third-party callout 正文丢失:HTML "This item links to a third party project or product
      that is not part of Kubernetes itself." 缺失,md(53 行)仅剩
      `[More information](#third-party-content-disclaimer)`(kube-state-metrics 页同类);
      ②h1 下 lead 副标题 "An overview of the key components…" 未提取;两组 dl(控制面 5 项、
      节点 3 项)、Addons dl 4 项、图片+figcaption、Flexibility 节一致
- [x] docs/concepts/overview/kubectl/ — 2026-09-17 — 问题:两处 `/releases/version-skew-policy/`
      链接丢失:①"Version compatibility"节 HTML `<a href="/releases/version-skew-policy/">
      version skew policy</a>`,md(41 行)为纯文本 "See the version skew policy for details.";
      ②What's next 末项同样丢失链接,md(50 行)纯文本(非 /docs/ 站内链接被剥离,与
      admission-webhooks 页同类);③h1 下 lead 副标题未提取;其余正文与链接一致
- [x] docs/concepts/overview/kubernetes-api/ — 2026-09-17 — 轻微问题:①"OpenAPI V2/V3"节两张
      请求头表格(HTML <table>,caption 为 display:none)被拉平为不含 `| --- |` 分隔行的竖线
      文本(md 91-95、133-137 行),渲染时不会成为表格,内容完整、形态退化;②h1 下 lead
      副标题未提取; discovery/OpenAPI/Protobuf 各节、2 个 JSON 代码块、Note(alpha)与链接一致
- [x] docs/concepts/overview/working-with-objects/ — 2026-09-17 — 问题:"Required fields"节末
      链接丢失:HTML `<a href="/blog/2025/11/25/configuration-good-practices/">Kubernetes
      Configuration Best Practices</a>`,md(82 行)为纯文本(/blog/ 链接丢失);②h1 下 lead
      副标题未提取;Strict/Warn/Ignore 术语对、deployment.yaml 样本(含文件名链接)、
      字段校验节与 What's next 一致
- [x] docs/concepts/overview/working-with-objects/annotations/ — 2026-09-17 — 通过
- [x] docs/concepts/overview/working-with-objects/common-labels/ — 2026-09-17 — 通过
- [x] docs/concepts/overview/working-with-objects/field-selectors/ — 2026-09-17 — 轻微问题:
      2 处 Note 碎片化(*filters*、`kubectl`/`kubectl get pods --field-selector ""`、
      set-based 操作符 `in`/`notin`/`exists` 各独占段),文字无缺失;10 行支持字段表、
      5 个 shell 代码块与链接逐项一致
- [x] docs/concepts/overview/working-with-objects/finalizers/ — 2026-09-17 — 问题:"What's next"
      节链接丢失:HTML `<a href="/blog/2021/05/14/using-finalizers-to-control-deletion/">
      Using Finalizers to Control Deletion</a>`,md(55 行)为纯文本(/blog/ 链接丢失);
      正文两处重复的 "You can use finalizers to control garbage collection…" 段为上游原文,
      md 忠实保留;3 处 Note 碎片化(内容完整);其余一致
- [x] docs/concepts/overview/working-with-objects/labels/ — 2026-09-17 — 问题:"What's next"
      节链接丢失:HTML `<a href="/blog/2021/06/21/writing-a-controller-for-pod-labels/">
      Writing a Controller for Pod Labels</a>`,md(317 行)为纯文本 "Read a blog on Writing a
      Controller for Pod Labels"(/blog/ 链接丢失);其余正文、equality/set-based 两节、
      全部代码块与链接逐项一致
- [x] docs/concepts/overview/working-with-objects/names/ — 2026-09-17 — 轻微问题:2 处 Note
      碎片化(`RelaxedServiceNameValidation` 各独占段),文字无缺失;4 类命名约束、
      RFC 链接、YAML 样本与 What's next 一致
- [x] docs/concepts/overview/working-with-objects/namespaces/ — 2026-09-17 — 轻微问题:2 处 Note
      碎片化(*not*、`default`、`kube-` 各独占段),文字无缺失;HTML Warning 警示框以
      `> [!CAUTION]` 呈现(两段完整);4 个初始命名空间、代码块、TLD 警示与 feature state 一致
- [x] docs/concepts/overview/working-with-objects/object-management/ — 2026-09-17 — 轻微问题:
      2 处 Warning 警示框以 `> [!CAUTION]` 呈现,其中 imperative `replace` 一处碎片化
      (`replace`/`LoadBalancer`/`externalIPs` 各独占段),文字无缺失;3 行管理技术对比表、
      6 个 sh 代码块、Trade-offs 列表与 What's next 逐项一致
- [x] docs/concepts/overview/working-with-objects/owners-dependents/ — 2026-09-17 — 通过
- [x] docs/concepts/overview/working-with-objects/storage-version/ — 2026-09-17 — 通过
- [x] docs/concepts/policy/ — 2026-09-17 — 问题:"Implementations"节第三方 callout 正文丢失
      (addons 页同类,md(45-49 行)仅剩 "**Note:**"+content guide+More information 三个碎片);
      ②h1 下 lead 副标题 "Manage security and best-practices with policies." 未提取;
      其余 5 个 policy 分类小节、列表与链接一致;含 glossary 术语链接化
- [x] docs/concepts/policy/limit-range/ — 2026-09-17 — 轻微问题:1 处 Note 碎片化
      (`metadata.namespace` 独占段),文字无缺失;3 个 YAML 代码样本(含文件名下载链接)、
      错误输出块与 What's next 6 链接逐项一致
- [x] docs/concepts/cluster-administration/proxies/ — 2026-09-17 — 通过
- [x] docs/concepts/policy/pid-limiting/ — 2026-09-17 — 问题:"What's next"节链接丢失:HTML
      `<a href="/blog/2019/04/15/process-id-limiting-for-stability-improvements-in-kubernetes-1.14/">
      Process ID Limiting for Stability Improvements in Kubernetes 1.14</a>` 被剥离,md(50 行)
      为纯文本(/blog/ 链接丢失,同类问题见 admission-webhooks 等页);②首段 Note 碎片化:`32768`、
      `/proc/sys/kernel/pid_max` 各独占一段(`> ` 空行分隔),文字无缺失;
      其余正文、Feature state(Stable v1.20)、CAUTION、4 个 What's next 条目一致
- [x] docs/concepts/policy/resource-quotas/ — 2026-09-17 — 轻微问题:"How Kubernetes ResourceQuotas
      work"列表第 3、5 项在 HTML 中各含两个 <p>(如 "Users create resources…defined in a ResourceQuota." 与
      "You can apply a scope…applies,"),md(19、21 行)合并为单段,文字无缺失;What's next
      "API reference" 链接 HTML 为 policy-resources/resource-quota-v1,md 按重定向规则(_redirects 566 行)
      正确改写为 core/resource-quota-v1;4 张表格(7+4+3+9 行)、4 处 Feature state
      (Alpha v1.8 / Stable v1.24 / Stable v1.17 / Stable gate:VolumeAttributesClass v1.36 含 More information)、
      3 个代码样本下载链接、shell/输出块、`\>=` 转义逐项一致
- [x] docs/concepts/resource-management/ — 2026-09-17 — 问题:h1 下 lead 副标题
      "How Kubernetes represents, requests, allocates, and constrains the resources that workloads
      consume." 未提取(div.lead,同类问题见多数索引页);正文段、section-index 3 个条目
      (Resource managers / Dynamic Resource Allocation / Pod-level resource managers)链接与
      `---` 分隔线一致
- [x] docs/concepts/resource-management/dynamic-resource-allocation/ — 2026-09-17 — 通过
      (含 glossary 术语链接化);Feature state(Stable gate:DynamicResourceAllocation v1.35)与
      More information 块、Benefits 5 条、Types of DRA users 嵌套列表、Limitations、
      What's next 4 条、section-index 5 个条目逐项一致;HTML 中 "DRA is a Kubernetes feature"
      段的双重 <p><p> 包裹为上游固有,md 正常化为单段
- [x] docs/concepts/resource-management/dynamic-resource-allocation/device-taints/ — 2026-09-17 —
      问题:dry-run 三步列表第 2 项代码围栏与文字粘连(md 113-117 行):HTML li 内为三个独立块
      (<p>Review the message:</p> / <pre><code>3 published devices…</code></pre> /
      <p>Published devices are those…</p>),md 将开围栏直接接在 "Review the message:" 行尾、
      闭围栏后紧连 "```Published devices are those…"(已知问题⑥),文字无缺失;
      ②md(23 行)#admin-access 锚点在 HTML 中无对应 id,上游断锚忠实保留,非提取问题;
      2 处 Feature state(Stable gate:DRADeviceTaints v1.37、Stable gate:DRADeviceTaintRules v1.37)
      及 More information(v1.33 / v1.35 首发)逐字一致;DeviceTaintRule YAML、3 组 kubectl 命令与输出块一致
- [x] docs/concepts/resource-management/dynamic-resource-allocation/dra-api/ — 2026-09-17 —
      勘误:先前记录误判 4 处链接少一级 concepts/(7、41、105、189 行),经解析验证
      `../../../overview/kubernetes-api` 等自本页(5 层深)均正确解析到 docs/concepts/ 下且目标存在,
      现予更正,相关链接实为正确;dl 4 组术语对、6 处 Feature state(gate/默认启用标注)、
      More information 块(斜体样式保留)、8 个代码块逐字一致;
      303 行 "use list-type attributes…" 病句为上游固有
- [x] docs/concepts/resource-management/dynamic-resource-allocation/dra-features/ — 2026-09-17 —
      通过
      (勘误:先前误判 3 处链接少一级 concepts/(258、337、358 行),经解析验证均正确,现予更正);
      355 行 #prioritized-list 锚点在本页 HTML 中即无对应 id(上游断锚,忠实保留,非提取问题);
      ③协议 ol 第 1 项在 HTML 中含 3 个 <p>,md 合并为一条(文字无缺失);
      6 处 Feature state(其中 2 处带 More information)、Immediate/Deferred dl、
      7 个 YAML/JSON 代码块、`{"mig"} ∩ {"vgpu"} = ∅` 特殊字符、"use of a only" 上游笔误逐项一致
- [x] docs/concepts/resource-management/dynamic-resource-allocation/dra-observability/ — 2026-09-17 —
      轻微问题
      (勘误:先前误判 4 处链接少一级 concepts/(15、25 ×2、223 行),经解析验证均正确,现予更正);
      "Check resource pool status" 4 个步骤的开围栏直接接在步骤文字行尾(如 105 行 "…pool name: ```yaml"),
      HTML 中各步骤为 <p> + 独立代码块(已知问题⑥),文字无缺失;
      5 处 Feature state、ResourceClaim YAML(含 consumedCapacity/networkData)、JSON/Go 代码块、
      ../dra-features 与 ../dra-api 锚链接逐项一致
- [x] docs/concepts/resource-management/dynamic-resource-allocation/how-dra-works/ — 2026-09-17 —
      勘误:先前误判 54 行 PreBind phase 链接少一级 concepts/,经解析验证该链接正确,现予更正;
      177 行 #consumable-capacity 锚点在本页
      HTML 中无对应 id(上游断锚,忠实保留,非提取问题);其余正文、bindingConditions 等 dl 3 组、
      2 处 Feature state(DRADeviceBindingConditions Beta v1.36;DRANodeAllocatableResources
      Alpha v1.36 含 More information)、4 个 YAML 块、用户/Kubernetes 两个 workflow 有序列表逐项一致
- [x] docs/concepts/resource-management/pod-level-resource-managers/ — 2026-09-17 — 轻微问题:
      empty-shared-pool YAML 代码样本(115-166 行)在 HTML 中位于 "Important considerations"
      列表第 1 项 <li> 内部,md 中被移到列表外(列表被打断成两段),文字与代码无缺失;
      本页 3 级相对链接(../../../reference、../../../tasks、../../../tutorials)解析深度正确;
      What's next "Node Resource Managers" 链接 md 为 ../resource-managers/,经重定向规则
      (_redirects 504 行 policy/node-resource-managers → resource-management/resource-managers)确认正确;
      Glossary dl 4 组、Feature state(Beta gate:PodLevelResourceManagers v1.37 disabled 含
      More information)、3 个代码样本(含下载链接,逐字节一致)逐项一致
- [x] docs/concepts/resource-management/resource-managers/ — 2026-09-17 — 通过
      (含 glossary 术语链接化);Topology/CPU manager 的 locked-removed 稳定特性说明(斜体+
      feature-gates-removed 链接)、Memory/Device/Pod-level 三处 Feature state、静态策略选项
      dl 6 组、6 个 pod spec YAML 示例及说明、6 个 h6 选项详解小节(顺序一致)逐项一致;
      What's next 单条 "Node Resource Managers" 链接 HTML 指向 policy/node-resource-managers,
      经 _redirects 504 行 301 到本页自身,md 写作 `./` 正确;171 行 "`distribute-cpus-across-numa`policy"
      缺空格为上游固有
- [x] docs/concepts/scheduling-eviction/ — 2026-09-17 — 通过
      (含 glossary 术语链接化);Scheduling 16 条列表、Pod Disruption 2 段与 3 条列表逐项一致;
      "Dynamic Resource Allocation" 链接 HTML 为旧路径 scheduling-eviction/dynamic-resource-allocation,
      md 按重定向规则(_redirects 610 行)正确改写为 ../resource-management/dynamic-resource-allocation/
- [x] docs/concepts/scheduling-eviction/api-eviction/ — 2026-09-17 — 轻微问题:2 个 tab 内的
      Note 碎片化(policy/v1 面板中 `policy/v1`、`policy/v1beta1` 各独占段,21-27 行;
      policy/v1beta1 面板中 `policy/v1` 独占段,45 行;已知模式⑤),文字无缺失;
      "DELETE operation" 链接 HTML 为 workload-resources/pod-v1,md 按 _redirects 584 行正确改写为
      core/pod-v1 且 #delete-delete-a-pod 锚点保留;tabs 结构(2 面板)、2 个 JSON 块、curl 示例、
      6 步 ol、响应码列表、What's next 3 条逐项一致
- [x] docs/concepts/scheduling-eviction/assign-pod-node/ — 2026-09-17 — 轻微问题:全页约 8 处
      Note/Caution 碎片化(如 `IgnoredDuringExecution`、`kubernetes.io/os=linux`、
      `topologyKey`、`nodeAffinity`、[node affinity](#node-affinity)、`Gt`/`Lt`/`podAffinity`、
      `matchLabelKeys`/`labelSelector` 等行内代码或链接独占段,已知模式⑤),文字无缺失;
      3 个代码样本下载链接、8 个 YAML 块、集群布局表与 2 张 Operators 表、
      Scheduling Behavior 嵌套列表、7 处 Feature state(Namespace Selector Stable v1.24、
      matchLabelKeys/mismatchLabelKeys Stable gate:MatchLabelKeysInPodAffinity v1.33 含
      More information、nominatedNodeName Beta gate:NominatedNodeNameForExpectation v1.35、
      Pod topology labels Beta gate:PodTopologyLabelsAdmission v1.35 等)、全部链接逐项一致;
      628 行 "for it's zone" 笔误为上游固有
- [x] docs/concepts/scheduling-eviction/gang-scheduling/ — 2026-09-17 — 问题:①"How it works"
      有序列表第 1 项结构重排:HTML 内为 段落("…until:") → 子列表 2 条 → 尾段
      ("The PodGroup does not enter the active scheduling queue until both conditions are met."),
      md(34 行)将尾段合并进首段("until:  The PodGroup…"处可见双空格),子列表随之后移,文字无缺失;
      ②首段 Note 碎片化(`minCount` 三处独占段,13-26 行,已知模式⑤);
      2 处 Feature state(Beta gate:GenericWorkload v1.37、Alpha gate:CompositePodGroup v1.37,
      均含 More information)、层级 quorum 嵌套列表、全部链接(含 2 处 API group 术语链接、
      compositepodgroup-api/lifecycle)与 What's next 4 条逐项一致
- [x] docs/concepts/scheduling-eviction/kube-scheduler/ — 2026-09-17 — 通过
      (含 glossary 术语链接化);正文各段、2 步 ol、2 种配置方式 ol、What's next 8 条
      (含存储 3 条子列表)与全部链接逐项一致
- [x] docs/concepts/scheduling-eviction/node-declared-features/ — 2026-09-17 — 问题:"How it
      works" 有序列表第 2 项结构重排(同 gang-scheduling 页):HTML 内为 段落("…This plugin:")
      → 子列表 2 条 → 尾段("Custom schedulers can also use…"),md(18 行)将尾段合并进首段
      ("This plugin:  Custom schedulers…"处可见双空格),子列表随之后移,文字无缺失;
      Feature state(Stable gate:NodeDeclaredFeatures v1.37 含 More information)、2 个 YAML 块、
      全部链接与 What's next 2 条逐项一致
- [x] docs/concepts/scheduling-eviction/node-pressure-eviction/ — 2026-09-17 — 通过(附注):
      ①Memory signals 与 Filesystem signals 两节 Note 中,上游 HTML 本身将 feature-state-notice
      (HugepageAwareEviction v1.37 / KubeletSeparateDiskGC v1.31)以转义文本嵌在 <pre><code> 内
      (上游固有),md(56-70、86-100 行)以围栏代码块忠实保留,非提取问题;
      ②表格内减号写作 `\-`(如 `memory.available` 公式、oom_score_adj 表 -997),渲染等价,轻微;
      ③3 处 Note 碎片化(`DiskPressure`、`containerfs.available`/`nodefs`/`imagefs`、
      `oom_score_adj`/`-997`/`system-node-critical` 独占段,已知模式⑤),文字无缺失;
      驱逐信号表(8 行)、节点状况表(3 行)、oom_score_adj 表(3 行)、KubeletConfiguration YAML、
      hugepages/GetPerformanceInfo 等内外链接逐项一致
- [x] docs/concepts/scheduling-eviction/pod-overhead/ — 2026-09-17 — 轻微问题:1 处 Note 碎片化
      (`limits`、`requests` 独占段,61-74 行,已知模式⑤),文字无缺失;Feature state(Stable v1.24
      无 gate)、2 个 RuntimeClass/Pod YAML、6 个 bash 块与 5 个输出块(含上游固有的 cat 前导空格)
      及全部链接逐项一致
- [x] docs/concepts/scheduling-eviction/pod-priority-preemption/ — 2026-09-17 — 轻微问题:
      1 处 NOTE 碎片化(`system-cluster-critical`、`system-node-critical`、
      "ensure that critical components…"链接等独占段,27-48 行,已知模式⑤),文字无缺失;
      17 行 "with[`priorityClassName`]" 缺空格与 HTML 一致(上游固有);
      6 处 Feature state(Stable v1.14、Non-preempting Stable v1.24、PodGroups/CompositePodGroups/
      InPlacePodVerticalScalingSchedulerPreemption 等 gate 标注均含 More information)、
      4 个 YAML 块、in-place resize 6 条 bullet 与约束 3 条、全部链接与 What's next 4 条逐项一致
- [x] docs/concepts/scheduling-eviction/pod-scheduling-readiness/ — 2026-09-17 — 通过;
      Feature state(Stable v1.30)、diagram-large 图片(podSchedulingGates.svg,HTML 路径
      /docs/images/…,md 三级相对路径正确,资产已复制到 documents/docs/images/)、
      mermaid.live 外链与 figcaption、2 个代码样本(含下载链接)、bash 块与输出、
      Mutable directives 4 条 ol、KEP 链接逐项一致
- [x] docs/concepts/scheduling-eviction/podgroup-scheduling/ — 2026-09-17 — 通过;
      4 处 Feature state(Beta gate:GenericWorkload v1.37、Alpha gate:TopologyAwareWorkloadScheduling
      v1.36、Alpha gate:CompositePodGroup v1.37 ×2,均含 More information)、
      调度周期 5 步 ol 与嵌套 bullet、层级调度 5 步 ol、conditions 两节、全部链接
      (含 ../gang-scheduling/#Hierarchical-quorum)与 What's next 5 条逐项一致;
      29 行 "extenion"、"`PodGroupPostFilter`extension" 缺空格均为上游固有笔误
- [x] docs/concepts/scheduling-eviction/resource-bin-packing/ — 2026-09-17 — 轻微问题:1 处
      Note 碎片化("article about Topology-aware Scheduling" 链接独占段,3-8 行,已知模式⑤),
      文字无缺失;2 个 KubeSchedulerConfiguration YAML、4 个 shape/resources YAML、
      6 个 node 评分纯文本块、FunctionShapePoint 段、全部链接与 What's next 2 条逐项一致
- [x] docs/concepts/scheduling-eviction/scheduler-perf-tuning/ — 2026-09-17 — 通过;
      2 处 Feature state(Beta v1.14 无 gate;Beta gate:OpportunisticBatching v1.35)、
      bash 块、示例 YAML、zone 顺序 2 个代码块、全部链接与 What's next 逐项一致
- [x] docs/concepts/scheduling-eviction/scheduling-framework/ — 2026-09-17 — 轻微问题:2 处
      警示框碎片化(CAUTION 中 `Unreserve` 独占段,124-129 行;NOTE 中 FrameworkHandle 与
      PreBind 链接独占段,139-148 行,已知模式⑤),文字无缺失;架构图(HTML /images/docs/…,
      md 四级相对路径正确,资产已复制到 documents/images/docs/)、figcaption 内 h4、
      Permit 3 项列表(**approve** 等 <br> 结构保留为同段)、3 处 Feature state、
      3 个 Go 代码块、全部扩展点小节与链接逐项一致
- [x] docs/concepts/scheduling-eviction/taint-and-toleration/ — 2026-09-17 — 问题:①效果说明
      dl 中 `NoExecute` 项的 <dd> 引导句 "This affects pods that are already running on the node
      as follows:" 丢失(md 84-88 行仅剩 3 条 bullet,与已知 dl 描述丢失同类问题);
      ②多处 Note 碎片化(`key`、`effect`、`Gt`、`Lt`、`servicelevel…=high:NoSchedule`、
      `--controllers=-taint-eviction-controller` 独占段,已知模式⑤),文字无缺失;
      12 个代码块、2 个代码样本下载链接、dl 3 组(NoSchedule/PreferNoSchedule 完整)、
      2 处 Feature state(Alpha gate:TaintTolerationComparisonOperators v1.35 含 More information、
      Stable v1.18)、内置 taint 8 条、Warning 块([!CAUTION])含 4 条列表;kubectl taint 链接
      (HTML generated/kubectl/kubectl-commands#taint)按 _redirects 212 行正确改写为
      reference/kubectl/#taint;2 处旧 DRA 路径按 610 行改写并保留 #device-taints-and-tolerations 锚点
- [x] docs/concepts/scheduling-eviction/topology-aware-scheduling/ — 2026-09-17 — 通过;
      2 处 Feature state(Alpha gate:TopologyAwareWorkloadScheduling v1.36、Alpha gate:
      CompositePodGroup v1.37,均含 More information)、TAS 插件 3 条列表、KubeSchedulerConfiguration
      CompositePodGroup v1.37,均含 More information)、TAS 插件 3 条列表、KubeSchedulerConfiguration
      YAML、候选放置生成 2 条列表、评分 2 条列表、全部链接与 What's next 3 条逐项一致
- [x] docs/concepts/scheduling-eviction/topology-spread-constraints/ — 2026-09-17 — 问题:
      ①What's next 博客链接丢失:HTML `<a href="/blog/2020/05/introducing-podtopologyspread/">
      Introducing PodTopologySpread</a>` 被剥离,md(581 行)为纯文本(问题③,/blog/ 链接丢失);
      ②多处警示框/代码块与列表项粘连(已知问题⑥):minDomains 项内嵌 NOTE 且与正文同行
      (91 行 "…node selector.  > [!NOTE]");matchLabelKeys 项内嵌 CAUTION(112 行)且 YAML 块
      开围栏粘连(127 行 "…single Deployment. ```yaml");nodeAffinityPolicy(142 行)与
      nodeTaintsPolicy(156 行)两项内嵌 NOTE、"Options are:" 的 Honor/Ignore 子列表被后移到
      NOTE 之后(154-155、168-169 行);ghost pods 项(575 行 "This means:  Ensure…")合并,
      子列表后移;③首段 NOTE 碎片化(topologySpreadConstraint/topologyKey/whenUnsatisfiable
      等独占段,43-80 行,已知模式⑤);文字均无缺失;
      其余一致:7 个 mermaid 图块、4 个代码样本(含下载链接)、Feature state(Stable v1.24)、
      2 个 PodTopologySpread 配置 YAML、全部链接(含 ../ 索引、#example-conflicting-topologyspreadconstraints、
      Descheduler、Issue 80921、KEP)与 2 条 API reference 链接逐项一致
- [x] docs/concepts/scheduling-eviction/workload-aware-preemption/ — 2026-09-17 — 问题(上游根因):
      26 行 API group 链接损坏:上游 HTML 源本身即为畸形的 "[<code>scheduling.k8s.io/v1beta1</code>]"
      字面括号紧邻 <a> 链接(无空格),md 转写为 "[`scheduling.k8s.io/v1beta1`][API group](…)",
      按 CommonMark 渲染为引用式链接残片,URL 变为可见文本、"API group" 链接失效;
      ②2 处 NOTE 碎片化(`WorkloadAwarePreemption`/`GenericWorkload`;**not**/`priority`/
      `disruptionMode` 独占段,11-20、41-54 行,已知模式⑤);其余一致:2 处 Feature state
      (均含 More information)、How it works 4 步 ol、Reprieval 2 组列表、CompositePodGroups
      2 步列表、全部链接与 What's next 3 条逐项一致
- [x] docs/concepts/security/ — 2026-09-17 — 问题:"Cloud provider security" 节第三方 callout
      正文丢失:HTML 正文 "Items on this page refer to vendors external to Kubernetes.…To add a
      vendor, product or project to this list, read the content guide before submitting a change."
      整段缺失,md(41-45 行)仅剩 "**Note:**"+content guide+More information 三个碎片
      (问题②,与 policy/addons 页同类);其余一致:5 个安全机制小节、9 行 IaaS 提供商表格、
      Policies 节、What's next 4 组列表、section-index 17 条、底部 disclaimer 逐项一致
- [x] docs/concepts/security/api-server-bypass-risks/ — 2026-09-17 — 通过;
      Static Pods/kubelet API/etcd API/Container runtime socket 四节及各自 Mitigations 列表、
      **get** 等粗体、43 行句中句点位于链接后为上游固有;全部链接(glossary mirror-pod、
      static-pod 锚、kubeadm issue、volumes#hostpath 等)逐项一致
- [x] docs/concepts/security/application-security-checklist/ — 2026-09-17 — 问题:①"Runtime
      classes" 节 /blog/ 链接丢失:HTML `<a href="/blog/2023/07/06/confidential-kubernetes/">
      confidential virtual machines</a>` 被剥离,md(95 行)为纯文本(问题③);
      ②同节第三方 callout 正文丢失("This section links to third party projects…before
      submitting a change.",md 85-89 行仅剩 "**Note:**"+content guide+More information 碎片,
      问题②);③CAUTION 碎片化(**not** 独占段,11-16 行,模式⑤);
      其余一致:各 checklist 列表与粗体动词、全部链接与底部 disclaimer 逐项一致
- [x] docs/concepts/security/cloud-native-security/ — 2026-09-17 — 通过;
      4 个 lifecycle phase 小节及 Runtime protection 3 小节、各编号/项目列表、
      "clusterf**k" 字面星号为上游固有且 md 渲染等价、全部内外链接(含 2 处 container runtime
      术语链接至 setup/production-environment/container-runtimes、CNCF 白皮书、YouTube 等)
      与 What's next 2 小节列表逐项一致
- [x] docs/concepts/security/controlling-access/ — 2026-09-17 — 轻微问题:35 行 "step **2**in"
      丢失空格(HTML 为 "**2** in");其余通过:access-control-overview.svg 图片(HTML
      /images/docs/admin/…,md 四级相对路径正确,资产已复制)、Transport/Authentication/
      Authorization/Admission control/Auditing 各节、2 个 ABAC/SubjectAccessReview JSON 块、
      What's next 嵌套列表(15 项)与全部链接逐项一致
- [x] docs/concepts/security/hardening-guide/authentication-mechanisms/ — 2026-09-17 — 通过;
      9 个认证机制小节及各自列表、全部链接(均为 access-authn-authz 内链含锚点)与
      What's next 4 条逐项一致
- [x] docs/concepts/security/hardening-guide/dynamic-resource-allocation/ — 2026-09-17 — 通过;
      Feature state(Beta gate:DRAResourceClaimGranularStatusAuthorization v1.36)、2 个合成子资源
      列表、node-aware verbs 列表、3 个 RBAC YAML、"updates,In" 缺空格与 "per-driver to drivers
      from tampering" 病句均为上游固有;全部链接与 What's next 3 条逐项一致
- [x] docs/concepts/security/hardening-guide/scheduler/ — 2026-09-17 — 通过;
      认证/网络/TLS 命令行选项 3 组列表、扩展点考量 3 条、"which are provide" 上游笔误、
      1 个 KubeSchedulerConfiguration YAML、全部链接与逐项一致
- [x] docs/concepts/security/linux-kernel-security-constraints/ — 2026-09-17 — 轻微问题:2 处
      NOTE 碎片化(`allowPrivilegeEscalation`/`false` 独占段,32-41 行;CVE-2019-5736 链接独占段,
      94-99 行,已知模式⑤),文字无缺失;seccomp/AppArmor/SELinux 各节、特性对照表(3 行 4 列,
      含 CVE-2022-0185 链接)、特权容器覆盖 3 条列表、全部链接与 What's next 4 条逐项一致
- [x] docs/concepts/security/linux-security/ — 2026-09-17 — 通过;短页,secret/emptyDir 卷、
      tmpfs 与 noswap 说明、swap-memory-management#memory-backed-volumes 链接逐项一致
- [x] docs/concepts/security/multi-tenancy/ — 2026-09-17 — 轻微问题:1 处 CAUTION 碎片化
      (CNI plugin 链接独占段,106-111 行,已知模式⑤;HTML 为 alert-danger Warning,
      按约定转写为 [!CAUTION]);其余通过:multi-tenancy.png 图片(资产已复制)、
      figcaption 内 h4、Use cases/Terminology/控制面与数据面隔离各节、
      全部链接(RBAC 锚、storage-classes#reclaim-policy、network-plugins#cni、
      bandwidth 插件、CoreDNS policy、cluster-api-provider-nested 等)与锚点逐项一致
- [x] docs/concepts/security/pod-security-admission/ — 2026-09-17 — 通过(附注):1 处 CAUTION
      碎片化(workload resource 链接与 `system:serviceaccount:kube-system:replicaset-controller`
      独占段,62-71 行,已知模式⑤),文字无缺失;Feature state(Stable v1.25)、
      "Built-in…"h3 与版本说明、模式表(3 行)、labels YAML、豁免 3 条列表、豁免字段 3 条列表、
      Metrics 3 条、全部链接与 What's next(含 migrate-from-psp 段落)逐项一致
- [x] docs/concepts/security/pod-security-policy/ — 2026-09-17 — 问题:2 处 /blog/ 链接丢失
      (问题③):①警示框内 "deprecated" 一词的链接(HTML 指向
      /blog/2021/04/08/kubernetes-1-21-release-announcement/#podsecuritypolicy-deprecation)被剥离,
      md(5 行)为纯文本;②"PodSecurityPolicy Deprecation: Past, Present, and Future" 链接
      (HTML 指向 /blog/2021/04/06/podsecuritypolicy-deprecation-past-present-and-future/)被剥离,
      md(15 行)为纯文本;附注:HTML 为 alert-warning(标题 "Removed feature"),md 按类转写为
      `> [!WARNING]`(全库仅 2 处),标题按 Note:/Caution: 同样约定省略;
      其余(Pod Security Admission 替代列表、migrate-from-psp 链接、版本说明)一致
- [x] docs/concepts/security/pod-security-standards/ — 2026-09-17 — 问题:①Alternatives 节
      第三方 callout 正文丢失(md 95-99 行仅剩碎片,问题②);②Restricted 策略表在 HTML 中为
      td+strong 伪表头的真实表格,md(72-79 行)渲染为无 `| --- |` 分隔行的竖线伪表格
      (已知问题⑪),单元格内 dl/多段拉平为单行但 **Restricted Fields**/**Allowed Values**
      标签计数 20/20、21/21 与 HTML 完全一致,HostProcess 单元格内 feature-state-notice
      拉平为文字;③2 处 NOTE 碎片化(通配符说明,23-36、57-70 行,模式⑤);
      其余一致:Profile 表(3 行)、Baseline 正常表格(12 行)、3 个 podsecurity YAML 链接、
      Pod OS field、FAQ 3 节、全部链接与底部 disclaimer 逐项一致
- [x] docs/concepts/security/rbac-good-practices/ — 2026-09-17 — 通过;
      General good practice 4 小节、权限提升风险 9 小节、DoS 风险小节、粗体动词、
      全部链接(含 #get-nodes-proxy-warning、#object-count-quota、k8s issue 107325)与
      What's next 逐项一致
- [x] docs/concepts/security/secrets-good-practices/ — 2026-09-17 — 问题:"Configure access to
      external Secrets" 节第三方 callout 正文丢失("This section links to third party projects…",
      md 50-54 行仅剩 "**Note:**"+content guide+More information 碎片,问题②);
      ②2 处 CAUTION 碎片化(`list`、*not* 独占段,26-31、80-85 行,模式⑤);
      21 行 "Role-based Access Control [(RBAC)]" 双链接为上游固有(glossary 术语 + 独立 (RBAC) 链接);
      其余一致:管理员/开发者两大节各小节、CSI Driver 外链、swap 锚链接、底部 disclaimer 逐项一致
- [x] docs/concepts/security/security-checklist/ — 2026-09-17 — 问题:3 处 /blog/ 链接丢失
      (问题③):①67 行 "Kubernetes 1.23: Pod Security Graduates to Beta" 为纯文本
      (HTML /blog/2021/12/09/pod-security-admission-beta/);②167 行 "should be properly
      secured" 为纯文本(HTML /blog/2022/01/19/secure-your-admission-controllers-and-webhooks/);
      ③231 行 "A Closer Look at NSA/CISA Kubernetes Hardening Guidance" 为纯文本
      (HTML /blog/2021/10/05/nsa-cisa-kubernetes-hardening-guidance/#building-secure-container-images);
      ②多处 NOTE/CAUTION 碎片化(**not**、"some Linux distributions" 两个发行版链接等独占段,
      模式⑤);其余一致:8 个 checklist 小节及列表、准入控制器 3 组 dl(11 项含链接与锚点)、
      全部其余链接与 What's next 逐项一致
- [x] docs/concepts/security/service-accounts/ — 2026-09-17 — 问题(已知⑧):75 行 TokenRequest API
      链接丢失重定向目标中的锚点:HTML 为 authentication-resources/token-request-v1/,重定向规则
      (_redirects 593 行)目标为 storage/csi-driver-v1/#TokenRequest,md 仅写到
      `../../../reference/kubernetes-api/storage/csi-driver-v1/`,#TokenRequest 被丢弃;
      ②NOTE 碎片化(`and`(上游即 <code>and</code>)、`kubernetes.io/enforce-mountable-secrets`
      独占段,87-99 行,模式⑤);其余一致:SA/用户对照表(3 行)、用例与使用方法各节、
      3 种凭据方式列表、2 处 Feature state(Beta gate:ServiceAccountNodeAudienceRestriction v1.33、
      Deprecated v1.32)、注解 YAML、JWT 校验 5 步 ol、Alternatives(含 SPIFFE/Istio 外链与
      More information)与全部链接逐项一致
- [x] docs/concepts/security/windows-security/ — 2026-09-17 — 通过;短页,Secret 明文说明与
      BitLocker、ContainerUser/ContainerAdministrator NOTE 列表、RunAsUsername/RunAsUsername 对照、
      Pod 级隔离机制不支持说明及全部链接逐项一致(24 行 GMSA 句末无句点为上游固有)
- [x] docs/concepts/services-networking/ — 2026-09-17 — 通过
      (含 glossary 术语链接化);网络模型嵌套列表、外部组件 5 条列表、What's next 5 条
      (`\-` 转义渲染等价)与全部链接逐项一致
- [x] docs/concepts/services-networking/cluster-ip-allocation/ — 2026-09-17 — 问题(已知⑩):
      3 处上标丢失:HTML "2<sup>8</sup>"/"2<sup>12</sup>"/"2<sup>16</sup>" 在 md(67、80、93 行)
      变为 "28"/"212"/"216"(与 manage-resources-containers 页同类);
      其余一致:动态/静态分配说明、kube-dns YAML、3 个 mermaid pie 图块、
      全部链接与 What's next 3 条逐项一致
- [x] docs/concepts/services-networking/dns-pod-service/ — 2026-09-17 — 轻微问题:①DNS Policy
      "ClusterFirstWithHostNet" 项内嵌 NOTE 且与正文同行(186 行 "…"Default"` policy. > [!NOTE]",
      已知问题⑥);②2 处 NOTE 碎片化(`hostname`/`subdomain`/`publishNotReadyAddresses=True`;
      `dnsPolicy`,144-165、194-199 行,模式⑤),文字无缺失;
      其余一致:Namespaces/DNS Records 前置 h3、A/AAAA/SRV 记录、hostname/subdomain、
      setHostnameAsFQDN(Stable v1.22)、DNS Policy 4 条、DNS Config(Stable v1.14)与
      custom-dns.yaml 代码样本、搜索域限制(Stable v1.28)、Windows 解析 3 条、
      全部链接与 What's next 逐项一致
- [x] docs/concepts/services-networking/dual-stack/ — 2026-09-17 — 轻微问题:①已知问题⑥:
      "Dual-stack defaults on existing Services" 两个步骤的验证说明与 ```shell 开围栏粘连
      (161、204 行行首空格 " ```shell",HTML 中各为 li 内独立 <p> 与代码块);"Switching
      Services between single-stack and dual-stack" 第 1 步 Before/After 两个围栏粘连
      (233 行 "Before: ```yaml"、237 行 "After: ```yaml");Prerequisites 第 1 项两段合并
      (21 行 "Kubernetes 1.20 or later For information…"),文字无缺失;
      ②4 处 NOTE 碎片化(`.spec.ipFamilies`、`LoadBalancer`、CNI 链接、**do not** 独占段,模式⑤);
      其余一致:Feature state(Stable v1.23)、Supported/Prerequisites、双栈 Service 配置场景
      3 个编号示例(含 4 个代码样本下载链接)、ipFamilies 取值列表、Egress/Windows 支持小节、
      全部链接与 What's next 2 条逐项一致
- [x] docs/concepts/services-networking/endpoint-slices/ — 2026-09-17 — 问题:①overview 段
      正文丢失:HTML "EndpointSlices track the IP addresses of backend endpoints. EndpointSlices
      are normally associated with a [Service] and the backend endpoints typically represent
      [Pods]." 在 md(5-7 行)仅剩 `[Service](../service/)` 与 `[Pods](../../workloads/pods/)`
      两个裸链接独占段,周边文字全部丢失;②lead 副标题缺失("The EndpointSlice API is the
      mechanism that Kubernetes uses to let your Service scale…",问题④);
      其余一致:4 处 Feature state(Stable v1.21/v1.26×2/Deprecated v1.33)、EndpointSlice YAML、
      Address types/Conditions/Topology/Management/Ownership/Distribution/Duplicate endpoints/
      Mirroring 各节、What's next 3 条(2 个 API reference 链接按 _redirects 569/570 行正确改写
      为 discovery/endpoint-slice-v1 与 core/endpoints-v1)
- [x] docs/concepts/services-networking/gateway/ — 2026-09-17 — 通过;Design principles 嵌套列表、
      Resource model 4 个 API kind、GatewayClass/Gateway/HTTPRoute/GRPCRoute 各节及 5 个 YAML、
      2 张图片(gateway-kind-relationships.svg、gateway-request-flow.svg,路径正确且资产已复制)、
      Request flow 6 步 ol、164 行 "specified,only" 缺空格与 "method\`" 游离反引号均为上游固有;
      全部链接与 What's next 逐项一致
- [x] docs/concepts/services-networking/ingress-controllers/ — 2026-09-17 — 问题:①lead 被剥离为
      碎片:HTML lead 正文 "In order for an Ingress to work in your cluster, there must be an
      ingress controller running. You need to select at least one ingress controller and make sure
      it is set up in your cluster. This page lists common ingress controllers that you can
      deploy." 丢失,md(3-5 行)仅剩 `[Ingress](../ingress/)` 链接与 `*ingress controller*`
      斜体两个碎片;②"Third party ingress controllers" 第三方 callout 正文丢失(21-25 行碎片,
      问题②);其余一致:Gateway 优先 NOTE(含 2 条列表)、官方 AWS/GCE controller、
      28 个第三方 ingress controller 列表、Using multiple Ingress controllers 节;
      "stability guarantees" 链接经 _redirects 210/611 行环路核对,真实页为
      reference/deprecation-policy,md 目标正确
- [x] docs/concepts/services-networking/ingress/ — 2026-09-17 — 轻微问题:①Path types 的
      `Prefix` 项内嵌 NOTE 且与正文同行(156 行 "…of the request path. > [!NOTE]",已知问题⑥),
      `/foo/bar` 等路径独占段(模式⑤);②Namespaced 面板中 feature-state-notice 原始 HTML 为
      上游转义文本(md 298-305 以代码块忠实保留,非提取问题);③4 处 NOTE 碎片化
      (`<pending>`、Ingress controller/Service 链接、hosts/tls/host/rules,模式⑤);
      其余一致:Feature state(Stable v1.19)、Terminology 5 条、3 张 mermaid 图片
      (ingress.svg/ingressFanOut.svg/ingressNameBased.svg,资产已复制)、7 个代码样本下载链接、
      Path types 表(19 行)、Hostname wildcards 表(3 行)、Cluster/Namespaced tabs、TLS、
      Load balancing、Updating 示例输出块、全部链接与 What's next 逐项一致
- [x] docs/concepts/services-networking/network-policies/ — 2026-09-17 — 轻微问题:①已知问题⑥:
      "hostNetwork pods" 节两个步骤的 ```yaml 开围栏粘连(421 行 "…`spec.podSelector`. ```yaml"、
      429 行 "…`egress` rule. ```yaml",HTML 中各为 li 内独立段落与代码块),文字无缺失;
      ②4 处 NOTE 碎片化(CNI、CNI/`endPort`/network plugin/`port`;`namespaceSelector`/
      `matchLabels`/`matchExpressions`,269-274、319-340、373-386 行,模式⑤);
      其余一致:Feature state(Stable v1.25)、两种隔离说明、NetworkPolicy 资源详解、
      4 种 selector 说明、默认策略 5 小节(7 个代码样本下载链接)、端口范围/多命名空间/
      Pod lifecycle/hostNetwork/无法实现清单 10 条,全部链接与 What's next 逐项一致
- [x] docs/concepts/services-networking/service-traffic-policy/ — 2026-09-17 — 问题:lead 被剥离
      为碎片:HTML lead("If two Pods in your cluster want to communicate…and both Pods are
      actually running on the same node, use Service Internal Traffic Policy to keep network
      traffic within that node. Avoiding a round trip via the cluster network can help with
      reliability, performance…or cost.")丢失,md(3 行)仅剩 `*Service Internal Traffic Policy*`
      斜体碎片;其余一致:Feature state(Stable v1.26)、internalTrafficPolicy 说明与 NOTE、
      Service YAML、How it works、What's next 3 条逐项一致
- [x] docs/concepts/services-networking/service/ — 2026-09-17 — 问题:①lead 副标题缺失
      ("Expose an application running in your cluster behind a single outward-facing endpoint,
      even when the workload is split across multiple backends.",问题④);②已知问题③:
      369 行 "See Avoid Collisions Assigning Ports to NodePort Services for more details…"
      博客链接丢失(HTML /blog/2023/05/11/nodeport-dynamic-and-static-allocation/ 被剥离为纯文本);
      其余一致:42 个标题一一对应;7 处 Feature state(Stable v1.21、Deprecated v1.33、
      Stable v1.20、Alpha gate:KubeProxyNFTablesLocalhostNodePorts v1.37、Stable v1.24×2、
      Deprecated v1.36);simple-service.yaml 代码样本;Client/Server 环境变量、
      headless/ExternalName/ExternalIPs/Traffic Policies/Traffic Distribution/Session affinity
      各节;全部链接(virtual-ips 各锚点、API reference 按 _redirects 573 等正确改写、
      RFC 6455/9113、docker links 等)逐项一致
- [x] docs/concepts/services-networking/topology-aware-routing/ — 2026-09-17 — 问题:lead 被剥离
      为碎片:HTML lead("Topology Aware Routing provides a mechanism to help keep network traffic
      within the zone where it originated. Preferring same-zone traffic between Pods in your
      cluster can help with reliability, performance (network latency and throughput), or cost.")
      丢失,md(3 行)仅剩 `*Topology Aware Routing*` 斜体碎片(正文 14 行措辞不同,非同句);
      其余一致:Feature state(Beta v1.23)、2 处 NOTE 碎片化(*Topology Aware Hints*、
      `service.kubernetes.io/topology-aware-hints`,模式⑤)、EndpointSlice hints YAML、
      Safeguards 5 条、Constraints 6 条、Custom heuristics、全部链接与 What's next 2 条逐项一致
- [x] docs/concepts/services-networking/windows-networking/ — 2026-09-17 — 通过;
      HNS/vSwitch 说明、网络模式表(5 行,含大量 CNI 外链)、Flannel 代理说明、9 种流量流向列表、
      IPAM 3 条、DSR(Stable gate:WinDSR v1.34 含 More information)、服务负载均衡特性表(5 行,
      #dsr 锚点有效)、Limitations 两组列表、32 处链接与逐项一致
- [x] docs/concepts/storage/ — 2026-09-17 — 通过;纯 section-index 页,17 个条目
      (Volumes 至 Windows Storage)链接与顺序逐项一致
- [x] docs/concepts/storage/dynamic-provisioning/ — 2026-09-17 — 通过;Background/Enabling/
      Using/Defaulting/Topology Awareness 各节、4 个 YAML 块、全部链接(DefaultStorageClass、
      storageclass 注解、multiple-zones、storage-classes#volume-binding-mode)逐项一致
- [x] docs/concepts/storage/ephemeral-storage/ — 2026-09-17 — 附注:①"Filesystem project
      quota" 面板中 feature-state(LocalStorageCapacityIsolationFSQuotaMonitoring Beta v1.31)
      的转义 HTML 在上游即被拆为两个 <pre><code> 块、中间夹真实段落("See Enable Or Disable
      Feature Gates…"),md(180-203 行)忠实保留,非提取问题;②已知问题⑥:220 行
      "…not mounted. ```bash" 围栏粘连;③4 处 NOTE 碎片化(`tmpfs`、`/var/lib/kubelet`/
      `/var/log` 等路径、open file descriptors 段,模式⑤),文字无缺失;
      其余一致:3 种存储配置 tabs、requests/limits 说明与示例、Pod YAML、调度与消耗管理、
      CAUTION(节点 taint)、What's next 逐项一致
- [x] docs/concepts/storage/ephemeral-volumes/ — 2026-09-17 — 轻微问题:1 处 NOTE 碎片化
      ("Drivers list" 链接独占段,35-40 行,模式⑤),文字无缺失;
      其余一致:2 处 Feature state(Stable v1.25、Stable v1.23)、类型列表、CSI 驱动限制、
      Generic 卷 YAML、生命周期/命名冲突/安全各节、23 处链接(17 相对+6 外部)与
      What's next 3 小节逐项一致
- [x] docs/concepts/storage/persistent-volumes/ — 2026-09-17 — 轻微问题:①NOTE("volume access
      modes do **not** enforce…"段,451-456 行)后随的 "**Important!** A volume can only be
      mounted…" 在 HTML 中为同一警示框内第二段,md(458 行)转写为独立普通引用块,文字无缺失;
      ②多处 NOTE/CAUTION 碎片化(Recycle、`.spec`、**not**、local 链接、`Filesystem`、
      `selector`、`deletionTimestamp` 等,模式⑤);其余一致:12 处 Feature state/locked 说明
      (10 [FEATURE STATE] + 2 斜体 locked:1.33 finalizer、1.31 phase transition)、
      访问模式插件表(11 行)与卷绑定矩阵表(9 行)、Panel 两个恢复方式、
      14 个 YAML/shell 块、PV 类型三组列表、全部链接与 What's next(含 2 个 API reference)逐项一致
- [x] docs/concepts/storage/projected-volumes/ — 2026-09-17 — 轻微问题:2 处 NOTE 碎片化
      (`subPath` 链接独占段,138-143 行;`credentialBundlePath` 独占段,217-222 行,模式⑤),
      文字无缺失;其余一致:6 种可投影源列表、4 个代码样本下载链接与 YAML、
      2 处 Feature state(Stable gate:ClusterTrustBundleProjection v1.37、
      Stable gate:PodCertificateRequest v1.37,均含斜体 More information)、
      全部链接逐项一致
- [x] docs/concepts/storage/storage-capacity/ — 2026-09-17 — 通过;Feature state(Stable v1.24)、
      API 两个扩展说明(CSIStorageCapacity 与 CSIDriverSpec.StorageCapacity 链接均按
      _redirects 545/547 行正确改写且 #CSIDriverSpec 锚点保留)、Scheduling/Rescheduling/
      Limitations 各节与 What's next KEP 链接逐项一致
- [x] docs/concepts/storage/storage-classes/ — 2026-09-17 — 附注+轻微问题:①上游固有:
      "Ceph RBD (deprecated)" 节 NOTE 内含转义的 feature-state-notice(Deprecated v1.28),
      md(316-323 行)以代码块忠实保留,非提取问题;②已知问题⑥:vCP Provisioner 有序列表
      第 1、2 步(288 行 "…disk format. ```yaml"、298 行 "…datastore. ```yaml")与 Ceph RBD
      `userSecretName` 项(355 行 "…in this way: ```shell")围栏粘连,文字无缺失;
      其余一致:provisioner 表(10 行)、卷扩展表(5 行)、10 个代码样本下载链接与 YAML、
      Default StorageClass/Reclaim policy/Volume binding mode/Allowed topologies、
      AWS EBS/EFS/NFS/vSphere/Ceph RBD/Azure Disk/Azure File/Portworx/Local 各参数节、
      全部链接逐项一致
- [x] docs/concepts/storage/storage-limits/ — 2026-09-17 — 通过;默认限制表(3 行)、
      动态卷限 5 条列表、3 处 Feature state(Stable v1.17、Stable gate:
      MutableCSINodeAllocatableCount v1.36 含 More information、Beta gate:VolumeLimitScaling
      v1.37)、2 个 CSIDriver YAML、Cluster autoscaler 节、全部链接逐项一致
- [x] docs/concepts/storage/volume-attributes-classes/ — 2026-09-17 — 通过;
      Feature state(Stable gate:VolumeAttributesClass v1.36 含 More information)、
      "GA as of version 1.34, and users have the option to disable it" 与上游一致(上游固有)、
      Provisioner/Resizer/Parameters 各节、4 个 YAML 块、全部链接逐项一致
- [x] docs/concepts/storage/volume-health-monitoring/ — 2026-09-17 — 轻微问题:1 处 NOTE
      碎片化(`CSIVolumeHealth` 与 Limitations 链接独占段,13-22 行,模式⑤),文字无缺失;
      其余一致:Feature state(Alpha gate:CSIVolumeHealth v1.21 含 More information)、
      4 个 RPC 说明、Pod/CSINode/PVC 三个 status YAML、Enabling/Monitoring/Limitations 2 条、
      全部链接与 What's next 2 条逐项一致
- [x] docs/concepts/storage/volume-populators-and-data-sources/ — 2026-09-17 — 轻微问题:1 处
      NOTE 碎片化(`gateway.networking.k8s.io` 与 ReferenceGrant 链接独占段,66-75 行,模式⑤),
      文字无缺失;其余一致:3 处 Feature state(Beta v1.24、Alpha v1.26 ×2)、dataSourceRef
      与 dataSource 差异列表、3 个 YAML(含 ReferenceGrant)、全部链接与 What's next 4 条逐项一致
- [x] docs/concepts/storage/volume-pvc-datasource/ — 2026-09-17 — 轻微问题:1 处 NOTE 碎片化
      (`spec.resources.requests.storage` 独占段,46-51 行,模式⑤),文字无缺失;
      其余一致:Introduction/Provisioning/Usage 各节、使用注意 6 条列表、1 个 PVC YAML、
      57 行 "it's original dataSource" 上游笔误、全部链接逐项一致

- [x] docs/concepts/storage/volume-snapshot-classes/ — 2026-09-17 — 通过;Introduction、
      VolumeSnapshotClass 资源(2 个 YAML)、依赖小节、Driver/DeletionPolicy/Parameters 各节、
      全部链接(volume-snapshots、storage-classes、#delete 锚)逐项一致
- [x] docs/concepts/storage/volume-snapshots/ — 2026-09-17 — 通过(附注):①151 行
      `"true"`needs` 缺空格与 163 行 annotations 下的列表项缩进(- snapshot.storage…)均为
      上游固有,忠实保留;其余一致:Introduction/生命周期/VolumeSnapshots/Contents/
      卷模式转换/Snapshot Topology/从快照供给卷各节、6 个 YAML 块、全部链接逐项一致
- [x] docs/concepts/storage/volumes/ — 2026-09-17 — 通过;46 个 HTML 标题与 md 一一对应
      (diff 中的差异均为 YAML 注释行/行内代码提取误差)、15 处 Feature state/locked 说明、
      2 个代码样本下载链接、全部 67 处链接经集合比对一致(architecture/cri 经 _redirects 503 行
      正确改写为 containers/cri;glossary 链接去掉 ?all=true;./ 为自引用链接);
      多处 NOTE/CAUTION 按既有模式含碎片化,文字无缺失
- [x] docs/concepts/storage/windows-storage/ — 2026-09-17 — 通过;持久存储说明与
      不支持功能 10 条列表、卷插件两类列表、In-tree 插件 2 条;"provides an storage overview"
      笔误为上游固有;全部链接逐项一致
- [x] docs/concepts/windows/ — 2026-09-17 — 问题:第三方 callout 正文丢失:HTML
      "This item links to a third party project or product that is not part of Kubernetes itself."
      整句缺失,md(5 行)仅剩 "[More information](#third-party-content-disclaimer)" 碎片(问题②);
      其余一致:Windows server 外链、9 条相关阅读列表、2 条概览列表、底部 disclaimer 逐项一致
- [x] docs/concepts/windows/intro/ — 2026-09-17 — 问题:①209 行 "version-skew policy" 链接丢失
      (HTML 链接 /docs/setup/release/version-skew-policy/,md 为纯文本;链接丢失不止 /blog/);
      ②2 处第三方 callout 正文丢失("Container runtimes" 节 170-174 行、"Hardware recommendations
      and considerations" 节 213-217 行,问题②);③轻微:"OS field" 项尾段("In the above list,
      wildcards…")在 HTML 中位于字段列表之后,md 合并进首段(44 行)且子列表后移,文字无缺失;
      ④1 处 NOTE 碎片化(known limitation 链接,184-189 行,模式⑤);
      其余一致:20 个受限字段列表、Windows OS 版本 dl(LTSC dt + 2022/2025 两个 dd 完整)、
      ContainerD Feature state(Stable v1.20)、Pause container/Node problem detector/
      Deployment tools/Getting help 各节与全部链接逐项一致- [x] docs/concepts/workloads/ — 2026-09-17 — 通过
      (含 glossary 术语链接化);内置 workload 资源 4 条列表、Workload placement 节
      (Beta gate:GenericWorkload v1.37 含 More information,链接 ../../reference、../../tasks
      解析深度正确)、What's next 各段与列表逐项一致
- [x] docs/concepts/windows/user-guide/ — 2026-09-17 — 轻微问题:已知问题⑥:开始部署步骤
      1、2(69-75 行)与 RuntimeClass 步骤 1、3(173、192 行)的 ```bash/```yaml 围栏直接接在
      步骤文字行尾,文字无缺失;其余一致:win-webserver.yaml(含 PowerShell 长命令)、
      验证 7 条列表、LogMonitor、RunAsUserName/GMSA、taints 与 .spec.os.name、
      windows-build 表(2 行)、RuntimeClass 示例 YAML、全部链接逐项一致

- [x] docs/concepts/workloads/ — 2026-09-17 — 通过
      (含 glossary 术语链接化);内置 workload 资源 4 条列表、Workload placement 节
      (Beta gate:GenericWorkload v1.37 含 More information,链接 ../../reference、../../tasks
      解析深度正确)、What's next 各段与列表逐项一致

- [x] docs/concepts/workloads/autoscaling/ — 2026-09-17 — 通过;手动/自动伸缩各节、
      2 处 Feature state(Stable v1.25、Stable gate:InPlacePodVerticalScaling v1.35 含
      More information)、NOTE 碎片化(Metrics Server 链接,模式⑤)文字无缺失、
      Cluster Proportional/KEDA/节点伸缩各节、全部链接与 What's next 逐项一致
- [x] docs/concepts/workloads/autoscaling/horizontal-pod-autoscale/ — 2026-09-17 — 通过
      (勘误:先前误判 2 处术语链接少一级 concepts/(11、369 行),经解析验证均正确,现予更正);
      轻微:①算法详解中数学式以 LaTeX 源保留(HTML 本身即字面 "\( … \)" 文本,md "\\(" 渲染等价,
      非提取问题);②Pod readiness 两个小节的行内列表粘连与 `\-` 转义(92、100 行,已知⑥);
      ③metrics.k8s.io 条目内嵌 NOTE 与代码块(199-203 行,已知⑥);④多处 NOTE 碎片化(模式⑤);
      其余一致:6 处 Feature state/locked 说明、mermaid 图、算法详解各段、
      Configurable scaling behavior 全部 YAML、Client/Server Side Apply tabs、
      全部链接与 What's next 逐项一致
- [x] docs/concepts/workloads/autoscaling/vertical-pod-autoscale/ — 2026-09-17 — 通过
      (勘误:先前误判 5 处链接少一级 concepts/(3、9、54、205、211 行),经解析验证均正确,现予更正);
      其余一致:vpa-architecture.svg 图片(5 级相对路径正确,资产已复制)、
      三个组件说明、Update modes 全部 6 种、resourcePolicy YAML 与字段说明、
      LimitRange 小节、全部其余链接(4 级 ../../../../reference、tasks 与 2 级 ../../controllers 等)
      与 What's next 逐项一致
- [x] docs/concepts/workloads/compositepodgroup-api/ — 2026-09-17 — 通过;
      Feature state(Alpha gate:CompositePodGroup v1.37 含 More information)、
      API structure 各小节、5 个 YAML 块、创建与查看命令、层级示例大 YAML、
      全部链接(2 级 ../../overview、3 级 ../../../reference/tasks、sibling 链接)与
      What's next 5 条逐项一致
- [x] docs/concepts/workloads/compositepodgroup-api/lifecycle/ — 2026-09-17 — 通过
      (勘误:先前误判 2 处链接少一级 concepts/(17、46 行),经解析验证均正确指向
      docs/concepts/ 下,现予更正);
      其余一致:Feature state(Alpha gate:CompositePodGroup v1.37 含 More information)、
      Ownership、Creation ordering 4 步、Limitations 7 条、../ 与 2 级/4 级链接、
      What's next 5 条逐项一致
- [x] docs/concepts/workloads/controllers/ — 2026-09-17 — 通过
      (含 glossary 术语链接化);Deployment/StatefulSet/DaemonSet/Job 介绍段、
      其他主题 2 条列表、全部链接(15 处)逐项一致
- [x] docs/concepts/workloads/controllers/cron-jobs/ — 2026-09-17 — 勘误:先前误判 5 处链接少一级 concepts/(11 行 ×2、92 行 ×2、187 行),经解析验证均正确,现予更正;
      其余一致:Feature state(Stable v1.21、Time zones Stable v1.27)、cronjob.yaml 代码样本、
      cron 语法图、宏表(5 行)、并发策略 3 条、CAUTION 碎片化 2 处(`.spec.suspend`、
      `startingDeadlineSeconds`,模式⑤)、Job creation 详解各段、全部链接与 What's next 逐项一致
- [x] docs/concepts/workloads/controllers/daemonset/ — 2026-09-17 — 勘误:先前误判 14 处链接少一级 concepts/(77-215 行各处),经解析验证均正确,现予更正;
      其余一致:daemonset.yaml 代码样本、Required Fields/Pod Template/Pod Selector、调度说明与
      nodeAffinity YAML、容忍表(7 行)、通信 4 种模式、更新与替代方案、全部其余链接与 What's next 逐项一致
- [x] docs/concepts/workloads/controllers/deployment/ — 2026-09-17 — 勘误:先前误判 5 处链接少一级 concepts/(5、881、883、909 行),经解析验证均正确,现予更正;
      其余一致:Feature state(Terminating Pods Beta gate:DeploymentReplicaSetTerminatingReplicas v1.35)、
      nginx-deployment.yaml 代码样本、3 个 strategy 面板、pod-template-hash、Rollover/Label selector
      updates/Deployment status 各节、全部其余链接与 What's next 逐项一致
- [x] docs/concepts/workloads/controllers/job/ — 2026-09-17 — 勘误:先前误判 12 处链接少一级 concepts/(185、242、257、273、501、792、805、931、1132 行),经解析验证均正确,现予更正;
      其余一致:13 处 Feature state/locked 说明、job.yaml 等 8 个代码样本(含下载链接)、并行/完成模式/
      podFailurePolicy/successPolicy/终止与清理/TTL/Job patterns 两表/suspend/manualSelector/
      podReplacementPolicy/managedBy 各节、全部其余链接与 What's next 逐项一致
- [x] docs/concepts/workloads/controllers/replicaset/ — 2026-09-17 — 勘误:先前误判 8 处链接少一级 concepts/(9、11、238、250、284、406 行),经解析验证均正确,现予更正;
      其余一致:frontend.yaml/pod-rs.yaml/hpa-rs.yaml 代码样本、获取 Pod 输出块、删除/缩容/隔离/
      成本注释/HPA 各节、2 处 Feature state(Beta v1.35、Beta v1.22)、全部其余链接与 What's next 逐项一致
- [x] docs/concepts/workloads/controllers/replicationcontroller/ — 2026-09-17 — 勘误:先前误判 5 处链接少一级 concepts/(122、124、146、234 行),经解析验证均正确,现予更正;
      其余一致:replication.yaml 代码样本与输出块、Writing manifest 各节、常见用法各节、职责说明、
      全部其余链接与 What's next 逐项一致
- [x] docs/concepts/workloads/controllers/statefulset/ — 2026-09-17 — 严重问题:Update strategies
      中 `Recreate` 词条描述文本丢失(HTML dl 的 dd 含完整句子 "The Recreate update strategy
      deletes all of the StatefulSet's Pods before creating new Pods that reflect modifications
      made to a StatefulSet's .spec.template. Using this strategy requires the
      StatefulSetRecreateStrategy feature gate to be enabled. See Recreate for details.",
      md 243-251 行仅剩 "`Recreate`"、"`.spec.template`"、"`StatefulSetRecreateStrategy`"、
      [feature gate] 链接、[Recreate](#recreate) 链接五个散落碎片,连接文字全部丢失;
      疑似 dd 内混排 feature-state-notice/details 块与正文导致提取失败,同类风险页需复查);
      勘误:先前误判约 12 处链接少一级 concepts/(24、26、101、103、111、113、144、153、170、
      180、436 行),经解析验证均正确,现予更正;
      其余一致:Feature state ×6(StatefulSetStartOrdinal/PodIndexLabel/Recreate ×2/
      maxUnavailable/AutoDeletePVC)、组件 YAML、Pod Identity 各节与 DNS 表(3 行)、
      保证/更新策略/分区滚动/forced rollback/Recreate 完整章节/Revision history/PVC retention
      各节、全部其余链接与 What's next 逐项一致
- [x] docs/concepts/workloads/controllers/ttlafterfinished/ — 2026-09-17 — 通过
      (勘误:先前误判 4 处链接少一级 concepts/(controller、garbage-collection、finalizers、
      labels),经解析验证均正确,现予更正);
      其余一致:Feature state(Stable v1.23)、
      Cleanup 机制说明、TTL 设置 5 种方式列表、Caveats 两节(更新 TTL/时间偏斜)、
      全部其余链接与 What's next 逐项一致
- [x] docs/concepts/workloads/management/ — 2026-09-17 — 问题:Helm 小节第三方 callout 正文丢失
      ("This item links to a third party project or product that is not part of Kubernetes itself.",
      md 88 行仅剩 "[More information]" 碎片,问题②);
      其余一致:nginx-app.yaml 多资源清单、kubectl 批量操作、递归操作、无中断更新与
      rollout 管理、Canary、annotations/scale/in-place 更新(apply/edit/patch)/disruptive 各节、
      全部链接(3 级 ../../../tasks、reference/kubectl/generated 与 2 级 ../../overview 均深度正确)
      与 What's next 逐项一致
- [x] docs/concepts/workloads/podgroup-api/ — 2026-09-17 — 轻微问题:2 处 NOTE 碎片化
      (`PodGroupInitiallyScheduled`+Limitations 链接;`PodGroup`/`spec.parentCompositePodGroupName`/
      `spec.workloadRef` 等,94-103、200-233 行,模式⑤),文字无缺失;
      其余一致:3 处 Feature state(Beta gate:GenericWorkload、Beta gate:DRAWorkloadResourceClaims、
      Alpha gate:CompositePodGroup,均含 More information)、API structure 各小节与 4 个 YAML、
      DRA 设备请求节、创建命令、层级示例、全部链接与 What's next 4 条逐项一致
- [x] docs/concepts/workloads/podgroup-api/lifecycle/ — 2026-09-17 — 通过
      (勘误:先前误判 2 处链接少一级 concepts/(17、55 行),经解析验证均正确,现予更正);
      其余一致:Feature state(Beta gate:GenericWorkload v1.37 含 More information)、
      Ownership/Creation ordering/Deletion protection/controller vs user-managed 各节、
      Limitations 6 条、全部其余链接与 What's next 5 条逐项一致
- [x] docs/concepts/workloads/pods/ — 2026-09-17 — 问题:What's next 博客链接丢失(问题③):
      312 行 "Distributed System Toolkit: Patterns for Composite Containers" 为纯文本
      (HTML 链接 /blog/2015/06/the-distributed-system-toolkit-patterns/);
      轻微:2 处 NOTE 碎片化(container runtime 链接,11-16 行;`status.observedGeneration`
      与 **not**,176-185 行,模式⑤),文字无缺失;
      其一致:Feature state ×4(Pod OS Stable v1.25、GenericWorkload Beta v1.37、
      PodObservedGenerationTracking Stable gate v1.35、SidecarContainers Stable gate v1.33)、
      simple-pod.yaml 与 Job 模板 YAML、更新限制列表、subresources 4 条、
      Direct/Indirect status 各 3 条、pod.svg 图片(资产已复制)、
      全部链接与 What's next(含 prior art 外链)逐项一致
- [x] docs/concepts/workloads/pods/advanced-pod-config/ — 2026-09-17 — 勘误:先前误判约 11 处链接少一级 concepts/(3、7、44、64、70、255、293-297 行),
      经解析验证均正确,现予更正;轻微:CAUTION 碎片化(`securityContext`、privileged mode 链接、
      `windowsOptions.hostProcess` 等,135-160 行,模式⑤);
      其余一致:PriorityClass/RuntimeClass/security context 各节与 8 个 YAML 块、
      调度影响(node selector/affinity/pod affinity/tolerations)、Pod overhead、
      全部其余链接(4 级 ../../../../reference、tasks)与 What's next 逐项一致
- [x] docs/concepts/workloads/pods/disruptions/ — 2026-09-17 — 勘误:先前误判约 12 处链接少一级 concepts/
      (系统性模式误判):18 行 node-pressure-eviction、31 行 Node Autoscaling、47 行 anti-affinity、
      49 行 PriorityClasses、145 行 preemption、149 行 preempted 与 Pod priority preemption(2 处)、
      151 行 Pod garbage collection、153 行 taint-based evictions、155 行 eviction using the
      Kubernetes API、157 行 node pressure eviction、161 行 graceful node shutdown、
      165 行 Pod container limits、167 行 graceful node shutdown(均 `../../../…`,
      HTML /docs/concepts/…);其余一致:Feature state(Stable v1.21)与 locked 说明(1.31)、
      6 张集群状态表、dl 形式的 5 种 disruption 条件、全部 4 级链接与 What's next 逐项一致
- [x] docs/concepts/workloads/pods/downward-api/ — 2026-09-17 — 通过(附注):resourceFieldRef
      节 NOTE 内的 feature-state(InPlacePodVerticalScaling Stable v1.35)在 HTML 中即为转义文本
      (<pre><code> 内),md(84-103 行)以代码块忠实保留,非提取问题;
      勘误:先前误判 6 处链接少一级 concepts/(30、38、42、52、82、145 行),
      经解析验证均正确,现予更正;
      42 行 labels、52 行 nodes、82 行 requests and limits、145 行 downwardAPI 卷
      (均 `../../../…`,HTML /docs/concepts/…);
      其余一致:fieldRef/resourceFieldRef 字段 dl(粗体术语+说明,完整)、
      全部其余链接(4 级 ../../../../tasks、reference)与 What's next 逐项一致
- [x] docs/concepts/workloads/pods/ephemeral-containers/ — 2026-09-17 — 轻微问题:1 处 NOTE
      碎片化(static pods 链接独占段,25-30 行,模式⑤),文字无缺失;
      其余一致:Feature state(Stable v1.25)、与普通容器差异 3 条、distroless 场景、
      全部链接(4 级 ../../../../reference、tasks)与 What's next 逐项一致

- [x] docs/concepts/workloads/pods/init-containers/ — 2026-09-17 — 勘误:先前误判约 6 处链接少一级
      concepts/(系统性模式误判):26 行 volumes、48 行 Secrets、56 行 Service、65 行 Volume、
      及 What's next 中同类(均 `../../../…`,HTML /docs/concepts/…);
      已知问题⑥:Examples 列表 4 条的 ```shell 围栏直接接在条目文字行尾(56-64 行);
      其余一致:Understanding/Differences from regular 与 sidecar containers 各节、
      myapp-pod 示例 YAML 与 describe 输出、init-myservice/mydb 修改后 YAML、
      Resources 节(4 级链接正确)、What's next 逐项一致
- [x] docs/concepts/workloads/pods/pod-condition/ — 2026-09-17 — 通过
      (勘误:先前误判约 11 处链接少一级 concepts/(7、123、146、150、154、162、164、220 行),
      经解析验证均正确,现予更正);轻微:NOTE 碎片化 2 处(`Ready`/`ContainersReady`/`readinessGates`
      链;`DisruptionTarget`,模式⑤);其余一致:字段表(7 行含 observedGeneration)、
      生命周期 5 条件列表、DisruptionTarget 5 种 reason dl、PodResizePending/InProgress 说明、
      readinessGates YAML、1 处 Feature state(Stable gate:PodReadyToStartContainersCondition
      v1.37 含 More information)、全部其余链接与 What's next 逐项一致

- [x] docs/concepts/workloads/pods/pod-hostname/ — 2026-09-17 — 通过
      (勘误:先前误判 20 行链接少一级 concepts/,经解析验证该链接正确,现予更正);
      其余一致:默认 hostname、hostname/subdomain、setHostnameAsFQDN(Stable v1.22,
      64 字符限制 NOTE)、hostnameOverride(Stable gate:HostnameOverride v1.37 含
      More information)、2 个示例 YAML、全部链接逐项一致

- [x] docs/concepts/workloads/pods/pod-lifecycle/ — 2026-09-17 — 勘误:先前误判约 20 处链接少一级
      concepts/(系统性模式误判;本页目录较深,经解析验证全部 `../../../…` 链接均正确
      指向 docs/concepts/ 下):涉及 7 行(UID、nodes)、15/17 行 scheduling-eviction、
      25 行(eviction、nodes)、27 行 controller、35 行 volumes、61-64 行 Pod API 4 级正确、
      92 行 Secret、120 行 probes、145/267/269 行 init/sidecar containers、657-669 行
      (hooks、Service、selector、EndpointSlice conditions)、699 行 controller、
      707/709 行 labels-annotations 与 disruptions 等;已知问题⑥:Termination flow 第 2 步
      内嵌 NOTE(657 行 "…shutdown process.  > [!NOTE]")且子列表 1./1. 编号重复为上游固有;
      其余一致:10 处 Feature state、phase 表(5 行)、restart 行为对比表(2 行)、
      PodConditions 字段表(6 行)、3 个 restart 示例 YAML 与 restart-all-containers 示例、
      kubelet config 2 例、pod.svg 图片(资产已复制)、全部其余链接与 What's next 逐项一致
- [x] docs/concepts/workloads/pods/pod-qos/ — 2026-09-17 — 勘误:先前误判约 9 处链接少一级 concepts/
      (系统性模式误判):7 行(manage-resources-containers、containers、node-pressure-eviction ×2)、
      11 行 cpu-management-policies(4 级正确)、22 行 pod-level-resources、129 行 eviction、
      131 行 preemption、132 行 in-place resize、136-138 行 What's next 前 3 条
      (均 `../../../…`,HTML /docs/concepts/…);轻微:CAUTION 碎片化
      (`memoryReservationPolicy: TieredReservation`/`memory.min`/`memory.max` 独占段,95-112 行,
      模式⑤);其余一致:Guaranteed/Burstable/BestEffort 判据、Pod-level resources 2 条
      (Beta gate:PodLevelResources v1.34)、Memory QoS(Beta gate:MemoryQoS v1.37)含
      公式与 KubeletConfiguration YAML、TieredReservation 说明、全部其余链接逐项一致
- [x] docs/concepts/workloads/pods/probes/ — 2026-09-17 — 勘误:先前误判 36 行 EndpointSlice 链接少一级 concepts/,
      经解析验证该链接正确,现予更正;
      附注:57 行数学式以 LaTeX 源保留(HTML 即字面文本,渲染等价,非提取问题);
      轻微:NOTE/CAUTION 碎片化(conditions 链接与 Pod termination 链接、`exec`/
      `initialDelaySeconds`/`periodSeconds`,40-49、97-110 行,模式⑤),文字无缺失;
      其余一致:4 种机制说明、配置字段 dl、probe-level terminationGracePeriodSeconds
      (Stable v1.28)、HTTP/TCP/gRPC 详解、H2CContainerProbe 与 GRPCContainerProbeTLS
      (Alpha,含 More information)、redirect handling 与 10KiB CAUTION、
      grpc-liveness.yaml 代码样本、全部其余链接与 What's next 逐项一致
- [x] docs/concepts/workloads/pods/scheduling-group/ — 2026-09-17 — 通过
      (勘误:先前误判 61 行 gang scheduling 链接少一级 concepts/,经解析验证该链接正确,
      现予更正);
      其余一致:2 处 Feature state(Beta gate:GenericWorkload v1.37、Alpha gate:
      CompositePodGroup v1.37,均含 More information)、schedulingGroup YAML、
      Behavior 两条策略说明、Missing group references 节、全部其余链接与 What's next 4 条逐项一致
- [x] docs/concepts/workloads/pods/sidecar-containers/ — 2026-09-17 — 问题:What's next 博客链接
      丢失(问题③):168 行 "native sidecar containers" 为纯文本(HTML 链接
      /blog/2023/08/25/native-sidecar-containers/);勘误:先前误判 2 处链接少一级 concepts/(153、171 行 pod-overhead),
      经解析验证均正确,现予更正;
      轻微:NOTE 碎片化(`initContainers`/`restartPolicy: Always`,25-34 行,模式⑤);
      其余一致:Feature state(Stable gate:SidecarContainers v1.33 含 More information)、
      deployment-sidecar.yaml 与 job-sidecar.yaml 代码样本、Pod lifecycle/差异/资源分享规则
      (含 cgroups 小节)逐项一致
- [x] docs/concepts/workloads/pods/static-pods/ — 2026-09-17 — 勘误:先前误判约 6 处链接少一级 concepts/
      (系统性模式误判):3 行 API server(`../../../architecture/#kube-apiserver`)、7 行
      control plane components(`../../../overview/components/#…`)、24 行 labels 与
      selectors(2 处)、30 行 ConfigMap、45 行 Kubernetes components(均 `../../../…`,
      HTML /docs/concepts/…);轻微:NOTE 碎片化(`kube-system`、`kubernetes.io/config.mirror`,
      9-18 行,模式⑤);其余一致:Mirror Pods 说明、Limitations、与 DaemonSet 对比、
      全部其余链接(4 级 ../../../../reference、tasks 均正确)与 What's next 逐项一致
- [x] docs/concepts/workloads/pods/user-namespaces/ — 2026-09-17 — 问题:①"Before you begin"
      第三方 callout 正文丢失("This section links to third party projects…",md 17-21 行仅剩
      "**Note:**"+content guide+More information 碎片,问题②);勘误:先前误判 146 行 Pod Security Standards 链接少一级 concepts/,
      经解析验证该链接正确,现予更正;
      其余一致:Feature state(Stable gate:UserNamespacesSupport v1.36 含 More information)、
      介绍与原理各节、运行时支持列表、subuid/subgid 约束 6 条、KubeletConfiguration 配置、
      与其他实现的差异、全部其余链接(4 级/5 级均正确)与 What's next 逐项一致
- [x] docs/concepts/workloads/workload-api/ — 2026-09-17 — 轻微问题:页面末尾 4 条相关链接
      (Pod Group Disruption and Priority 等,205-208 行)在 HTML 中为 ul 列表项,md 丢失列表
      标记成为裸链接行,链接与文字无缺失;3 处 NOTE 碎片化(34-47、93-118、136-149 行,模式⑤);
      其余一致:3 处 Feature state(Beta gate:GenericWorkload、Alpha gate:CompositePodGroup、
      Alpha gate:WorkloadWithJob,均含 More information)、API structure 各节、
      层级模板 YAML、限制 2 条、全部链接与 What's next 逐项一致
- [x] docs/concepts/workloads/workload-api/disruption-and-priority/ — 2026-09-17 — 通过
      (勘误:先前误判 6 处链接少一级 concepts/(11、26、74、104、127、145 行),经解析验证
      均正确,现予更正);
      轻微:2 处 NOTE 碎片化(`priority`/`disruptionMode`;`CompositePodGroup`,15-36、108-113 行,
      模式⑤);78 行 "PriorirtyClass" 上游笔误;其余一致:4 处 Feature state(均含 More
      information)、Single/All 两种模式、priority/preemptionPolicy 各节、YAML 示例、
      全部其余链接与 What's next 逐项一致
- [x] docs/concepts/workloads/workload-api/policies/ — 2026-09-17 — 通过
      (勘误:先前误判 3 处链接少一级 concepts/(21、89、90 行),经解析验证均正确,现予更正);
      其余一致:2 处 Feature state(Beta gate:
      GenericWorkload v1.37、Alpha gate:CompositePodGroup v1.37,均含 More information)、
      basic/gang 策略 2 个 YAML、CompositePodGroup 策略与 minGroupCount YAML、
      全部其余链接与 What's next 5 条逐项一致
- [x] docs/concepts/workloads/workload-api/topology-aware-scheduling/ — 2026-09-17 — 通过
      (勘误:先前误判 2 处链接少一级 concepts/(36、83 行),经解析验证均正确,现予更正);
      其余一致:2 处 Feature state(Alpha gate:TopologyAwareWorkloadScheduling v1.36、
      Alpha gate:CompositePodGroup v1.37,均含 More information)、gang/basic 两种 TAS、
      schedulingConstraints YAML、多层级解析与 Workload 层级示例 YAML、全部其余链接逐项一致
- [x] docs/concepts/workloads/workload-api/workloadbuilder/ — 2026-09-17 — 通过
      (勘误:先前误判 52 行起 3 处链接少一级 concepts/,经解析验证均正确,现予更正);
      其余一致:Feature state(Beta gate:GenericWorkload
      v1.37 含 More information)、构建块各小节、2 个 Go 代码块、Builder 四步流程、
      声明式校验与 allow-list 说明、全部其余链接与 What's next 6 条逐项一致
- [x] docs/contribute/ — 2026-09-17 — 通过;短页,贡献方式说明、k8s.dev 与 CNCF 外链、
      docs/ 与 blog/ 内链逐项一致
- [x] docs/contribute/advanced/ — 2026-09-17 — 通过;Propose improvements/Release coordination/
      New Contributor Ambassador/Sponsor/SIG Co-chair 各节、会议指南各条、
      claim-host.png 图片(3 级相对路径正确,资产已复制)、全部链接(1 级与外部)逐项一致
- [x] docs/contribute/analytics/ — 2026-09-17 — 通过;短页,Looker Studio 仪表盘说明与
      3 个外链逐项一致
- [x] docs/contribute/blog/ — 2026-09-17 — 问题:2 处 /blog/ 链接丢失(问题③):①13 行
      "The main Kubernetes blog is used by…"中 Kubernetes blog 链接(HTML /blog/)为纯文本;
      ②44 行 What's next "Kubernetes blog"(HTML /blog/)为纯文本;
      其余一致:Main blog/Contributor blog/Evergreen 各节、What's next 其余 6 条
      (article-submission、guidelines、article-mirroring、release-comms、writing-buddy、
      reviewing-prs#blog)逐项一致
- [x] docs/contribute/blog/article-mirroring/ — 2026-09-17 — 通过(附注):①9 行
      "# Before you begin" 的 h1 级标题为上游固有(HTML 同为 h1),非提取问题;
      ②3 行 "its own blog" 在 HTML 中亦无链接,上游一致;
      已知问题⑥/段落合并:24 行列表项内尾段合并("…criteria are met:  This is because…"),
      子列表后移,文字无缺失;其余(Mirroring 原则、条件列表、How to mirror、
      canonicalUrl、全部链接)逐项一致
- [x] docs/contribute/blog/article-submission/ — 2026-09-17 — 通过;三条投稿路线、
      Article scheduling、Authoring 各节(Initial steps/Drafting/Markdown for publication/
      Front matter YAML/Article content/Diagrams/Commit messages/Squashing)、
      123 行 "use the figure shortcode can be used" 病句为上游固有;
      全部链接(1 级 relatives、Slack、externals)逐项一致
- [x] docs/contribute/blog/guidelines/ — 2026-09-17 — 通过(附注):7 行 "# Before you begin"
      的 h1 级标题为上游固有(与 article-mirroring 页一致);
      其余一致:Original content/Relevant content/Localization/Copyright/SIG/
      National restrictions/Blog-specific guidance(Diagrams/Timelessness/Content examples/
      不可接受内容列表)各节与全部链接逐项一致
- [x] docs/contribute/blog/release-comms/ — 2026-09-17 — 通过;Release Comms 说明、
      Opting in/Preparing/Publication 各节、CAUTION 碎片化(**must**、`/hold`,模式⑤)
      文字无缺失、全部链接逐项一致
- [x] docs/contribute/blog/writing-buddy/ — 2026-09-17 — 通过;Buddy responsibilities、
      Supporting the blog team/your buddy、Collaborative editing 与 Markdown/Git 两个 Panel、
      Pull request review、Subsequent steps 各节、全部链接逐项一致
- [x] docs/contribute/docs/ — 2026-09-17 — 通过;Getting started、2 个 mermaid 流程图
      (roadmap/first contribution)、first contribution 列表、Getting help、Get involved with
      SIG Docs、Other ways、Next steps 各节与全部链接逐项一致
- [x] docs/contribute/generate-ref-docs/ — 2026-09-17 — 通过(附注):section-index 最后一条
      `##### [](prerequisites-ref-docs/)` 空链接文本为上游固有(HTML 中 <a> 亦无文字),
      忠实保留;开头 2 条 quickstart/release-generation 链接与其余 7 条条目顺序一致
- [x] docs/contribute/generate-ref-docs/config-api/ — 2026-09-17 — 通过;genref 工具说明、
      Requirements 列表、仓库设置、build 变量、copyconfigapi 步骤、预览与提交各节、
      全部链接(2 级 ../../new-content、3 级 ../../../reference/config-api)逐项一致
- [x] docs/contribute/generate-ref-docs/contribute-upstream/ — 2026-09-17 — 通过;Before you
      begin、Clone 仓库、生成与提交各节、全部链接(1 级 siblings、3 级
      ../../../reference/generated/kubernetes-api/v1.37/)逐项一致
- [x] docs/contribute/generate-ref-docs/kubectl/ — 2026-09-17 — 通过;开头 NOTE 碎片化
      (4 个 kubectl 链接独占段,8-33 行,模式⑤)文字无缺失;Requirements、
      克隆与生成各节、2 个代码样本、全部链接逐项一致
- [x] docs/contribute/generate-ref-docs/kubernetes-api/ — 2026-09-17 — 通过;OpenAPI 说明、
      Requirements、克隆仓库、生成与提交各节、全部链接逐项一致
- [x] docs/contribute/generate-ref-docs/kubernetes-components/ — 2026-09-17 — 通过;短页,
      prerequisites 与 quickstart 链接及 What's next 4 条逐项一致
- [x] docs/contribute/generate-ref-docs/metrics-reference/ — 2026-09-17 — 通过;metrics 生成
      说明、Requirements、生成步骤各节、全部链接逐项一致
- [x] docs/contribute/generate-ref-docs/prerequisites-ref-docs/ — 2026-09-17 — 通过(附注):
      上游页面 h1 为空(HTML <h1></h1> 无文字,上游固有),md 以 "# Requirements:" 标题 +
      "### Requirements:" 保留;Requirements 列表与链接逐项一致
- [x] docs/contribute/generate-ref-docs/quickstart/ — 2026-09-17 — 通过;15 个标题与 HTML
      一致、Requirements/克隆/生成各节、全部链接逐项一致
- [x] docs/contribute/generate-ref-docs/release-generation/ — 2026-09-17 — 通过;22 个标题
      与 HTML 一一对应(md 侧多出的匹配为 YAML 注释行误计);staging module 检查、
      生成与发布各节、全部链接逐项一致
- [x] docs/contribute/localization/ — 2026-09-17 — 通过;26 个标题与 HTML 一致(md 侧多出 3 个
      为 OWNERS 示例的 YAML 注释行误计);Contribute to existing/Start a new localization
      各节(Find community、GitHub org、teams.yaml、workflow、hugo.toml 配置、OWNERS、
      最低内容要求、源码示例)、全部链接(1 级 relatives 与外链)逐项一致
- [x] docs/contribute/new-content/ — 2026-09-17 — 通过;New content task flow mermaid 图、
      Contributing basics 列表、case studies 与 blog articles 链接、
      全部链接(1 级 relatives 与外链)逐项一致
- [x] docs/contribute/new-content/case-studies/ — 2026-09-17 — 通过;短页,CNCF 协作说明、
      2 个外链(case studies 源、guidelines)逐项一致
- [x] docs/contribute/new-content/new-features/ — 2026-09-17 — 通过;For documentation
      contributors / For developers 两大节、feature tracking sheet 说明、placeholder PR
      3 步、Feature gates 各小节、全部链接逐项一致
- [x] docs/contribute/new-content/open-a-pr/ — 2026-09-17 — 轻微问题:已知问题⑥大面积:
      各步骤的 ```shell/```none 围栏直接接在步骤文字行尾(130-141、142-151、175-177、
      186-216、277-304、325-355、380-411 行等)且多处 NOTE 内嵌于步骤中(54、62、152、208、
      283、340、393 行),文字与代码无缺失;其余一致:2 个开头 NOTE、3 个 mermaid 图、
      Changes using GitHub 7 步、local fork 全流程、merge conflicts/squashing 各节、
      全部链接与 What's next 逐项一致
- [x] docs/contribute/new-content/preview-locally/ — 2026-09-17 — 轻微问题:已知问题⑥:
      两个 Panel 内多步骤的 ```shell/``` 围栏直接接在步骤文字行尾(如 "…(if required) ```shell"、
      "…npm ci" 前步骤),文字与命令无缺失;其余一致:Hugo 容器/命令行两个 Panel、
      Troubleshooting(TOCSS/macOS 文件数)各节、全部链接逐项一致
- [x] docs/contribute/participate/ — 2026-09-17 — 通过;SIG Docs 说明、chairperson、
      GitHub teams 与 OWNERS/prow 各节、全部链接逐项一致
- [x] docs/contribute/participate/issue-wrangler/ — 2026-09-17 — 通过;Duties/Requirements/
      Prow commands 代码块/When to close issues 各节、全部链接(1 级 relatives、外链)逐项一致
- [x] docs/contribute/participate/pr-wranglers/ — 2026-09-17 — 通过;Duties 列表、
      NOTE(本地化 PR 除外)、Helpful GitHub queries 5 条、Prow commands 代码块、
      全部链接逐项一致
- [x] docs/contribute/participate/roles-and-responsibilities/ — 2026-09-17 — 轻微问题:已知⑥:
      `/lgtm` 与 `/approve` 相关 bullet 内嵌 NOTE/CAUTION(如 "…/approve` comment, which merges
      PRs into the repo. > [!CAUTION]"),文字无缺失;其余一致:四个角色(Anyone/Members/
      Reviewers/Approvers)说明与权限列表、netlify-pass.png 图片(资产已复制)、
      Becoming approver 各步、全部链接逐项一致
- [x] docs/contribute/review/ — 2026-09-17 — 通过;纯 section-index 页,2 个条目
      (Reviewing pull requests / for-approvers)链接一致
- [x] docs/contribute/review/for-approvers/ — 2026-09-17 — 轻微问题:已知⑥:2 处 bullet 内嵌
      NOTE(technical review 的 `reviewers` 字段;直接 push 到 k/website 的限制),文字无缺失;
      其余一致:PR Wrangler 说明、Commit into another person's PR、Prow commands 表(6 行)、
      Triage 各节、全部链接逐项一致
- [x] docs/contribute/review/reviewing-prs/ — 2026-09-17 — 通过;Before you begin、
      Review process mermaid 图、评审要点各节(technical accuracy 等)、全部链接逐项一致
- [x] docs/contribute/style/ — 2026-09-17 — 通过;纯 section-index 页,7 个条目
      (Content Guide/Style Guide/Diagram Guide/Write a new topic/Page content types/
      Content organization/Hugo Shortcodes)链接与顺序一致
- [x] docs/contribute/style/content-guide/ — 2026-09-17 — 通过
      (勘误:先前误判 31 行 3 处链接深度错误,经解析验证 `../../../concepts/…` 自本页
      正确解析到 docs/concepts/ 下,现予更正);轻微:NOTE 碎片化(sig-docs Slack 链接,模式⑤);
      其余一致:What's allowed/Third party content/Dual sourced content 各节与
      全部其余链接逐项一致
- [x] docs/contribute/style/content-organization/ — 2026-09-17 — 通过;10 个标题与 HTML 一致、
      Hugo Tip NOTE、Page Lists/Page Order/Main Menu/Head 管理/Extra formats 各节、
      全部链接逐项一致
- [x] docs/contribute/style/diagram-guide/ — 2026-09-17 — 通过;标题数与 HTML 一致;
      Mermaid 使用指南(mermaid 图内含 click 外链为上游固有)、创建方式 3 种、
      样式与 caption、注意事项各节、全部链接逐项一致
- [x] docs/contribute/style/hugo-shortcodes/ — 2026-09-17 — 通过;标题数与 HTML 一致;
      feature-state/feature-gate-description/glossary_tooltip/caption/tab/code_sample/
      third-party 等全部 shortcode 小节,示例的渲染输出(如 **[FEATURE STATE…]**、
      glossary 定义文本)与演示代码块一致;链接深度(3 级 ../../../reference、../../../concepts)
      解析正确;全部链接逐项一致
- [x] docs/contribute/style/page-content-types/ — 2026-09-17 — 通过(附注):md 中 2 个
      演示用标题(`{{% heading "whatsnext" %}}` 等)在 HTML 中为代码块演示内容,
      属页面自指涉特性,文字无缺失;四种 page content type 各节、全部链接逐项一致
- [x] docs/contribute/style/style-guide/ — 2026-09-17 — 通过;55 个标题与 HTML 一致;
      Language/Formatting standards(Content formatting/Dates/Code style/Placeholder content/
      Numbers/Inline code/Links/Lists/Markdown elements/Shortcodes/Headings/Paragraphs/
      Words to use 等)各节、4 处 3 级链接深度正确、全部链接逐项一致
- [x] docs/contribute/style/write-new-topic/ — 2026-09-17 — 通过;13 个标题与 HTML 一致;
      选择 title/filename、concept/task/tutorial 三类表格、代码示例指南各节、
      全部链接(3 级 ../../../concepts、tasks 与 4 级 reference)逐项一致
- [x] docs/contribute/suggesting-improvements/ — 2026-09-17 — 通过;Opening an issue 3 步、
      Suggesting new content、How to file great issues 6 条、全部链接逐项一致
- [x] docs/home/ — 2026-09-17 — 通过;文档首页各栏目(Understand/Try/Set up/Use/Concepts 等)
      列表与链接(1 级 relatives 指向 concepts/tutorials/setup 等)逐项一致
- [x] docs/home/supported-doc-versions/ — 2026-09-17 — 通过;短页,Latest 与 Older versions
      外链(v1-36 至 v1-33.docs.kubernetes.io)逐项一致
- [x] docs/reference/ — 2026-09-17 — 通过;API Reference/客户端库/CLI/Components/
      Config APIs/框架配置各节与全部链接(1 级 relatives、glossary、kubectl JSONPath 等)
      逐项一致
- [x] docs/reference/access-authn-authz/ — 2026-09-17 — 通过;短页,API 访问控制参考
      嵌套列表(13 条,含各子页与锚点)逐项一致
- [x] docs/reference/access-authn-authz/abac/ — 2026-09-17 — 轻微问题:已知问题⑥:Examples
      5 个编号步骤的 ```json 围栏直接接在步骤文字行尾(77-92 行),文字与 JSON 无缺失;
      其余一致:Policy File Format 嵌套列表、Authorization Algorithm、Kubectl 节、
      service account 说明、全部链接(jsonlines、releases.k8s.io、#examples)逐项一致
- [x] docs/reference/access-authn-authz/admission-controllers/ — 2026-09-17 — 通过(附注):
      35 行图片链接目标为 #ZgotmplZ(Hugo 模板转义产物,上游固有,md 忠实保留);
      全部准入控制器小节(AlwaysAdmit 至 ValidatingAdmissionWebhook,含 Type 标注与
      Feature state)、默认启用列表、配置文件格式 YAML、ImageReview JSON 示例、
      全部链接(2 级 ../../config-api 等、3 级 ../../../concepts 均深度正确)逐项一致
- [x] docs/reference/access-authn-authz/authentication/ — 2026-09-17 — 通过;68 个标题中
      20 个为 YAML 注释误计,实际与 HTML 54 个一一对应;8 处 Feature state 与 HTML 一致;
      Users/Strategies/Anonymous(含 AuthenticationConfiguration YAML)/X.509(Username/
      UID/Group/Node mapping、openssl 示例)/Bootstrap/SA tokens/JWT/OIDC/Webhook/
      Authenticating proxy/SA 投影卷等各节、全部链接逐项一致;多处 NOTE 碎片化(模式⑤)文字无缺失
- [x] docs/reference/access-authn-authz/authorization/ — 2026-09-17 — 轻微问题:①55 行
      CAUTION 开头 "+The" 多出 "+" 号(HTML 中该处为列表符残留,上游固有);②128 行 CAUTION
      混入警示标题文字 "Warning:" 且与正文粘连为 "Warning:Enabling"(HTML 中 "Warning:" 为
      <h4> 标题、"Enabling" 另起段落;其他页面约定是丢弃标题文字,此处却保留了标题文字,
      约定不一致);其余一致:verdicts、
      request attributes、verbs 表、Authorization modes 各条、StructuredAuthorizationConfiguration
      (Stable v1.32 含 More information)与完整 AuthorizationConfiguration YAML、
      Privilege escalation/Escalation paths、auth can-i 各示例、全部链接与 What's next 逐项一致
- [x] docs/reference/access-authn-authz/bootstrap-tokens/ — 2026-09-17 — 通过;Overview/Token
      format/Enabling/Secret format(yaml)/Token management/ConfigMap signing(yaml + JWS 说明)/
      CAUTION 各节、全部链接逐项一致
- [x] docs/reference/access-authn-authz/certificate-signing-requests/ — 2026-09-17 — 轻微问题:
      已知⑥:Kubernetes signers 第 5 项(kubernetes.io/kube-apiserver-serving)的 Feature state
      与 "> **More information about this feature**" 粘连嵌在列表项内(196-198 行),文字无缺失;
      其余一致:CSR 签发/授权/Signers 6 条详解/Kubelet serving 与 apiserver-serving/
      approval 与 signing YAML、PodCertificateRequests(Stable gate:PodCertificateRequest
      v1.37)、ClusterTrustBundles(signer-linked/unlinked、projection)各节、
      3 个 ClusterRole YAML、全部链接与 What's next 逐项一致
- [x] docs/reference/access-authn-authz/kubelet-authn-authz/ — 2026-09-17 — 通过;认证/授权说明、
      verbs 表、资源映射 2 张表、CAUTION、Fine-grained authorization(Stable gate:
      KubeletFineGrainedAuthz v1.36 含 More information)与属性列表、全部链接逐项一致
- [x] docs/reference/access-authn-authz/node/ — 2026-09-17 — 通过;Overview(读写/auth 操作)、
      3 处 Feature state(AuthorizeNodeWithSelectors Stable v1.34、
      ServiceAccountNodeAudienceRestriction Beta v1.33)、AuthorizationConfiguration YAML、
      audience 属性表与 3 个 RBAC YAML、Migration 各节、NOTE 碎片化(模式⑤)文字无缺失、
      全部链接逐项一致
- [x] docs/reference/access-authn-authz/webhook/ — 2026-09-17 — 通过;Webhook Mode 说明、
      kubeconfig 配置 YAML、请求/响应 JSON 各例(allow/deny/no-opinion/non-resource/
      selectors)、AuthorizeWithSelectors(Stable gate v1.34 含 More information)、
      非资源路径列表、全部链接逐项一致
- [x] docs/reference/access-authn-authz/extensible-admission-controllers/ — 2026-09-17 —
      轻微问题:已知⑥:Monitoring 节 3 个列表项的 ```yaml 围栏接在条目文字行尾(988、1010、
      1032 行),且多处 NOTE 碎片化(`<CA_BUNDLE>`、`clientConfig.service`、`timeout`、
      `kube-apiserver`、`.static.k8s.io`、`matchConditions` 等,模式⑤),文字无缺失;
      其余一致:3 处 Feature state(Alpha gate:APIServerWebhookAuthenticationToken v1.37、
      Beta gate:ExcludeAdmissionWebhookVirtualResources v1.37、matchConditions locked 1.30)、
      webhook 配置 YAML 与认证 kubeconfig、Request/Response 各 JSON 例(含 patch/warnings)、
      rules/objectSelector/namespaceSelector/matchPolicy/matchConditions/URL/service/
      sideEffects/timeouts/reinvocation/failure policy/monitoring 各节、
      webhook-auth-attest RBAC 示例、全部链接逐项一致
- [x] docs/reference/access-authn-authz/kubelet-tls-bootstrapping/ — 2026-09-17 — 通过;
      Initialization 15 步、Configuration/CA/apiserver/kube-controller-manager/kubelet 各节、
      Bootstrap tokens 与 token file 两种认证、3 个 ClusterRoleBinding YAML、
      bootstrap kubeconfig 示例与 kubectl 生成命令、证书轮换与 serving certificates、
      其他组件认证、kubectl approval 各节、全部链接逐项一致
- [x] docs/reference/access-authn-authz/manifest-admission-control/ — 2026-09-17 — 通过(附注):
      322-336 行存在编号列表序号重复("1. Initial load" 后再 "1. Atomic file updates",
      HTML/上游亦如此)属上游固有;轻微:NOTE/CAUTION 碎片化(模式⑤)文字无缺失;
      其余一致:Feature state(Beta gate:ManifestBasedAdmissionControlConfig v1.37)、
      支持资源类型表、配置类型表、3 个代码样本下载链接与 YAML、命名约定/限制/
      Evaluation order/文件监视与重载/Metrics 表/Audit/HA/Upgrade/Troubleshooting 表、
      全部链接与 What's next 逐项一致
- [x] docs/reference/access-authn-authz/mutating-admission-policy/ — 2026-09-17 — 通过;
      Feature state(Stable gate:MutatingAdmissionPolicy v1.36 含 More information)、
      三资源模型、ApplyConfiguration/JSONPatch 两示例 YAML、CEL 变量与类型列表 ×2、
      escapeKey 说明、豁免 API kinds 两组列表、NOTE 碎片化(模式⑤)文字无缺失、
      全部链接逐项一致
- [x] docs/reference/access-authn-authz/psp-to-pod-security-standards/ — 2026-09-17 — 通过;
      PSP Spec 映射表(26 行)与 annotations 表(4 行)、Baseline/Restricted/Privileged 与
      migrate-from-psp 链接逐项一致
- [x] docs/reference/access-authn-authz/rbac/ — 2026-09-17 — 通过;43 个标题与 HTML 一致
      (差额为 YAML 注释行误计);API objects/Role 与 ClusterRole/RoleBinding 与
      ClusterRoleBinding 示例 YAML ×6、Referring to resources(subresource/resourceNames/
      wildcard)、subjects 示例 9 组、Default roles 与 auto-reconciliation、API discovery roles、
      User-facing roles 大表、Privilege escalation prevention、Restrictions on role binding、
      CLI 5 小节、SA permissions、EndpointSlices write、ABAC 升级与 Parallel authorizers、
      Permissive RBAC 各节;全部链接(3 级 ../../../concepts 深度正确)逐项一致
- [x] docs/reference/access-authn-authz/service-accounts-admin/ — 2026-09-17 — 通过(附注):
      已知⑥:Verifying private claims 与 Create additional tokens 等节多个步骤的 ```shell/
      ```yaml 围栏接在步骤文字行尾(65-78、108-117 行等),文字无缺失;
      其余一致:Bound tokens(含 webhook-bound Alpha gate:APIServerWebhookAuthenticationToken
      v1.37)、PodNodeInfo(Stable gate v1.32)、TokenReview 流程、JWT schema、
      projected volume 三源说明 ×2、legacy token 清理、ExternalServiceAccountTokenSigner
      (Stable gate v1.36)与 3 个 proto 块、CAUTION ×3、全部链接与 What's next 逐项一致
- [x] docs/reference/access-authn-authz/user-impersonation/ — 2026-09-17 — 通过(附注):
      57 行 "…Impersonate-Group` header，set the…" 处含全角逗号"，"为上游固有;
      其余一致:impersonation 流程与 4 种 header、示例 http/bash 块、3 个 RBAC YAML、
      NOTE 碎片化(模式⑤)、Constrained impersonation(Stable gate:ConstrainedImpersonation
      Beta v1.36)3 种模式与 verb 列表、示例 YAML ×5、Auditing/Metrics 各节、
      全部链接与 What's next 逐项一致
- [x] docs/reference/access-authn-authz/validating-admission-policy/ — 2026-09-17 — 通过;
      三资源模型、basic 示例 policy/binding、validationActions 3 种、Parameter resources
      全示例链(policy/binding/param/prod binding/param)、Optional parameters、
      Per-namespace/Parameter selector/Authorization checks/paramRef 含 parameterNotFoundAction、
      failurePolicy、Validation Expression 与示例表(13 行)、matchConditions、
      Audit annotations、Message expression、Type checking(限制 4 条)、Variable composition
      完整示例、豁免 kinds 两组、NOTE 碎片化(模式⑤)文字无缺失、全部链接与代码样本逐项一致
- [x] docs/reference/command-line-tools-reference/ — 2026-09-17 — 通过;纯 section-index 页,
      6 个条目(Feature Gates/removed/kube-apiserver/kube-controller-manager/kube-proxy/
      kube-scheduler/kubelet)链接与顺序一致
- [x] docs/reference/command-line-tools-reference/feature-gates-removed/ — 2026-09-17 — 通过;
      3 个标题一致;已移除 feature gate 表 612 数据行与 HTML 613 个 <tr>(含表头)完全对应;
      Descriptions for removed feature gates 节 230 个粗体词条及链接逐项一致
- [x] docs/reference/command-line-tools-reference/feature-gates/ — 2026-09-17 — 通过;
      10 个标题一致;Alpha/Beta 与 Graduated/Deprecated 两张表数据行 526 行,
      加 2 个表头与 HTML 528 个 <tr> 完全对应;How to enable/Feature stages/
      Listing a feature gate 各节与全部链接逐项一致
- [x] docs/reference/command-line-tools-reference/kube-apiserver/ — 2026-09-17 — 通过;
      自动生成页,Synopsis/Options 两节,168 个 flag 条目与 HTML 完全一一对应,
      页尾自动生成说明一致
- [x] docs/reference/command-line-tools-reference/kube-controller-manager/ — 2026-09-17 —
      通过;自动生成页,144 个 flag 条目与 HTML 一一对应
- [x] docs/reference/command-line-tools-reference/kube-proxy/ — 2026-09-17 — 通过;
      自动生成页,64 个 flag 条目与 HTML 一一对应
- [x] docs/reference/command-line-tools-reference/kube-scheduler/ — 2026-09-17 — 通过;
      自动生成页,58 个 flag 条目与 HTML 一一对应
- [x] docs/reference/command-line-tools-reference/kubelet/ — 2026-09-17 — 通过;
      自动生成页,126 个 flag 条目与 HTML 一一对应
- [x] docs/reference/config-api/ — 2026-09-17 — 通过;纯 section-index 页,23 个条目
      (Client Authentication 至 WebhookAdmission Configuration)链接与顺序一致
- [x] docs/reference/config-api/apiserver-admission.v1/ — 2026-09-17 — 通过;genref 生成页,
      Resource Types 列表 + 6 个类型小节(AdmissionReview/Request/Response/Operation/PatchType)、
      字段表 26 行、Appears in 交叉链接与 pkg.go.dev 外链逐项一致
- [x] docs/reference/config-api/apiserver-audit.v1/ — 2026-09-17 — 通过;genref 生成页,
      11 个类型小节与 HTML(除 feedback)一一对应、Appears in 8 组、字段表逐项一致
- [x] docs/reference/config-api/apiserver-config.v1/ — 2026-09-17 — 通过;genref 生成页,
      31 个类型小节、Appears in 25 组、字段表逐项一致
- [x] docs/reference/config-api/apiserver-config.v1alpha1/ — 2026-09-17 — 通过;genref 生成页,
      31 个类型小节、Appears in 25 组、字段表逐项一致
- [x] docs/reference/config-api/apiserver-config.v1beta1/ — 2026-09-17 — 通过;genref 生成页,
      30 个类型小节、Appears in 24 组、字段表逐项一致
- [x] docs/reference/config-api/apiserver-eventratelimit.v1alpha1/ — 2026-09-17 — 通过;
      genref 生成页,4 个类型小节、字段表逐项一致
- [x] docs/reference/config-api/apiserver-webhookadmission.v1/ — 2026-09-17 — 通过;
      genref 生成页,单个类型小节(WebhookAdmissionConfiguration)与字段表逐项一致
- [x] docs/reference/config-api/client-authentication.v1/ — 2026-09-17 — 通过;genref 生成页,
      5 个类型小节、Appears in 3 组、字段表逐项一致
- [x] docs/reference/config-api/client-authentication.v1beta1/ — 2026-09-17 — 通过;
      genref 生成页,5 个类型小节、Appears in 3 组、字段表逐项一致
- [x] docs/reference/config-api/imagepolicy.v1alpha1/ — 2026-09-17 — 通过;genref 生成页,
      5 个类型小节、Appears in 3 组、字段表逐项一致
- [x] docs/reference/config-api/kube-controller-manager-config.v1alpha1/ — 2026-09-17 — 通过;
      genref 生成页,47 个类型小节、字段表逐项一致
- [x] docs/reference/config-api/kube-proxy-config.v1alpha1/ — 2026-09-17 — 通过;
      genref 生成页,23 个类型小节、字段表逐项一致
- [x] docs/reference/config-api/kube-scheduler-config.v1/ — 2026-09-17 — 通过;
      genref 生成页,27 个类型小节、字段表逐项一致
- [x] docs/reference/config-api/kubeadm-config.v1beta3/ — 2026-09-17 — 通过;
      genref 生成页,标题数与 HTML 一致(含 id-less h2),字段表逐项一致
- [x] docs/reference/config-api/kubeadm-config.v1beta4/ — 2026-09-17 — 通过;
      genref 生成页,39 个内容 h2 与 HTML 一致(HTML 另有 feedback h2),
      Init/Join/Reset/Upgrade 配置类型与字段表逐项一致
- [x] docs/reference/config-api/kubeconfig.v1/ — 2026-09-17 — 通过;genref 生成页,
      14 个类型小节、字段表逐项一致
- [x] docs/reference/config-api/kubelet-config.v1/ — 2026-09-17 — 通过;genref 生成页,
      6 个类型小节、字段表逐项一致
- [x] docs/reference/config-api/kubelet-config.v1alpha1/ — 2026-09-17 — 通过;genref 生成页,
      9 个类型小节、字段表逐项一致
- [x] docs/reference/config-api/kubelet-config.v1beta1/ — 2026-09-17 — 通过;genref 生成页,
      37 个类型小节、字段表逐项一致
- [x] docs/reference/config-api/kubelet-credentialprovider.v1/ — 2026-09-17 — 通过;
      genref 生成页,5 个类型小节、字段表逐项一致
- [x] docs/reference/config-api/kuberc.v1alpha1/ — 2026-09-17 — 通过;genref 生成页,
      5 个类型小节、字段表逐项一致
- [x] docs/reference/config-api/kuberc.v1beta1/ — 2026-09-17 — 通过;genref 生成页,
      7 个类型小节、字段表逐项一致
- [x] docs/reference/debug-cluster/ — 2026-09-17 — 通过;纯 section-index 页,
      1 个条目(Flow control)链接一致
- [x] docs/reference/debug-cluster/flow-control/ — 2026-09-17 — 通过;
      9 个代码块(5 shell+4 none)、5 个 h2、3 个内链/外链全部一致;
      列表项内代码块呈 ⑥(围栏粘连)现象,文本无丢失
- [x] docs/reference/deprecation-policy/ — 2026-09-17 — 问题:
      HTML "This ensures beta API support covers the maximum supported version skew of 2 releases" 中
      "maximum supported version skew of 2 releases" 为指向 /releases/version-skew-policy/ 的链接,
      md 中该短语退化为纯文本无链接(md 79行附近);
      另:规则#4a 后 NOTE 含 #52185 链接呈 ⑤ 碎片化;promql 代码块与列表第3项粘连(⑥);
      上游原样保留:`ALPHA`, `BETA` `STABLE` 缺逗号、"adopt and transitions"、2 表(3+16行)完整
- [x] docs/reference/encodings/ — 2026-09-17 — 通过;HTML 正文仅有标题无内容,md 一致
- [x] docs/reference/encodings/kyaml/ — 2026-09-17 — 通过;
      1 h2+1 h3+1 段+1 段+1 yaml 代码块逐字一致(该页上游即短)
- [x] docs/reference/external-api/ — 2026-09-17 — 通过;4 个 h5 条目链接一致
- [x] docs/reference/external-api/custom-metrics.v1beta2/ — 2026-09-17 — 通过;
      4 类型各 1 表(4/7/4/2 数据行)、2 处 Appears in、外链 4(v1.37)+1(pkg.go.dev)、锚点一致
- [x] docs/reference/external-api/external-metrics.v1beta1/ — 2026-09-17 — 通过;
      2 表(7/4 数据行)、1 处 Appears in、外链 v1.37×2+pkg.go.dev×1 一致
- [x] docs/reference/external-api/metrics.v1/ — 2026-09-17 — 通过;
      5 类型 5 表(6/4/6/4/2 数据行)、3 处 Appears in、v1.37×8+pkg.go.dev×2 一致
- [x] docs/reference/external-api/metrics.v1beta1/ — 2026-09-17 — 通过;
      与 v1 同构:5 表 27 行、3 处 Appears in、外链一致
- [x] docs/reference/glossary/ — 2026-09-17 — 轻微问题+1处链接丢失;逐词比对162/162词条:
      词条名、#term- 锚点(含3个大小写混合锚 CustomResourceDefinition/Extensions/HostAliases)、
      顺序、简介与完整定义文本全部一致;HTML 列表/<li> 在 md 中以 - 列表项保留;
      3 处定义内 Note 在 md 中为 [!NOTE] 引用块(文字完整,⑤ 近亲但无损);
      12 个筛选标签、Select all/Deselect all、[+] 提示文字均在;
      问题1:12 个带 aka 的词条(如 DRA/HPA/PDB/API server等)HTML 显示 "Also known as: X",
      md 丢弃 "Also known as:" 标签仅保留 X 粘在简介前;
      问题2:Dockershim 词条 "see Dockershim FAQ" 在 HTML 中为 /dockershim 链接,
      md 422行退化为纯文本;
      备注:API server 词条正文 kube-apiserver 链接被改指向 command-line-tools-reference/kube-apiserver
      (HTML 原目标 /docs/reference/generated/kube-apiserver/ 在本 pinned 树中不存在,视为合理修复);
      CEL/DRA 系与 CRI 系链接改用新路径(经 _redirects 610/503 等价)
- [x] docs/reference/instrumentation/ — 2026-09-17 — 通过;7 个 h5 条目及描述文字、链接一致
- [x] docs/reference/instrumentation/cri-pod-container-metrics/ — 2026-09-17 — 通过;
      1 feature-state、5 h2+2 h3、2 NOTE(其一为 ⑤ 碎片化但文字完整)、
      链接(feature-gates×2/kubelet/stats.v1alpha1/containers-cri/configure-feature-gates/cadvisor)一致
- [x] docs/reference/instrumentation/metrics/ — 2026-09-17 — 严重问题:
      全部 605 个指标丢失名称(metric_name)与帮助文本(metric_help),
      md 中每个指标仅剩 Stability Level/Type/Labels/Components 元数据
      (计数 605/447/605/8 与 HTML 完全一致、顺序一致);
      且 Labels 值连写无分隔(HTML 为独立 span:name/operation/rejected/type → md "nameoperationrejectedtype");
      页面 lead("Details of the metric data that Kubernetes components export.",④)亦丢失;
      3 个 h3(stable/beta/alpha)保留
- [x] docs/reference/instrumentation/native-histograms/ — 2026-09-17 — 通过;
      1 feature-state、8 h2+2 h3、6 代码块、1 表(5 数据行)全部一致;
      NOTE/CAUTION 呈 ⑤ 碎片化但文字完整;promql/bash 与列表项粘连(⑥,已知);
      外链(prometheus 规范/exposition formats)与 ../metrics/ 内链一致
- [x] docs/reference/instrumentation/node-metrics/ — 2026-09-17 — 通过;
      2 shell 代码块、1 feature-state(locked)、NOTE(⑤ 碎片化,文字完整)、
      内外链接(stats.v1alpha1/kernel-version-requirements/cgroups/system-metrics#psi-metrics 等)一致
- [x] docs/reference/instrumentation/slis/ — 2026-09-17 — 通过;
      2 个 metrics 代码块逐字一致、2 h2、斜体导语(feature-gates-removed 链接)一致
- [x] docs/reference/instrumentation/understand-psi-metrics/ — 2026-09-17 — 通过;
      1 feature-state(locked)、13 代码块、标题层级(h2/h3/h4 ×12)逐级一致;
      PSI 解释文本(avg/spike/total 微秒)与 3 个示例场景(CPU/Memory/IO)yaml+shell 全部一致
- [x] docs/reference/instrumentation/zpages/ — 2026-09-17 — 通过;
      3 feature-state、8 代码块、自引用嵌套端点列表(上游原样)、状态/flag 结构化 go schema 一致;
      code 块中 "&#43;" 为 HTML 渲染原文(pinned 页面亦显示 &#43; 字面),非转义错误;
      4 处 NOTE(⑤ 碎片化)文字完整
- [x] docs/reference/issues-security/ — 2026-09-17 — 通过;3 个 h5 条目链接一致
- [x] docs/reference/issues-security/issues/ — 2026-09-17 — 通过;
      6 个链接(security 披露流程/github issues/CVE 列表/委员会/announce 列表)一致
- [x] docs/reference/issues-security/official-cve-feed/ — 2026-09-17 — 通过;
      tabset(JSON/RSS feed)按惯例展平为列表+"Panel:"标注;CVE 表 91 数据行与 HTML 92 tr 精确一致;
      index.json/feed.xml 两资产已随页复制到 documents/ 树
- [x] docs/reference/issues-security/security/ — 2026-09-17 — 通过;
      6 h2/h3 结构一致、12 段关键文字完整、6 个外链一致
- [x] docs/reference/kubectl/ — 2026-09-17 — 通过;
      8 h2+2 h3+4 h4、32 代码块、3 表(45/57/10 数据行)精确一致;
      链接集一致;备注:首段 "control plane" 链接 md 多了 #term-control-plane 锚
      (HTML 仅指向 /docs/reference/glossary/,目标页相同,锚有效)
- [x] docs/reference/kubectl/conventions/ — 2026-09-17 — 通过;
      3 h2+3 h3、"Kubernetes Configuration Good Practices" 在 HTML 中本无链接(上游原样)、
      CAUTION 完整、Kubectl Book 外链一致
- [x] docs/reference/kubectl/docker-cli-to-kubectl/ — 2026-09-17 — 问题:
      9 个命令链接目标被改写且锚点悬空——HTML 指向
      /docs/reference/generated/kubectl/kubectl-commands/#attach|#get|#run|#logs|#delete|#exec|#version|#cluster-info
      及 kubectl-commands#-em-deployment-em-;md 改为 docs/reference/kubectl#attach 等同页锚,
      而 kubectl index 页HTML 无 get/run/attach/logs/delete/exec/version/cluster-info/
      -em-deployment-em- 任一 id(md 24/86/103/137/173/248/266/287/44 行附近);
      其余:10 h2、58 代码块逐字一致、NOTE×2、deployment/service/logging 等内链一致
- [x] docs/reference/kubectl/generated/ — 2026-09-17 — 通过;
      44 个命令卡片(含子命令列表)链接目标集与 HTML 完全一致(153 个内链去重后一一对应)
- [x] docs/reference/kubectl/generated/kubectl/ — 2026-09-17 — 通过;
      Synopsis+Options(36 个 flag,HTML 108 td=36×3 精确一致)+See Also 44 条链接一致
- [x] docs/reference/kubectl/generated/kubectl_annotate/ — 2026-09-17 — 通过;
      Options 18 flag+Parent 35 flag(HTML 53 个 colspan 行精确一致)、2 代码块、See Also 1 条
- [x] docs/reference/kubectl/generated/kubectl_api-resources/ — 2026-09-17 — 通过;
      45 flag 一致、2 代码块、See Also 1 条
- [x] docs/reference/kubectl/generated/kubectl_api-versions/ — 2026-09-17 — 通过;
      36 flag 一致、2 代码块、See Also 1 条
- [x] docs/reference/kubectl/generated/kubectl_apply/ — 2026-09-17 — 通过;
      60 flag 一致、2 代码块、See Also 4 条(3 子命令+kubectl)文本一致
- [x] docs/reference/kubectl/generated/kubectl_apply/kubectl_apply_edit-last-applied/ — 2026-09-17 — 通过;
      46 flag、2 代码块、synopsis 一致
- [x] docs/reference/kubectl/generated/kubectl_apply/kubectl_apply_set-last-applied/ — 2026-09-17 — 通过;
      43 flag、2 代码块、synopsis 一致
- [x] docs/reference/kubectl/generated/kubectl_apply/kubectl_apply_view-last-applied/ — 2026-09-17 — 通过;
      42 flag、2 代码块、synopsis 一致
- [x] docs/reference/kubectl/generated/kubectl_attach/ — 2026-09-17 — 通过;42 flag、2 代码块、synopsis/flag 描述一致
- [x] docs/reference/kubectl/generated/kubectl_auth/ — 2026-09-17 — 通过;36 flag、1 代码块、See Also 3 子命令
- [x] docs/reference/kubectl/generated/kubectl_auth/kubectl_auth_can-i/ — 2026-09-17 — 通过;41 flag、2 代码块
- [x] docs/reference/kubectl/generated/kubectl_auth/kubectl_auth_reconcile/ — 2026-09-17 — 通过;46 flag、2 代码块
- [x] docs/reference/kubectl/generated/kubectl_auth/kubectl_auth_whoami/ — 2026-09-17 — 通过;40 flag、3 代码块
- [x] docs/reference/kubectl/generated/kubectl_autoscale/ — 2026-09-17 — 通过;51 flag、2 代码块
- [x] docs/reference/kubectl/generated/kubectl_certificate/ — 2026-09-17 — 通过;36 flag、1 代码块、See Also 2 子命令
- [x] docs/reference/kubectl/generated/kubectl_certificate/kubectl_certificate_approve/ — 2026-09-17 — 通过;44 flag、2 代码块
- [x] docs/reference/kubectl/generated/kubectl_certificate/kubectl_certificate_deny/ — 2026-09-17 — 通过;44 flag、2 代码块
- [x] docs/reference/kubectl/generated/kubectl_cluster-info/ — 2026-09-17 — 通过;36 flag、2 代码块、See Also 2 条(dump 子命令)
- [x] docs/reference/kubectl/generated/kubectl_cluster-info/kubectl_cluster-info_dump/ — 2026-09-17 — 通过;44 flag、2 代码块
- [x] docs/reference/kubectl/generated/kubectl_completion/ — 2026-09-17 — 通过;36 flag、3 代码块
- [x] docs/reference/kubectl/generated/kubectl_config/ — 2026-09-17 — 通过;36 flag、1 代码块、See Also 15 子命令
- [x] docs/reference/kubectl/generated/kubectl_config/kubectl_config_current-context/ — 2026-09-17 — 通过;36 flag、2 代码块
- [x] docs/reference/kubectl/generated/kubectl_config/kubectl_config_delete-cluster/ — 2026-09-17 — 通过;36 flag、2 代码块
- [x] docs/reference/kubectl/generated/kubectl_config/kubectl_config_delete-context/ — 2026-09-17 — 通过;36 flag、2 代码块
- [x] docs/reference/kubectl/generated/kubectl_config/kubectl_config_delete-user/ — 2026-09-17 — 通过;36 flag、2 代码块
- [x] docs/reference/kubectl/generated/kubectl_config/kubectl_config_get-clusters/ — 2026-09-17 — 通过;36 flag、2 代码块
- [x] docs/reference/kubectl/generated/kubectl_config/kubectl_config_get-contexts/ — 2026-09-17 — 通过;38 flag、2 代码块
- [x] docs/reference/kubectl/generated/kubectl_config/kubectl_config_get-users/ — 2026-09-17 — 通过;36 flag、2 代码块
- [x] docs/reference/kubectl/generated/kubectl_config/kubectl_config_rename-context/ — 2026-09-17 — 通过;36 flag、2 代码块
- [x] docs/reference/kubectl/generated/kubectl_config/kubectl_config_set-cluster/ — 2026-09-17 — 通过;37 flag、2 代码块
- [x] docs/reference/kubectl/generated/kubectl_config/kubectl_config_set-context/ — 2026-09-17 — 通过;37 flag、2 代码块
- [x] docs/reference/kubectl/generated/kubectl_config/kubectl_config_set-credentials/ — 2026-09-17 — 通过;45 flag、3 代码块
- [x] docs/reference/kubectl/generated/kubectl_config/kubectl_config_set/ — 2026-09-17 — 通过;37 flag、2 代码块
- [x] docs/reference/kubectl/generated/kubectl_config/kubectl_config_unset/ — 2026-09-17 — 通过;36 flag、2 代码块
- [x] docs/reference/kubectl/generated/kubectl_config/kubectl_config_use-context/ — 2026-09-17 — 通过;36 flag、2 代码块
- [x] docs/reference/kubectl/generated/kubectl_config/kubectl_config_view/ — 2026-09-17 — 通过;44 flag、2 代码块
- [x] docs/reference/kubectl/generated/kubectl_cordon/ — 2026-09-17 — 通过;38 flag、2 代码块
- [x] docs/reference/kubectl/generated/kubectl_cp/ — 2026-09-17 — 通过;39 flag、2 代码块
- [x] docs/reference/kubectl/generated/kubectl_create/ — 2026-09-17 — 通过;51 flag、2 代码块、See Also 17 子命令
- [x] docs/reference/kubectl/generated/kubectl_create/kubectl_create_clusterrole/ — 2026-09-17 — 通过;49 flag、2 代码块
- [x] docs/reference/kubectl/generated/kubectl_create/kubectl_create_clusterrolebinding/ — 2026-09-17 — 通过;47 flag、2 代码块
- [x] docs/reference/kubectl/generated/kubectl_create/kubectl_create_configmap/ — 2026-09-17 — 通过;48 flag、2 代码块
- [x] docs/reference/kubectl/generated/kubectl_create/kubectl_create_cronjob/ — 2026-09-17 — 通过;47 flag、2 代码块
- [x] docs/reference/kubectl/generated/kubectl_create/kubectl_create_deployment/ — 2026-09-17 — 通过;47 flag、2 代码块
- [x] docs/reference/kubectl/generated/kubectl_create/kubectl_create_ingress/ — 2026-09-17 — 通过;48 flag、2 代码块
- [x] docs/reference/kubectl/generated/kubectl_create/kubectl_create_job/ — 2026-09-17 — 通过;46 flag、2 代码块
- [x] docs/reference/kubectl/generated/kubectl_create/kubectl_create_namespace/ — 2026-09-17 — 通过;44 flag、2 代码块
- [x] docs/reference/kubectl/generated/kubectl_create/kubectl_create_poddisruptionbudget/ — 2026-09-17 — 通过;47 flag、2 代码块
- [x] docs/reference/kubectl/generated/kubectl_create/kubectl_create_priorityclass/ — 2026-09-17 — 通过;48 flag、2 代码块
- [x] docs/reference/kubectl/generated/kubectl_create/kubectl_create_quota/ — 2026-09-17 — 通过;46 flag、2 代码块
- [x] docs/reference/kubectl/generated/kubectl_create/kubectl_create_role/ — 2026-09-17 — 通过;47 flag、2 代码块
- [x] docs/reference/kubectl/generated/kubectl_create/kubectl_create_rolebinding/ — 2026-09-17 — 通过;48 flag、2 代码块
- [x] docs/reference/kubectl/generated/kubectl_create/kubectl_create_secret/ — 2026-09-17 — 通过;36 flag、1 代码块、See Also 3 子命令
- [x] docs/reference/kubectl/generated/kubectl_create/kubectl_create_secret_docker-registry/ — 2026-09-17 — 通过;50 flag、4 代码块
- [x] docs/reference/kubectl/generated/kubectl_create/kubectl_create_secret_generic/ — 2026-09-17 — 通过;49 flag、2 代码块
- [x] docs/reference/kubectl/generated/kubectl_create/kubectl_create_secret_tls/ — 2026-09-17 — 通过;47 flag、2 代码块
- [x] docs/reference/kubectl/generated/kubectl_create/kubectl_create_service/ — 2026-09-17 — 通过;36 flag、1 代码块、See Also 4 子命令
- [x] docs/reference/kubectl/generated/kubectl_create/kubectl_create_service_clusterip/ — 2026-09-17 — 通过;46 flag、2 代码块
- [x] docs/reference/kubectl/generated/kubectl_create/kubectl_create_service_externalname/ — 2026-09-17 — 通过;46 flag、2 代码块
- [x] docs/reference/kubectl/generated/kubectl_create/kubectl_create_service_loadbalancer/ — 2026-09-17 — 通过;45 flag、2 代码块
- [x] docs/reference/kubectl/generated/kubectl_create/kubectl_create_service_nodeport/ — 2026-09-17 — 通过;46 flag、2 代码块
- [x] docs/reference/kubectl/generated/kubectl_create/kubectl_create_serviceaccount/ — 2026-09-17 — 通过;44 flag、2 代码块
- [x] docs/reference/kubectl/generated/kubectl_create/kubectl_create_token/ — 2026-09-17 — 通过;45 flag、2 代码块
- [x] docs/reference/kubectl/generated/kubectl_debug/ — 2026-09-17 — 通过;59 flag、2 代码块
- [x] docs/reference/kubectl/generated/kubectl_delete/ — 2026-09-17 — 通过;54 flag、2 代码块
- [x] docs/reference/kubectl/generated/kubectl_describe/ — 2026-09-17 — 通过;43 flag、3 代码块
- [x] docs/reference/kubectl/generated/kubectl_diff/ — 2026-09-17 — 通过;48 flag、2 代码块
- [x] docs/reference/kubectl/generated/kubectl_drain/ — 2026-09-17 — 通过;47 flag、2 代码块
- [x] docs/reference/kubectl/generated/kubectl_edit/ — 2026-09-17 — 通过;49 flag、2 代码块
- [x] docs/reference/kubectl/generated/kubectl_events/ — 2026-09-17 — 通过;46 flag、2 代码块
- [x] docs/reference/kubectl/generated/kubectl_exec/ — 2026-09-17 — 通过;42 flag、2 代码块
- [x] docs/reference/kubectl/generated/kubectl_explain/ — 2026-09-17 — 通过;40 flag、3 代码块
- [x] docs/reference/kubectl/generated/kubectl_expose/ — 2026-09-17 — 通过;59 flag、2 代码块
- [x] docs/reference/kubectl/generated/kubectl_get/ — 2026-09-17 — 通过;59 flag、2 代码块
- [x] docs/reference/kubectl/generated/kubectl_kuberc/ — 2026-09-17 — 通过;36 flag、2 代码块、See Also 2 子命令
- [x] docs/reference/kubectl/generated/kubectl_kuberc/kubectl_kuberc_set/ — 2026-09-17 — 通过;45 flag、2 代码块
- [x] docs/reference/kubectl/generated/kubectl_kuberc/kubectl_kuberc_view/ — 2026-09-17 — 通过;40 flag、2 代码块
- [x] docs/reference/kubectl/generated/kubectl_kustomize/ — 2026-09-17 — 通过;49 flag、2 代码块
- [x] docs/reference/kubectl/generated/kubectl_label/ — 2026-09-17 — 通过;53 flag、2 代码块
- [x] docs/reference/kubectl/generated/kubectl_logs/ — 2026-09-17 — 通过;52 flag、2 代码块
- [x] docs/reference/kubectl/generated/kubectl_options/ — 2026-09-17 — 通过;36 flag、2 代码块
- [x] docs/reference/kubectl/generated/kubectl_patch/ — 2026-09-17 — 通过;50 flag、2 代码块
- [x] docs/reference/kubectl/generated/kubectl_plugin/ — 2026-09-17 — 通过;36 flag、2 代码块、See Also 1 子命令
- [x] docs/reference/kubectl/generated/kubectl_plugin/kubectl_plugin_list/ — 2026-09-17 — 通过;37 flag、2 代码块
- [x] docs/reference/kubectl/generated/kubectl_port-forward/ — 2026-09-17 — 通过;38 flag、2 代码块
- [x] docs/reference/kubectl/generated/kubectl_proxy/ — 2026-09-17 — 通过;49 flag、2 代码块
- [x] docs/reference/kubectl/generated/kubectl_replace/ — 2026-09-17 — 通过;54 flag、3 代码块
- [x] docs/reference/kubectl/generated/kubectl_rollout/ — 2026-09-17 — 通过;36 flag、2 代码块、See Also 6 子命令
- [x] docs/reference/kubectl/generated/kubectl_rollout/kubectl_rollout_history/ — 2026-09-17 — 通过;45 flag、2 代码块
- [x] docs/reference/kubectl/generated/kubectl_rollout/kubectl_rollout_pause/ — 2026-09-17 — 通过;45 flag、2 代码块
- [x] docs/reference/kubectl/generated/kubectl_rollout/kubectl_rollout_restart/ — 2026-09-17 — 通过;45 flag、3 代码块
- [x] docs/reference/kubectl/generated/kubectl_rollout/kubectl_rollout_resume/ — 2026-09-17 — 通过;45 flag、2 代码块
- [x] docs/reference/kubectl/generated/kubectl_rollout/kubectl_rollout_status/ — 2026-09-17 — 通过;43 flag、2 代码块
- [x] docs/reference/kubectl/generated/kubectl_rollout/kubectl_rollout_undo/ — 2026-09-17 — 通过;46 flag、2 代码块
- [x] docs/reference/kubectl/generated/kubectl_run/ — 2026-09-17 — 通过;70 flag、2 代码块
- [x] docs/reference/kubectl/generated/kubectl_scale/ — 2026-09-17 — 通过;50 flag、2 代码块
- [x] docs/reference/kubectl/generated/kubectl_set/ — 2026-09-17 — 通过;36 flag、1 代码块、See Also 6 子命令
- [x] docs/reference/kubectl/generated/kubectl_set/kubectl_set_env/ — 2026-09-17 — 通过;56 flag、3 代码块
- [x] docs/reference/kubectl/generated/kubectl_set/kubectl_set_image/ — 2026-09-17 — 通过;48 flag、3 代码块
- [x] docs/reference/kubectl/generated/kubectl_set/kubectl_set_resources/ — 2026-09-17 — 通过;51 flag、2 代码块
- [x] docs/reference/kubectl/generated/kubectl_set/kubectl_set_selector/ — 2026-09-17 — 通过;47 flag、2 代码块
- [x] docs/reference/kubectl/generated/kubectl_set/kubectl_set_serviceaccount/ — 2026-09-17 — 通过;47 flag、2 代码块
- [x] docs/reference/kubectl/generated/kubectl_set/kubectl_set_subject/ — 2026-09-17 — 通过;50 flag、2 代码块
- [x] docs/reference/kubectl/generated/kubectl_taint/ — 2026-09-17 — 通过;46 flag、2 代码块
- [x] docs/reference/kubectl/generated/kubectl_top/ — 2026-09-17 — 通过;36 flag、1 代码块、See Also 2 子命令
- [x] docs/reference/kubectl/generated/kubectl_top/kubectl_top_node/ — 2026-09-17 — 通过;42 flag、2 代码块
- [x] docs/reference/kubectl/generated/kubectl_top/kubectl_top_pod/ — 2026-09-17 — 通过;45 flag、2 代码块
- [x] docs/reference/kubectl/generated/kubectl_uncordon/ — 2026-09-17 — 通过;38 flag、2 代码块
- [x] docs/reference/kubectl/generated/kubectl_version/ — 2026-09-17 — 通过;38 flag、2 代码块
- [x] docs/reference/kubectl/generated/kubectl_wait/ — 2026-09-17 — 通过;49 flag、2 代码块
- [x] docs/reference/kubectl/introduction/ — 2026-09-17 — 通过;
      6 h2、1 表(5 数据行)、TIP/WARNING 完整、无内容外链(与 HTML 一致)
- [x] docs/reference/kubectl/jsonpath/ — 2026-09-17 — 通过;
      3 h2、5 代码块、函数表 11 数据行精确一致;HTML 原文含字面 \( \ge 0 \) 双反斜杠,
      md 原样保留(上游 quirk);NOTE 内嵌 cmd 代码块一致
- [x] docs/reference/kubectl/kubectl-cmds/ — 2026-09-17 — 通过;单段+1 链接指向 generated/kubectl/,一致
- [x] docs/reference/kubectl/kubectl/ — 2026-09-17 — 问题:
      Synopsis 正文丢失——HTML 为 "Command line tool for communicating with a Kubernetes cluster's
      control plane, using the Kubernetes API.",md 仅剩孤立链接片段
      [control plane](../../glossary/#term-control-plane)(md 5行,首句与尾句文字全部丢失,①型);
      其余通过:Options 43 flag、Env vars 6 项、See Also 43 条、1 代码块均一致
- [x] docs/reference/kubectl/kuberc/ — 2026-09-17 — 通过;
      5 h2+6 h3、2 feature-state(Beta v1.34/v1.35)、12 代码块、aliases/defaults/凭据插件策略文本一致
- [x] docs/reference/kubectl/quick-reference/ — 2026-09-17 — 通过;
      15 h2+7 h3、20 代码块、NOTE/内链(configure-access-multiple-clusters)一致
- [x] docs/reference/kubelet-api/ — 2026-09-17 — 通过;单个 h5 条目链接一致
- [x] docs/reference/kubelet-api/stats.v1alpha1/ — 2026-09-17 — 通过;
      23 h2、21 表、22 处 Appears in、v1.37 外链×11 全部一致(97 数据行核对一致)
- [x] docs/reference/kubernetes-api/ — 2026-09-17 — 通过;
      导语 2 段完整、23 个 API 组 h5 条目及描述、链接一致
- [x] docs/reference/kubernetes-api/admissionregistration/ — 2026-09-17 — 通过;
      6 个 h5 资源类型条目、描述与顺序一致
- [x] docs/reference/kubernetes-api/admissionregistration/mutating-admission-policy-binding-v1/ — 2026-09-17 — 通过;
      4 h2+9 h3、31 表(106 数据行)精确一致、Operations 全套(post/patch/delete/get/list/watch)
- [x] docs/reference/kubernetes-api/admissionregistration/mutating-admission-policy-v1/ — 2026-09-17 — 通过;
      7 h2+9 h3、34 表(115 数据行)、正文与单元格抽样一致
- [x] docs/reference/kubernetes-api/admissionregistration/mutating-webhook-configuration-v1/ — 2026-09-17 — 通过;
      4 h2+9 h3、31 表(115 数据行)、抽样一致
- [x] docs/reference/kubernetes-api/admissionregistration/validating-admission-policy-binding-v1/ — 2026-09-17 — 通过;
      4 h2+9 h3、31 表(107 数据行)
- [x] docs/reference/kubernetes-api/admissionregistration/validating-admission-policy-v1/ — 2026-09-17 — 通过;
      9 h2+12 h3、47 表(143 数据行)
- [x] docs/reference/kubernetes-api/admissionregistration/validating-webhook-configuration-v1/ — 2026-09-17 — 通过;
      4 h2+9 h3、31 表(114 数据行)
- [x] docs/reference/kubernetes-api/apiextensions/ — 2026-09-17 — 通过;组索引,条目链接一致
- [x] docs/reference/kubernetes-api/apiextensions/custom-resource-definition-v1/ — 2026-09-17 — 通过;
      24 h2+12 h3、58 表(231 数据行);页首概念文档链接框(Custom Resources 等)一致
- [x] docs/reference/kubernetes-api/apiregistration/ — 2026-09-17 — 通过;组索引 1 条目(APIService)链接描述一致
- [x] docs/reference/kubernetes-api/apiregistration/api-service-v1/ — 2026-09-17 — 通过;
      7 h2+12 h3、45 表(140 数据行)
- [x] docs/reference/kubernetes-api/apiserverinternal/ — 2026-09-17 — 通过;组索引 1 条目(StorageVersion)一致
- [x] docs/reference/kubernetes-api/apiserverinternal/storage-version-v1alpha1/ — 2026-09-17 — 通过;
      7 h2+12 h3、44 表(137 数据行)
- [x] docs/reference/kubernetes-api/apps/ — 2026-09-17 — 通过;组索引 5 条目链接一致
- [x] docs/reference/kubernetes-api/apps/controller-revision-v1/ — 2026-09-17 — 通过;
      3 h2+11 h3、38 表(139 数据行)
- [x] docs/reference/kubernetes-api/apps/daemon-set-v1/ — 2026-09-17 — 轻微问题:
      7 h2+14 h3、53 表(186 数据行)一致,但页首概念文档框内编号列表第1项与段落粘连:
      "…or be part of an add-on.1. [Writing a DaemonSet Spec](…)"(md 6行,项2/3 正常成行),
      HTML 中为独立 <ol> 列表;文字无丢失
- [x] docs/reference/kubernetes-api/apps/deployment-v1/ — 2026-09-17 — 轻微问题:
      6 h2+17 h3、63 表(212 数据行)一致;概念文档框编号列表第1项粘连:
      "…doesn't maintain state.1. [Use Case](…)"(md 6行),文字无丢失
- [x] docs/reference/kubernetes-api/apps/replica-set-v1/ — 2026-09-17 — 轻微问题:
      6 h2+17 h3、63 表(201 数据行)一致;概念框同样粘连 "…automatically.1. [How a ReplicaSet works](…)"
- [x] docs/reference/kubernetes-api/apps/stateful-set-v1/ — 2026-09-17 — 通过;
      10 h2+17 h3、67 表(218 数据行)
- [x] docs/reference/kubernetes-api/autoscaling/ — 2026-09-17 — 通过;组索引 1 条目(HPA v2)一致
- [x] docs/reference/kubernetes-api/autoscaling/horizontal-pod-autoscaler-v2/ — 2026-09-17 — 通过;
      25 h2+14 h3、71 表(236 数据行)
- [x] docs/reference/kubernetes-api/batch/ — 2026-09-17 — 通过;组索引 2 条目(CronJob/Job)一致
- [x] docs/reference/kubernetes-api/batch/cron-job-v1/ — 2026-09-17 — 轻微问题:
      6 h2+14 h3、52 表(175 数据行)一致;概念框粘连 "…repeating schedule.1. [Example](…)"
- [x] docs/reference/kubernetes-api/batch/job-v1/ — 2026-09-17 — 轻微问题:
      22 h2+14 h3、65 表(223 数据行)一致;概念框粘连 "…then stop.1. [Running an example Job](…)"
- [x] docs/reference/kubernetes-api/certificates/ — 2026-09-17 — 通过;组索引 3 条目一致
- [x] docs/reference/kubernetes-api/certificates/certificate-signing-request-v1/ — 2026-09-17 — 通过;
      6 h2+12 h3、44 表(140 数据行)
- [x] docs/reference/kubernetes-api/certificates/cluster-trust-bundle-v1/ — 2026-09-17 — 通过;
      4 h2+9 h3、31 表(105 数据行)
- [x] docs/reference/kubernetes-api/certificates/pod-certificate-request-v1/ — 2026-09-17 — 通过;
      5 h2+14 h3、51 表(177 数据行)
- [x] docs/reference/kubernetes-api/coordination/ — 2026-09-17 — 通过;组索引 2 条目(Lease/LeaseCandidate)一致
- [x] docs/reference/kubernetes-api/coordination/lease-candidate-v1beta1/ — 2026-09-17 — 通过;
      4 h2+11 h3、39 表(144 数据行)
- [x] docs/reference/kubernetes-api/coordination/lease-v1/ — 2026-09-17 — 通过;
      4 h2+11 h3、39 表(145 数据行);概念框为纯列表(Node heartbeats 等链接),文字完整
- [x] docs/reference/kubernetes-api/core/ — 2026-09-17 — 通过;组索引 16 条目链接一致
- [x] docs/reference/kubernetes-api/core/component-status-v1/ — 2026-09-17 — 通过;4 h2+2 h3、8 表(28 数据行)
- [x] docs/reference/kubernetes-api/core/config-map-v1/ — 2026-09-17 — 通过;
      3 h2+11 h3、38 表(140 数据行);概念框列表项文字完整(ConfigMap object 等)
- [x] docs/reference/kubernetes-api/core/endpoints-v1/ — 2026-09-17 — 通过;6 h2+11 h3、41 表(149 数据行)
- [x] docs/reference/kubernetes-api/core/event-v1/ — 2026-09-17 — 通过;4 h2+11 h3、39 表(153 数据行)
- [x] docs/reference/kubernetes-api/core/limit-range-v1/ — 2026-09-17 — 通过;
      5 h2+11 h3、40 表(145 数据行);概念框列表文字完整
- [x] docs/reference/kubernetes-api/core/namespace-v1/ — 2026-09-17 — 通过;
      6 h2+11 h3、41 表(115 数据行);概念框列表文字完整(When to Use Multiple Namespaces 等)
- [x] docs/reference/kubernetes-api/core/node-v1/ — 2026-09-17 — 通过;21 h2+22 h3、89 表(228 数据行)
- [x] docs/reference/kubernetes-api/core/persistent-volume-claim-v1/ — 2026-09-17 — 通过;
      10 h2+14 h3、56 表(196 数据行)
- [x] docs/reference/kubernetes-api/core/persistent-volume-v1/ — 2026-09-17 — 通过;
      29 h2+12 h3、67 表(272 数据行)
- [x] docs/reference/kubernetes-api/core/pod-template-v1/ — 2026-09-17 — 通过;4 h2+11 h3、39 表(140 数据行)
- [x] docs/reference/kubernetes-api/core/pod-v1/ — 2026-09-17 — 通过;
      107 h2+38 h3、230 表(815 数据行)全部一致(大页)
- [x] docs/reference/kubernetes-api/core/replication-controller-v1/ — 2026-09-17 — 通过;6 h2+17 h3、63 表(200 数据行)
- [x] docs/reference/kubernetes-api/core/resource-quota-v1/ — 2026-09-17 — 通过;7 h2+14 h3、53 表(171 数据行)
- [x] docs/reference/kubernetes-api/core/secret-v1/ — 2026-09-17 — 通过;3 h2+11 h3、38 表(141 数据行)
- [x] docs/reference/kubernetes-api/core/service-account-v1/ — 2026-09-17 — 通过;3 h2+11 h3、38 表(140 数据行)
- [x] docs/reference/kubernetes-api/core/service-v1/ — 2026-09-17 — 通过;11 h2+24 h3、87 表(245 数据行)
- [x] docs/reference/kubernetes-api/discovery/ — 2026-09-17 — 通过;组索引 1 条目一致
- [x] docs/reference/kubernetes-api/discovery/endpoint-slice-v1/ — 2026-09-17 — 通过;9 h2+11 h3、44 表(159 数据行)
- [x] docs/reference/kubernetes-api/events/ — 2026-09-17 — 通过;组索引 1 条目一致
- [x] docs/reference/kubernetes-api/events/event-v1/ — 2026-09-17 — 通过;4 h2+11 h3、39 表(153 数据行)
- [x] docs/reference/kubernetes-api/flowcontrol/ — 2026-09-17 — 通过;组索引 2 条目一致
- [x] docs/reference/kubernetes-api/flowcontrol/flow-schema-v1/ — 2026-09-17 — 通过;15 h2+12 h3、53 表(154 数据行)
- [x] docs/reference/kubernetes-api/flowcontrol/priority-level-configuration-v1/ — 2026-09-17 — 通过;
      10 h2+12 h3、48 表(144 数据行)
- [x] docs/reference/kubernetes-api/group-versions/ — 2026-09-17 — 通过;1 表(24 组数据行)一致
- [x] docs/reference/kubernetes-api/lifecycle/ — 2026-09-17 — 通过;组索引 2 条目一致
- [x] docs/reference/kubernetes-api/lifecycle/eviction-request-v1alpha1/ — 2026-09-17 — 通过;
      7 h2+14 h3、53 表(170 数据行)
- [x] docs/reference/kubernetes-api/lifecycle/eviction-v1alpha1/ — 2026-09-17 — 通过;
      10 h2+14 h3、56 表(182 数据行)
- [x] docs/reference/kubernetes-api/networking/ — 2026-09-17 — 通过;组索引 5 条目一致
- [x] docs/reference/kubernetes-api/networking/ingress-class-v1/ — 2026-09-17 — 通过;
      5 h2+9 h3、32 表(110 数据行);概念框文字完整
- [x] docs/reference/kubernetes-api/networking/ingress-v1/ — 2026-09-17 — 通过;
      15 h2+14 h3、61 表(188 数据行);概念框文字完整
- [x] docs/reference/kubernetes-api/networking/ip-address-v1/ — 2026-09-17 — 通过;
      5 h2+9 h3、32 表(108 数据行)
- [x] docs/reference/kubernetes-api/networking/network-policy-v1/ — 2026-09-17 — 通过;
      9 h2+11 h3、44 表(154 数据行);概念框列表文字完整
- [x] docs/reference/kubernetes-api/networking/service-cidr-v1/ — 2026-09-17 — 通过;
      5 h2+12 h3、43 表(126 数据行)
- [x] docs/reference/kubernetes-api/node/ — 2026-09-17 — 通过;组索引 1 条目一致
- [x] docs/reference/kubernetes-api/node/runtime-class-v1/ — 2026-09-17 — 通过;5 h2+9 h3、32 表(108 数据行)
- [x] docs/reference/kubernetes-api/policy/ — 2026-09-17 — 通过;组索引 1 条目一致
- [x] docs/reference/kubernetes-api/policy/pod-disruption-budget-v1/ — 2026-09-17 — 通过;
      5 h2+14 h3、51 表(173 数据行)
- [x] docs/reference/kubernetes-api/rbac/ — 2026-09-17 — 通过;组索引 4 条目一致
- [x] docs/reference/kubernetes-api/rbac/cluster-role-binding-v1/ — 2026-09-17 — 通过;3 h2+9 h3、30 表(104 数据行)
- [x] docs/reference/kubernetes-api/rbac/cluster-role-v1/ — 2026-09-17 — 通过;4 h2+9 h3、31 表(105 数据行)
- [x] docs/reference/kubernetes-api/rbac/role-binding-v1/ — 2026-09-17 — 通过;3 h2+11 h3、38 表(139 数据行)
- [x] docs/reference/kubernetes-api/rbac/role-v1/ — 2026-09-17 — 通过;3 h2+11 h3、38 表(138 数据行)
- [x] docs/reference/kubernetes-api/resource/ — 2026-09-17 — 通过;组索引 6 条目一致
- [x] docs/reference/kubernetes-api/resource/device-class-v1/ — 2026-09-17 — 通过;8 h2+9 h3、35 表(111 数据行)
- [x] docs/reference/kubernetes-api/resource/device-taint-rule-v1/ — 2026-09-17 — 通过;7 h2+12 h3、45 表(134 数据行)
- [x] docs/reference/kubernetes-api/resource/resource-claim-template-v1/ — 2026-09-17 — 通过;4 h2+11 h3、39 表(140 数据行)
- [x] docs/reference/kubernetes-api/resource/resource-claim-v1/ — 2026-09-17 — 通过;21 h2+14 h3、67 表(234 数据行)
- [x] docs/reference/kubernetes-api/resource/resource-pool-status-request-v1alpha3/ — 2026-09-17 — 通过;
      9 h2+12 h3、47 表(154 数据行)
- [x] docs/reference/kubernetes-api/resource/resource-slice-v1/ — 2026-09-17 — 通过;17 h2+9 h3、44 表(161 数据行)
- [x] docs/reference/kubernetes-api/scheduling/ — 2026-09-17 — 通过;组索引 4 条目一致
- [x] docs/reference/kubernetes-api/scheduling/composite-pod-group-v1alpha3/ — 2026-09-17 — 通过;
      5 h2+14 h3、51 表(171 数据行)
- [x] docs/reference/kubernetes-api/scheduling/pod-group-v1beta1/ — 2026-09-17 — 通过;
      15 h2+14 h3、58 表(185 数据行)
- [x] docs/reference/kubernetes-api/scheduling/priority-class-v1/ — 2026-09-17 — 通过;3 h2+9 h3、30 表(106 数据行)
- [x] docs/reference/kubernetes-api/scheduling/workload-v1beta1/ — 2026-09-17 — 通过;13 h2+11 h3、45 表(164 数据行)
- [x] docs/reference/kubernetes-api/storage/ — 2026-09-17 — 通过;组索引 6 条目一致
- [x] docs/reference/kubernetes-api/storage/csi-driver-v1/ — 2026-09-17 — 通过;5 h2+9 h3、32 表(116 数据行)
- [x] docs/reference/kubernetes-api/storage/csi-node-v1/ — 2026-09-17 — 通过;9 h2+12 h3、47 表(139 数据行)
- [x] docs/reference/kubernetes-api/storage/csi-storage-capacity-v1/ — 2026-09-17 — 通过;3 h2+11 h3、38 表(141 数据行)
- [x] docs/reference/kubernetes-api/storage/storage-class-v1/ — 2026-09-17 — 通过;5 h2+9 h3、32 表(112 数据行)
- [x] docs/reference/kubernetes-api/storage/volume-attachment-v1/ — 2026-09-17 — 通过;7 h2+12 h3、45 表(136 数据行)
- [x] docs/reference/kubernetes-api/storage/volume-attributes-class-v1/ — 2026-09-17 — 通过;3 h2+9 h3、30 表(104 数据行)
- [x] docs/reference/kubernetes-api/storagemigration/ — 2026-09-17 — 通过;组索引 1 条目一致
- [x] docs/reference/kubernetes-api/storagemigration/storage-version-migration-v1/ — 2026-09-17 — 通过;
      5 h2+12 h3、43 表(127 数据行)
- [x] docs/reference/labels-annotations-taints/ — 2026-09-17 — 通过;
      3 h2 与 211 个 h3 标签小节标题逐一对应(按标题文本精确匹配);
      全页链接经 _redirects 归一后一一对应(quantity/endpoints/endpoint-slice/DRA 等
      均为旧→新路径等价改写);导语与各节文字抽样完整、无表格(与 HTML 一致)
- [x] docs/reference/labels-annotations-taints/audit-annotations/ — 2026-09-17 — 通过;
      14 个注解 h2 一致、NOTE(⑤ 碎片化)文字完整、6 个内链一致;
      Event API 链接 md 用新路径 core/event-v1(HTML 旧路径 cluster-resources/event-v1,
      经 _redirects 525 行等价重定向)
- [x] docs/reference/networking/ — 2026-09-17 — 通过;3 个 h5 条目链接一致
- [x] docs/reference/networking/ports-and-protocols/ — 2026-09-17 — 通过;
      2 表(9 数据行)精确一致,control plane/443/10250/2379-2380/10259 等端口行完整
- [x] docs/reference/networking/service-protocols/ — 2026-09-17 — 通过;
      2 h2+5 h3+1 h4、1 feature-state(SCTP Stable v1.20)、1 代码块(PROXY 行逐字一致)、
      NOTE(⑤ 碎片化)与内链(service/annotations/ingress)一致
- [x] docs/reference/networking/virtual-ips/ — 2026-09-17 — 通过;
      6 h2、12 代码块、7 feature-state(ipvs Deprecated/NFTables Alpha/WinDSR Stable/MultiCIDR Stable 等)、
      2 张插图(services-iptables-overview.svg、services-ipvs-overview.svg)已随页复制到 documents/images/;
      代理模式(iptables/ipvs/nftables/kernelspace)长文与会话亲和/IP 分配各节文字完整
- [x] docs/reference/node/ — 2026-09-17 — 通过;
      11 个 node 内链+3 个 instrumentation 交叉链接全部一致
- [x] docs/reference/node/device-plugin-api-versions/ — 2026-09-17 — 通过;
      兼容矩阵表(6 行)与 Key 列表逐字一致
- [x] docs/reference/node/dra-standard-device-attributes/ — 2026-09-17 — 通过;
      1 h2+3 h3、NUMA/PCI/PCIe 各节及 helper 函数列表文字完整
- [x] docs/reference/node/kernel-version-requirements/ — 2026-09-17 — 问题(②):
      页首 Note 提示框正文丢失——HTML 为 "Note: This section links to third party projects
      that provide functionality required by Kubernetes. The Kubernetes project authors
      aren't responsible for these projects, which are listed alphabetically.
      To add a project to this list, read the content guide before submitting a change.",
      md 仅剩 "**Note:**"、孤立链接 [content guide](…) 与 [More information.](#…);
      其余通过:5 h2(sysctls 列表/nftables/cgroup v2/PSI/other requirements/longterm)文字完整,
      页尾第三方免责声明完整
- [x] docs/reference/node/kubelet-checkpoint-api/ — 2026-09-17 — 通过;
      1 feature-state(Beta ContainerCheckpoint)、checkpoint 请求路径/参数列表一致
- [x] docs/reference/node/kubelet-config-directory-merging/ — 2026-09-17 — 通过;3 h3、9 代码块
- [x] docs/reference/node/kubelet-files/ — 2026-09-17 — 通过;5 h2+15 h3 文件路径列表一致
- [x] docs/reference/node/kubelet-pods-api/ — 2026-09-17 — 通过;7 h2+3 h3、1 feature-state(Beta PodsAPI v1.37)、1 代码块
- [x] docs/reference/node/kubelet-sync-loop/ — 2026-09-17 — 通过;syncLoop 三组件说明文字一致
- [x] docs/reference/node/node-labels/ — 2026-09-17 — 通过;标准标签列表(含 en-dash 说明)逐字一致
- [x] docs/reference/node/node-lifecycle-conditions/ — 2026-09-17 — 通过;
      1 h3+2 h4、1 表、3 代码块、feature-state(Alpha NodeLifecycleConditions v1.37)一致
- [x] docs/reference/node/node-status/ — 2026-09-17 — 通过;7 h2、1 表、2 代码块
- [x] docs/reference/node/pod-level-resource-managers/ — 2026-09-17 — 通过;3 h2+4 h3、2 表、feature-state(Beta v1.37)一致
- [x] docs/reference/node/seccomp/ — 2026-09-17 — 通过;2 h2+1 h3、2 代码块、seccompProfile.type 取值列表一致
- [x] docs/reference/node/swap-behavior/ — 2026-09-17 — 通过;NoSwap/LimitedSwap 说明一致
- [x] docs/reference/node/systemd-watchdog/ — 2026-09-17 — 通过;2 h2+1 h3、2 代码块
- [x] docs/reference/node/topics-on-dockershim-and-cri-compatible-runtimes/ — 2026-09-17 — 通过;2 h2 链接列表一致
- [x] docs/reference/node/what-happens-on-restart/ — 2026-09-17 — 通过;4 h2 文字一致
- [x] docs/reference/scheduling/ — 2026-09-17 — 通过;2 个 h5 条目一致
- [x] docs/reference/scheduling/config/ — 2026-09-17 — 通过;
      1 feature-state(Stable v1.25)、10 代码块、扩展点/插件各节文字一致、1 表(6 数据行)
- [x] docs/reference/scheduling/policies/ — 2026-09-17 — 通过;
      弃用说明(predicates/priorities, v1.23 移除)与 What's next 链接一致
- [x] docs/reference/setup-tools/ — 2026-09-17 — 通过;1 个 h5 条目一致
- [x] docs/reference/setup-tools/kubeadm/ — 2026-09-17 — 通过;
      导语插图(kubeadm-stacked-color.png 已复制到 documents/images/)、10 个 kubeadm-* 命令链接一致
- [x] docs/reference/setup-tools/kubeadm/implementation-details/ — 2026-09-17 — 通过;8 h2+16 h3 一致
- [x] docs/reference/setup-tools/kubeadm/kubeadm-alpha/ — 2026-09-17 — 通过;1 h2,子命令索引一致
- [x] docs/reference/setup-tools/kubeadm/kubeadm-certs/ — 2026-09-17 — 通过;6 h2+52 h3、18 代码块
- [x] docs/reference/setup-tools/kubeadm/kubeadm-config/ — 2026-09-17 — 通过;8 h2+21 h3、7 代码块
- [x] docs/reference/setup-tools/kubeadm/kubeadm-init-phase/ — 2026-09-17 — 通过;16 h2+149 h3、57 代码块
- [x] docs/reference/setup-tools/kubeadm/kubeadm-init/ — 2026-09-17 — 通过;1 h2+17 h3、10 代码块
- [x] docs/reference/setup-tools/kubeadm/kubeadm-join-phase/ — 2026-09-17 — 通过;8 h2+46 h3、18 代码块
- [x] docs/reference/setup-tools/kubeadm/kubeadm-join/ — 2026-09-17 — 通过;1 h2+8 h3、18 代码块
- [x] docs/reference/setup-tools/kubeadm/kubeadm-kubeconfig/ — 2026-09-17 — 通过;2 h2+7 h3、2 代码块
- [x] docs/reference/setup-tools/kubeadm/kubeadm-reset-phase/ — 2026-09-17 — 通过;5 h2+12 h3、4 代码块
- [x] docs/reference/setup-tools/kubeadm/kubeadm-reset/ — 2026-09-17 — 通过;1 h2+9 h3、7 代码块
- [x] docs/reference/setup-tools/kubeadm/kubeadm-token/ — 2026-09-17 — 通过;5 h2+12 h3、4 代码块
- [x] docs/reference/setup-tools/kubeadm/kubeadm-upgrade-phase/ — 2026-09-17 — 通过;3 h2+42 h3、14 代码块
- [x] docs/reference/setup-tools/kubeadm/kubeadm-upgrade/ — 2026-09-17 — 通过;6 h2+12 h3、6 代码块
- [x] docs/reference/setup-tools/kubeadm/kubeadm-version/ — 2026-09-17 — 通过;3 h3、1 代码块
- [x] docs/reference/tools/ — 2026-09-17 — 通过;
      8 个工具小节(crictl/Dashboard/Headlamp/Helm/kind/Kompose/Kui/Minikube)一致,
      页尾第三方免责声明完整
- [x] docs/reference/using-api/ — 2026-09-17 — 轻微问题(⑥ 近亲):
      "Beta" 列表项内的 NOTE 提示被内联到 "- Beta:" 同行
      ("- Beta:  > [!NOTE] > Please try beta features...")HTML 中为独立提示框,文字无丢失;
      其余通过:5 h2、Alpha/Beta/Stable 各条目、API groups、runtime-config 示例、persistence 一致
- [x] docs/reference/using-api/api-concepts/ — 2026-09-17 — 轻微问题(⑥/⑤ 近亲):
      "Protobuf wrapper format" 在 HTML 为独立 <pre> 代码块+独立 Note 提示框,
      md 将其压为一行行内代码并粘连 "} Note:Clients that receive…"(Note 正文完整);
      其余通过:17 h2+25 h3、39 代码块、3 表、资源生命周期/Watch/分块/dry-run 等文字完整;
      链接中 service-resources→core 等为 _redirects 等价改写
- [x] docs/reference/using-api/cel/ — 2026-09-17 — 通过;
      7 h2+11 h3、2 代码块、21 表(156 数据行,含 1 个上游空行 "| | | |")逐表核对一致;
      feature-state(AuthorizeWithSelectors 等)为 HTML 渲染形式,文字完整
- [x] docs/reference/using-api/client-libraries/ — 2026-09-17 — 通过;2 h2、2 表(客户端库列表)一致
- [x] docs/reference/using-api/declarative-validation/ — 2026-09-17 — 通过;
      4 h2+26 h3、27 代码块、1 表;subresource-path 占位符等文字完整
- [x] docs/reference/using-api/deprecation-guide/ — 2026-09-17 — 通过;
      2 h2+10 h3、各 API 弃用迁移列表逐项一致
- [x] docs/reference/using-api/health-checks/ — 2026-09-17 — 通过;2 h2、6 代码块、
      链接(kube-apiserver/probes)一致
- [x] docs/reference/using-api/server-side-apply/ — 2026-09-17 — 通过;
      12 h2+8 h3、15 代码块、1 表;managedFields/合并规则文字完整;
      object-meta 链接改用新路径(definitions→common-definitions,_redirects 等价)
- [x] docs/setup/ — 2026-09-17 — 通过;导语+学习/生产环境+What's next 各链接一致
- [x] docs/setup/best-practices/ — 2026-09-17 — 通过;5 个 h5 条目链接一致
- [x] docs/setup/best-practices/certificates/ — 2026-09-17 — 通过;
      4 h2+6 h3、7 表(证书要求)、4 代码块;CSR 链接改新路径(authentication-resources→certificates,_redirects 509 等价)
- [x] docs/setup/best-practices/cluster-large/ — 2026-09-17 — 通过;5 h2+1 h3、限额与 dashboard 链接一致
- [x] docs/setup/best-practices/enforcing-pod-security-standards/ — 2026-09-17 — 通过;2 h2+3 h3、版本表格文字一致
- [x] docs/setup/best-practices/multiple-zones/ — 2026-09-17 — 通过;8 h2+1 h3、7 个内外链接一致
- [x] docs/setup/best-practices/node-conformance/ — 2026-09-17 — 通过;6 h2、3 代码块、1 表一致
- [x] docs/setup/learning-environment/ — 2026-09-17 — 通过;5 h2+3 h3、工具列表(kind/minikube/Rancher Desktop/MicroK8s/CRC)一致
- [x] docs/setup/production-environment/ — 2026-09-17 — 通过;5 h2+2 h3、turnkey 链接与考量清单一致
- [x] docs/setup/production-environment/container-runtimes/ — 2026-09-17 — 通过;5 h2+8 h3、9 代码块、CRI/kubelet 链接一致
- [x] docs/setup/production-environment/tools/ — 2026-09-17 — 通过;4 个部署工具(kubeadm/Cluster API/kops/kubespray)一致
- [x] docs/setup/production-environment/tools/kubeadm/ — 2026-09-17 — 通过;9 个 h5 条目链接一致
- [x] docs/setup/production-environment/tools/kubeadm/control-plane-flags/ — 2026-09-17 — 通过;
      5 h2+4 h3、10 代码块;kubeadm 组件自定义说明与链接一致
- [x] docs/setup/production-environment/tools/kubeadm/create-cluster-kubeadm/ — 2026-09-17 — 问题(链接丢失×2):
      ①HTML 24 行 "Kubernetes' version and version skew support policy" 为指向
      /docs/setup/release/version-skew-policy/ 的链接(经 436 重定向),md 24 行退化为纯文本;
      ②HTML 379 行 "Version Skew Policy" 同样为链接,md "see the Version Skew Policy." 退化为纯文本;
      其余通过:8 h2+20 h3、17 代码块、前置条件/网络插件/清理等长文一致
- [x] docs/setup/production-environment/tools/kubeadm/dual-stack-support/ — 2026-09-17 — 通过;
      2 h2+4 h3、9 代码块、节点/Service/Pod 链接一致
- [x] docs/setup/production-environment/tools/kubeadm/ha-topology/ — 2026-09-17 — 通过;3 h2、堆叠/外部 etcd 说明一致
- [x] docs/setup/production-environment/tools/kubeadm/high-availability/ — 2026-09-17 — 通过;
      6 h2+9 h3、16 代码块、负载均衡器指南一致
- [x] docs/setup/production-environment/tools/kubeadm/install-kubeadm/ — 2026-09-17 — 通过;
      10 h2、17 代码块、2 表(包版本);CRI 旧路径为 _redirects 等价
- [x] docs/setup/production-environment/tools/kubeadm/kubelet-integration/ — 2026-09-17 — 通过;4 h2+4 h3、6 代码块、1 表
- [x] docs/setup/production-environment/tools/kubeadm/setup-ha-etcd-with-kubeadm/ — 2026-09-17 — 通过;
      3 h2、11 代码块(含 etcd-healthcheck-client 等证书命令)
- [x] docs/setup/production-environment/tools/kubeadm/troubleshooting-kubeadm/ — 2026-09-17 — 通过;
      21 h2、26 代码块(含旧 Docker 升级提示)
- [x] docs/setup/production-environment/turnkey-solutions/ — 2026-09-17 — 通过;
      导语一致;正文列表由外部脚本渲染(HTML main 内亦无内容链接),与 pinned 渲染一致
- [x] docs/tasks/ — 2026-09-17 — 通过;17 个子章节条目及描述、链接一致
- [x] docs/tasks/access-application-cluster/ — 2026-09-17 — 通过;11 个 h5 条目及描述、链接一致
- [x] docs/tasks/access-application-cluster/access-cluster-services/ — 2026-09-17 — 问题(链接悬空):
      md 24 行 [kubectl expose](../../../reference/kubectl/#expose) 指向 kubectl 索引页,
      该页无 #expose 锚;HTML 指向 generated/kubectl/kubectl-commands/#expose;
      其余 2 h2+2 h3+2 h4、6 代码块一致
- [x] docs/tasks/access-application-cluster/access-cluster/ — 2026-09-17 — 问题(链接悬空):
      md 42 行 [kubectl proxy](../../../reference/kubectl/#proxy) 同样指向无锚的 kubectl 索引页,
      HTML 为 kubectl-commands/#proxy;其余 7 h2+5 h3、10 代码块一致
- [x] docs/tasks/access-application-cluster/communicate-containers-same-pod-shared-volume/ — 2026-09-17 — 通过;
      4 h2、10 代码块一致
- [x] docs/tasks/access-application-cluster/configure-access-multiple-clusters/ — 2026-09-17 — 问题(链接悬空):
      md 362 行 [kubectl config](../../../reference/kubectl/#config) 同样悬空,
      HTML 为 kubectl-commands/#config;其余 9 h2+8 h3、25 代码块一致
- [x] docs/tasks/access-application-cluster/configure-dns-cluster/ — 2026-09-17 — 通过;
      短页,仅导语+dns-custom-nameservers 链接,与 HTML 一致
- [x] docs/tasks/access-application-cluster/connecting-frontend-backend/ — 2026-09-17 — 通过;
      9 h2、18 代码块逐一对应,labels/deployment 链接一致
- [x] docs/tasks/access-application-cluster/create-external-load-balancer/ — 2026-09-17 — 通过;
      7 h2+3 h3、7 代码块;externalTrafficPolicy 说明一致
- [x] docs/tasks/access-application-cluster/list-all-running-container-images/ — 2026-09-17 — 通过;
      7 h2+1 h3、5 代码块、go-template/jsonpath 示例一致
- [x] docs/tasks/access-application-cluster/port-forward-access-application-cluster/ — 2026-09-17 — 通过;
      6 h2+1 h3、25 代码块一致
- [x] docs/tasks/access-application-cluster/service-access-application-cluster/ — 2026-09-17 — 通过;
      6 h2、13 代码块、deployment/replicaset/pods 链接一致
- [x] docs/tasks/access-application-cluster/web-ui-dashboard/ — 2026-09-17 — 通过;
      6 h2+4 h3+6 h4、3 代码块、Deployment 示例字段列表一致
- [x] docs/tasks/administer-cluster/ — 2026-09-17 — 通过;46 个 h5 条目及描述、链接一致
- [x] docs/tasks/administer-cluster/access-cluster-api/ — 2026-09-17 — 问题(链接悬空):
      md [kubectl proxy](../../../reference/kubectl/#proxy) 指向无 #proxy 锚的 kubectl 索引页,
      HTML 为 kubectl-commands/#proxy;其余 3 h2+3 h3+8 h4、13 代码块一致
- [x] docs/tasks/administer-cluster/certificates/ — 2026-09-17 — 通过;2 h2+3 h3、22 代码块(cfssl 工作流)一致
- [x] docs/tasks/administer-cluster/change-default-storage-class/ — 2026-09-17 — 通过;4 h2、6 代码块一致
- [x] docs/tasks/administer-cluster/change-pv-access-mode-readwriteoncepod/ — 2026-09-17 — 通过;
      4 h2、8 代码块、ReadWriteOncePod 前置条件一致
- [x] docs/tasks/administer-cluster/change-pv-reclaim-policy/ — 2026-09-17 — 通过;
      4 h2+1 h3、6 代码块;PersistentVolume 链接改新路径(config-and-storage-resources,_redirects 等价)
- [x] docs/tasks/administer-cluster/cluster-upgrade/ — 2026-09-17 — 通过;
      3 h2+6 h3、升级步骤一致;备注:HTML 有指向 kubectl-commands 的链接而 md 用 reference/kubectl/
- [x] docs/tasks/administer-cluster/configure-feature-gates/ — 2026-09-17 — 通过;8 h2+6 h3、15 代码块一致
- [x] docs/tasks/administer-cluster/configure-upgrade-etcd/ — 2026-09-17 — 通过;
      11 h2+11 h3、19 代码块;单成员/多成员 etcd 恢复流程一致;static-pod 链接在 HTML 中亦有
- [x] docs/tasks/administer-cluster/controller-manager-leader-migration/ — 2026-09-17 — 通过;
      3 h2+7 h3、6 代码块、策略配置示例一致
- [x] docs/tasks/administer-cluster/coredns/ — 2026-09-17 — 通过;6 h2、CoreDNS 定制链接一致
- [x] docs/tasks/administer-cluster/cpu-management-policies/ — 2026-09-17 — 通过;
      6 h2+3 h3、1 代码块;备注:HTML 额外链接 node-resource-managers 概念页
      (HTML 2 处,md 0 处)——MD 缺该链接(轻微,目标页存在于 public/)
- [x] docs/tasks/administer-cluster/declare-network-policy/ — 2026-09-17 — 通过;7 h2、18 代码块一致
- [x] docs/tasks/administer-cluster/decrypt-data/ — 2026-09-17 — 通过;4 h2+5 h5、3 代码块、secret/api/static-pod 链接一致
- [x] docs/tasks/administer-cluster/developing-cloud-controller-manager/ — 2026-09-17 — 通过;
      2 h2+2 h3、开发流程与 daemonset/glossary 链接一致
- [x] docs/tasks/administer-cluster/dns-custom-nameservers/ — 2026-09-17 — 通过;4 h2+2 h3+1 h4、4 代码块、CoreDNS/StubDomain 配置一致
- [x] docs/tasks/administer-cluster/dns-debugging-resolution/ — 2026-09-17 — 通过;3 h2+9 h3、31 代码块、nslookup 故障排查流程一致
- [x] docs/tasks/administer-cluster/dns-horizontal-autoscaling/ — 2026-09-17 — 通过;8 h2+3 h3、21 代码块、autoscaler 部署参数一致
- [x] docs/tasks/administer-cluster/enable-disable-api/ — 2026-09-17 — 通过;
      1 h2、runtime-config 说明一致;弃用政策链接 md 用 reference/deprecation-policy
      (HTML 用 using-api/deprecation-policy 旧路径)
- [x] docs/tasks/administer-cluster/encrypt-data/ — 2026-09-17 — 轻微问题(⑪):
      提供方对照表(identity/aescbc/aesgcm/kms v1/kms v2/secretbox 共 6 行,13 tr)
      在 md 中退化伪表:142 行表头后无分隔行、各"行"与描述断为多个段落,文字无丢失;
      其余通过:9 h2+10 h3+2 h4、16 代码块、kms 配置与背书一致
- [x] docs/tasks/administer-cluster/extended-resource-node/ — 2026-09-17 — 通过;
      6 h2+3 h3、13 代码块;DRA 链接改新路径(resource-management,等价)
- [x] docs/tasks/administer-cluster/guaranteed-scheduling-critical-addon-pods/ — 2026-09-17 — 通过;
      短页,reserved+affinity 配置说明一致
- [x] docs/tasks/administer-cluster/hardening-dra/ — 2026-09-17 — 通过;6 h2+3 h3、3 代码块、安全策略一致
- [x] docs/tasks/administer-cluster/ip-masq-agent/ — 2026-09-17 — 通过;3 h2+1 h3、9 代码块、masq 配置一致
- [x] docs/tasks/administer-cluster/kms-provider/ — 2026-09-17 — 通过;
      9 h2+10 h3+3 h4、8 代码块、1 表;kms v1/v2 握手与 Status 响应一致
- [x] docs/tasks/administer-cluster/kubeadm/ — 2026-09-17 — 通过;任务索引列表一致
- [x] docs/tasks/administer-cluster/kubeadm/adding-linux-nodes/ — 2026-09-17 — 通过;3 h2+1 h3、9 代码块一致
- [x] docs/tasks/administer-cluster/kubeadm/adding-windows-nodes/ — 2026-09-17 — 通过;3 h2+5 h3+1 h4、11 代码块一致
- [x] docs/tasks/administer-cluster/kubeadm/change-package-repository/ — 2026-09-17 — 通过;3 h2+1 h3、12 代码块一致
- [x] docs/tasks/administer-cluster/kubeadm/kubeadm-certs/ — 2026-09-17 — 通过;14 h2+13 h3+3 h4、22 代码块一致
- [x] docs/tasks/administer-cluster/kubeadm/kubeadm-reconfigure/ — 2026-09-17 — 通过;4 h2+5 h3+10 h4、11 代码块一致
- [x] docs/tasks/administer-cluster/kubeadm/kubeadm-upgrade/ — 2026-09-17 — 通过;8 h2+6 h3、20 代码块(apt/dnf/yum 变体)一致
- [x] docs/tasks/administer-cluster/kubeadm/upgrading-linux-nodes/ — 2026-09-17 — 通过;4 h2+5 h3、10 代码块一致
- [x] docs/tasks/administer-cluster/kubeadm/upgrading-windows-nodes/ — 2026-09-17 — 通过;3 h2+5 h3、7 代码块一致
- [x] docs/tasks/administer-cluster/kubelet-config-file/ — 2026-09-17 — 通过;6 h2+1 h3、5 代码块、kubelet 配置覆盖流程一致
- [x] docs/tasks/administer-cluster/kubelet-credential-provider/ — 2026-09-17 — 通过;5 h2+1 h3+1 h4、credential provider 配置一致
- [x] docs/tasks/administer-cluster/kubelet-in-userns/ — 2026-09-17 — 通过;8 h2+13 h3、4 代码块、user namespace 前置检查一致
- [x] docs/tasks/administer-cluster/limit-storage-consumption/ — 2026-09-17 — 通过;5 h2、quota 示例一致
- [x] docs/tasks/administer-cluster/manage-resources/ — 2026-09-17 — 通过;5 个 h5 条目一致
- [x] docs/tasks/administer-cluster/manage-resources/cpu-constraint-namespace/ — 2026-09-17 — 通过;
      11 h2+2 h3、23 代码块;limit-range 链接改新路径(_redirects 560 等价)
- [x] docs/tasks/administer-cluster/manage-resources/cpu-default-namespace/ — 2026-09-17 — 通过;8 h2+2 h3、16 代码块一致
- [x] docs/tasks/administer-cluster/manage-resources/memory-constraint-namespace/ — 2026-09-17 — 通过;
      10 h2+2 h3、24 代码块
- [x] docs/tasks/administer-cluster/manage-resources/memory-default-namespace/ — 2026-09-17 — 通过;8 h2+2 h3、17 代码块一致
- [x] docs/tasks/administer-cluster/manage-resources/quota-memory-cpu-namespace/ — 2026-09-17 — 通过;
      8 h2+2 h3、14 代码块;resource-quota 链接改新路径(_redirects 等价)
- [x] docs/tasks/administer-cluster/manage-resources/quota-pod-namespace/ — 2026-09-17 — 通过;
      5 h2+3 h3、10 代码块、cron-jobs/deployment 链接一致
- [x] docs/tasks/administer-cluster/memory-manager/ — 2026-09-17 — 通过;
      6 h2+6 h3+3 h4、6 代码块、feature-state(Stable MemoryManager v1.32)、pod-qos 链接一致
- [x] docs/tasks/administer-cluster/migrating-from-dockershim/ — 2026-09-17 — 通过;
      短索引页,checklist 链接一致
- [x] docs/tasks/administer-cluster/migrating-from-dockershim/change-runtime-containerd/ — 2026-09-17 — 通过;
      9 h2、13 代码块一致
- [x] docs/tasks/administer-cluster/migrating-from-dockershim/check-if-dockershim-removal-affects-you/ — 2026-09-17 — 问题(③链接丢失×2):
      ①HTML "Kubernetes Containerd integration goes GA blog post" 为 /blog/2018/05/24/... 链接,md 53 行纯文本;
      ②HTML "dockershim deprecation FAQ" 为 /blog/2020/12/02/dockershim-faq/ 链接,md 78 行纯文本;
      cri-containerd.png 插图已复制到 documents/images/;其余 4 h2+1 h3+1 h4、1 代码块一致
- [x] docs/tasks/administer-cluster/migrating-from-dockershim/find-out-runtime-you-use/ — 2026-09-17 — 通过;
      3 h2、4 代码块;CRI 链接旧路径等价(_redirects 503)
- [x] docs/tasks/administer-cluster/migrating-from-dockershim/migrating-telemetry-and-security-agents/ — 2026-09-17 — 通过;
      2 h2+11 h3、agent 厂商列表与链接一致
- [x] docs/tasks/administer-cluster/migrating-from-dockershim/troubleshooting-cni-plugin-related-errors/ — 2026-09-17 — 通过;
      2 h2+3 h3、3 代码块一致
- [x] docs/tasks/administer-cluster/namespaces/ — 2026-09-17 — 通过;8 h2+2 h3、25 代码块;namespace 链接旧路径等价(_redirects)
- [x] docs/tasks/administer-cluster/network-policy-provider/ — 2026-09-17 — 通过;组索引 4 条目一致
- [x] docs/tasks/administer-cluster/network-policy-provider/antrea-network-policy/ — 2026-09-17 — 通过;3 h2 一致
- [x] docs/tasks/administer-cluster/network-policy-provider/calico-network-policy/ — 2026-09-17 — 通过;4 h2、3 代码块一致
- [x] docs/tasks/administer-cluster/network-policy-provider/cilium-network-policy/ — 2026-09-17 — 通过;5 h2、8 代码块一致
- [x] docs/tasks/administer-cluster/network-policy-provider/kube-router-network-policy/ — 2026-09-17 — 通过;3 h2 一致
- [x] docs/tasks/administer-cluster/node-overprovisioning/ — 2026-09-17 — 通过;6 h2+3 h3、9 代码块、PriorityClass 示例一致
- [x] docs/tasks/administer-cluster/nodelocaldns/ — 2026-09-17 — 通过;7 h2+1 h4(HTML h4 无 id,文字一致)、4 代码块、configmap 脚本逐字一致
- [x] docs/tasks/administer-cluster/quota-api-object/ — 2026-09-17 — 通过;8 h2+2 h3、13 代码块、1 表一致
- [x] docs/tasks/administer-cluster/reserve-compute-resources/ — 2026-09-17 — 通过;4 h2+7 h3、Capacity/Allocatable 说明一致
- [x] docs/tasks/administer-cluster/running-cloud-controller/ — 2026-09-17 — 通过;4 h2+5 h3、1 代码块一致
- [x] docs/tasks/administer-cluster/safely-drain-node/ — 2026-09-17 — 问题(链接悬空×2):
      md 31/72 行 [kubectl drain](../../../reference/kubectl/#drain) 指向无锚的 kubectl 索引页,
      HTML 为 kubectl-commands/#drain;其余 6 h2、3 代码块、eviction API 说明一致
- [x] docs/tasks/administer-cluster/securing-a-cluster/ — 2026-09-17 — 通过;
      6 h2+16 h3、安全清单各节一致;DRA 链接改新路径(等价)
- [x] docs/tasks/administer-cluster/switch-to-evented-pleg/ — 2026-09-17 — 通过;
      4 h2、5 代码块、feature-state(Alpha EventedPLEG v1.29)一致
- [x] docs/tasks/administer-cluster/sysctl-cluster/ — 2026-09-17 — 通过;
      5 h2+4 h3、5 代码块、安全/不安全 sysctl 列表一致
- [x] docs/tasks/administer-cluster/topology-manager/ — 2026-09-17 — 通过;10 h2+8 h3、5 代码块一致
- [x] docs/tasks/administer-cluster/use-cascading-deletion/ — 2026-09-17 — 通过;6 h2、15 代码块(前台/后台级联)一致
- [x] docs/tasks/administer-cluster/verify-signed-artifacts/ — 2026-09-17 — 通过;
      5 h2+1 h3、6 代码块、cosign/gpg 验证步骤一致
- [x] docs/tasks/configmap-secret/ — 2026-09-17 — 通过;短索引页一致
- [x] docs/tasks/configmap-secret/managing-secret-using-config-file/ — 2026-09-17 — 问题(链接悬空):
      md 中 [kubectl create configmap](../../../reference/kubectl/#configmap) 类链接
      (reference/kubectl/#)指向无锚的 kubectl 索引页,HTML 为 kubectl-commands 锚;
      其余 5 h2+2 h3、17 代码块一致
- [x] docs/tasks/configmap-secret/managing-secret-using-kubectl/ — 2026-09-17 — 通过;5 h2+4 h3、17 代码块一致
- [x] docs/tasks/configmap-secret/managing-secret-using-kustomize/ — 2026-09-17 — 通过;5 h2+2 h3、13 代码块一致
- [x] docs/tasks/configure-pod-container/ — 2026-09-17 — 通过;组索引条目一致
- [x] docs/tasks/configure-pod-container/assign-cpu-resource/ — 2026-09-17 — 通过;10 h2+2 h3、20 代码块一致
- [x] docs/tasks/configure-pod-container/assign-memory-resource/ — 2026-09-17 — 通过;10 h2+2 h3、35 代码块一致
- [x] docs/tasks/configure-pod-container/assign-pod-level-resources/ — 2026-09-17 — 通过;
      8 h2+2 h3、21 代码块、feature-state(Beta PodLevelResources v1.32)一致
- [x] docs/tasks/configure-pod-container/assign-pods-nodes-using-node-affinity/ — 2026-09-17 — 通过;5 h2、13 代码块一致
- [x] docs/tasks/configure-pod-container/assign-pods-nodes/ — 2026-09-17 — 通过;5 h2、10 代码块、节点标签输出一致
- [x] docs/tasks/configure-pod-container/assign-resources/ — 2026-09-17 — 通过;DRA 任务组索引一致
- [x] docs/tasks/configure-pod-container/assign-resources/access-dra-device-metadata/ — 2026-09-17 — 通过;
      6 h2+1 h3、15 代码块、device 属性说明一致
- [x] docs/tasks/configure-pod-container/assign-resources/allocate-devices-dra/ — 2026-09-17 — 通过;
      7 h2、11 代码块、feature-state(Stable DRA v1.34)、约束列表一致
- [x] docs/tasks/configure-pod-container/assign-resources/set-up-dra-cluster/ — 2026-09-17 — 通过;
      8 h2、11 代码块、feature-state(Stable DRA)、驱动安装步骤一致
- [x] docs/tasks/configure-pod-container/attach-handler-lifecycle-event/ — 2026-09-17 — 通过;4 h2+1 h3、6 代码块一致
- [x] docs/tasks/configure-pod-container/configure-bind-mount-options/ — 2026-09-17 — 通过;
      3 h2、7 代码块、feature-state(Alpha VolumeBindMountOptions)一致
- [x] docs/tasks/configure-pod-container/configure-emptydir-volume-mode/ — 2026-09-17 — 通过;
      3 h2、6 代码块、feature-state(Alpha EmptyDirVolumeMode...实际为 Stable)一致
- [x] docs/tasks/configure-pod-container/configure-gmsa/ — 2026-09-17 — 通过;8 h2+2 h3、13 代码块一致
- [x] docs/tasks/configure-pod-container/configure-liveness-readiness-startup-probes/ — 2026-09-17 — 通过;
      9 h2+2 h3、24 代码块;Pod 链接改新路径(等价)
- [x] docs/tasks/configure-pod-container/configure-pod-configmap/ — 2026-09-17 — 问题(链接悬空):
      md 含 [kubectl ...](../../../reference/kubectl/#…) 类锚链接(如 #create-secret 等),
      指向无对应锚的 kubectl 索引页,HTML 为 kubectl-commands#…;
      其余 12 h2+9 h3+7 h4、77 代码块一致
- [x] docs/tasks/configure-pod-container/configure-pod-initialization/ — 2026-09-17 — 通过;3 h2、8 代码块一致
- [x] docs/tasks/configure-pod-container/configure-projected-volume-storage/ — 2026-09-17 — 通过;4 h2、8 代码块一致
- [x] docs/tasks/configure-pod-container/configure-runasusername/ — 2026-09-17 — 通过;5 h2、12 代码块一致
- [x] docs/tasks/configure-pod-container/configure-service-account/ — 2026-09-17 — 通过;
      7 h2+7 h3、27 代码块;ServiceAccount/TokenRequest 链接改新路径(_redirects 等价)
- [x] docs/tasks/configure-pod-container/configure-volume-storage/ — 2026-09-17 — 通过;3 h2、13 代码块一致
- [x] docs/tasks/configure-pod-container/create-hostprocess-pod/ — 2026-09-17 — 通过;8 h2+6 h6、3 代码块、1 表一致
- [x] docs/tasks/configure-pod-container/enforce-standards-admission-controller/ — 2026-09-17 — 通过;2 h2、1 代码块一致
- [x] docs/tasks/configure-pod-container/enforce-standards-namespace-labels/ — 2026-09-17 — 通过;3 h2+2 h3、5 代码块一致
- [x] docs/tasks/configure-pod-container/extended-resource/ — 2026-09-17 — 通过;6 h2+2 h3、12 代码块一致
- [x] docs/tasks/configure-pod-container/image-volumes/ — 2026-09-17 — 通过;
      4 h2、12 代码块、feature-state(Stable ImageVolume v1.36)一致
- [x] docs/tasks/configure-pod-container/migrate-from-psp/ — 2026-09-17 — 通过;8 h2+7 h3、8 代码块一致
- [x] docs/tasks/configure-pod-container/pull-image-private-registry/ — 2026-09-17 — 通过;
      8 h2、18 代码块;Pod 链接改新路径(workload-resources→core,_redirects 等价)
- [x] docs/tasks/configure-pod-container/quality-service-pod/ — 2026-09-17 — 通过;9 h2+2 h3+3 h4、23 代码块一致
- [x] docs/tasks/configure-pod-container/resize-container-resources/ — 2026-09-17 — 通过;
      11 h2+4 h3、15 代码块、feature-state(Stable InPlacePodVerticalScaling v1.33)一致
- [x] docs/tasks/configure-pod-container/resize-pod-resources/ — 2026-09-17 — 通过;
      7 h2+2 h3、6 代码块、feature-state(Beta InPlacePodLevelResourcesVertical)一致
- [x] docs/tasks/configure-pod-container/security-context/ — 2026-09-17 — 通过;14 h2+3 h3+2 h4、62 代码块一致
- [x] docs/tasks/configure-pod-container/share-process-namespace/ — 2026-09-17 — 通过;3 h2、9 代码块一致
- [x] docs/tasks/configure-pod-container/static-pod/ — 2026-09-17 — 通过;5 h2+2 h3、20 代码块一致
- [x] docs/tasks/configure-pod-container/translate-compose-kubernetes/ — 2026-09-17 — 通过;9 h2+3 h3、35 代码块、2 表一致
- [x] docs/tasks/configure-pod-container/user-namespaces/ — 2026-09-17 — 通过;
      2 h2、8 代码块、feature-state(Stable UserNamespacesSupport v1.33)一致
- [x] docs/tasks/debug/ — 2026-09-17 — 通过;2 h2+5 h3、调试索引表(15 数据行)一致
- [x] docs/tasks/debug/debug-application/ — 2026-09-17 — 通过;组索引条目一致
- [x] docs/tasks/debug/debug-application/debug-init-containers/ — 2026-09-17 — 通过;5 h2、6 代码块、1 表一致
- [x] docs/tasks/debug/debug-application/debug-pods/ — 2026-09-17 — 通过;2 h2+3 h3+7 h4、5 代码块一致
- [x] docs/tasks/debug/debug-application/debug-running-pod/ — 2026-09-17 — 通过;9 h2+7 h3、63 代码块、1 表一致
- [x] docs/tasks/debug/debug-application/debug-service/ — 2026-09-17 — 通过;12 h2+4 h3+2 h4、60 代码块一致
- [x] docs/tasks/debug/debug-application/debug-statefulset/ — 2026-09-17 — 通过;3 h2、1 代码块一致
- [x] docs/tasks/debug/debug-application/determine-reason-pod-failure/ — 2026-09-17 — 通过;4 h2、8 代码块一致
- [x] docs/tasks/debug/debug-application/get-shell-running-container/ — 2026-09-17 — 问题(链接悬空):
      md 中 kubectl exec 链接指向 reference/kubectl/#exec(无锚),HTML 为 kubectl-commands/#exec;
      其余 6 h2、13 代码块一致
- [x] docs/tasks/debug/debug-cluster/ — 2026-09-17 — 通过;4 h2+6 h3、8 代码块、节点日志清单一致
- [x] docs/tasks/debug/debug-cluster/audit/ — 2026-09-17 — 通过;5 h2+3 h3、6 代码块、审计策略字段一致
- [x] docs/tasks/debug/debug-cluster/crictl/ — 2026-09-17 — 通过;5 h2+5 h3、23 代码块一致
- [x] docs/tasks/debug/debug-cluster/kubectl-node-debug/ — 2026-09-17 — 通过;3 h2、6 代码块一致
- [x] docs/tasks/debug/debug-cluster/local-debugging/ — 2026-09-17 — 通过;5 h2、1 代码块、端口占位符说明一致
- [x] docs/tasks/debug/debug-cluster/monitor-node-health/ — 2026-09-17 — 通过;
      7 h2+4 h3、5 代码块;Event 链接改新路径(cluster-resources→core,等价)
- [x] docs/tasks/debug/debug-cluster/resource-metrics-pipeline/ — 2026-09-17 — 通过;
      4 h2+2 h3、7 代码块、metrics-server 说明一致;api-service 链接改新路径(等价);
      备注:HTML 另有指向 tasks/run-application/horizontal-pod-autoscale 的链接,md 用 concepts 版
- [x] docs/tasks/debug/debug-cluster/resource-usage-monitoring/ — 2026-09-17 — 通过;
      短页;HPA 链接 md 用 concepts 版(HTML 用 tasks 版,内容等价)
- [x] docs/tasks/debug/debug-cluster/topology/ — 2026-09-17 — 通过;5 h2+1 h3、6 代码块一致
- [x] docs/tasks/debug/debug-cluster/troubleshoot-kubectl/ — 2026-09-17 — 通过;9 h2、9 代码块一致
- [x] docs/tasks/debug/debug-cluster/windows/ — 2026-09-17 — 通过;2 h2+2 h3、7 代码块(含 PowerShell)一致
- [x] docs/tasks/debug/logging/ — 2026-09-17 — 通过;短页、外部日志指南链接一致
- [x] docs/tasks/debug/monitoring/ — 2026-09-17 — 通过;短页、Traces 链接一致
- [x] docs/tasks/extend-kubectl/kubectl-plugins/ — 2026-09-17 — 通过;5 h2+9 h3+6 h4、35 代码块一致
- [x] docs/tasks/extend-kubernetes/ — 2026-09-17 — 通过;短索引页一致
- [x] docs/tasks/extend-kubernetes/configure-aggregation-layer/ — 2026-09-17 — 通过;4 h2+7 h3+3 h4、4 代码块一致
- [x] docs/tasks/extend-kubernetes/configure-multiple-schedulers/ — 2026-09-17 — 通过;5 h2+2 h3、17 代码块一致
- [x] docs/tasks/extend-kubernetes/custom-resources/ — 2026-09-17 — 通过;短索引页一致
- [x] docs/tasks/extend-kubernetes/custom-resources/custom-resource-definition-versioning/ — 2026-09-17 — 通过;
      7 h2+12 h3+1 h4、24 代码块;CRD 链接改新路径(extend-resources→apiextensions,_redirects 555 等价)
- [x] docs/tasks/extend-kubernetes/custom-resources/custom-resource-definitions/ — 2026-09-17 — 通过;
      8 h2+13 h3+17 h4、93 代码块、7 表(CRD 长文)一致;
      备注:HTML 上游 href 有双斜笔误 "reference//kubernetes-api/...",md 侧无此问题
- [x] docs/tasks/extend-kubernetes/http-proxy-access-api/ — 2026-09-17 — 问题(链接悬空):
      md 含 [kubectl ...](../../../reference/kubectl/#…) 悬空锚链接;HTML 为 kubectl-commands 锚;
      其余 4 h2、6 代码块一致
- [x] docs/tasks/extend-kubernetes/setup-extension-api-server/ — 2026-09-17 — 通过;3 h2、部署/证书流程一致
- [x] docs/tasks/extend-kubernetes/setup-konnectivity/ — 2026-09-17 — 通过;2 h2、6 代码块一致
- [x] docs/tasks/extend-kubernetes/socks5-proxy-access-api/ — 2026-09-17 — 通过;6 h2、7 代码块(含 ssh 参数列表)一致
- [x] docs/tasks/inject-data-application/ — 2026-09-17 — 通过;组索引一致
- [x] docs/tasks/inject-data-application/define-command-argument-container/ — 2026-09-17 — 通过;5 h2、7 代码块一致
- [x] docs/tasks/inject-data-application/define-environment-variable-container/ — 2026-09-17 — 通过;4 h2、7 代码块一致
- [x] docs/tasks/inject-data-application/define-environment-variable-via-file/ — 2026-09-17 — 通过;
      4 h2+2 h3、7 代码块、feature-state(Beta EnvFiles v1.35)一致
- [x] docs/tasks/inject-data-application/define-interdependent-environment-variables/ — 2026-09-17 — 通过;3 h2、7 代码块一致
- [x] docs/tasks/inject-data-application/distribute-credentials-secure/ — 2026-09-17 — 通过;7 h2+7 h3、43 代码块一致
- [x] docs/tasks/inject-data-application/downward-api-volume-expose-pod-information/ — 2026-09-17 — 通过;
      5 h2、16 代码块;Pod 链接改新路径(等价)
- [x] docs/tasks/inject-data-application/environment-variable-expose-pod-information/ — 2026-09-17 — 通过;
      4 h2、13 代码块;Pod 链接改新路径(等价)
- [x] docs/tasks/job/ — 2026-09-17 — 通过;组索引一致
- [x] docs/tasks/job/automated-tasks-with-cron-jobs/ — 2026-09-17 — 通过;3 h2、13 代码块一致
- [x] docs/tasks/job/coarse-parallel-processing-work-queue/ — 2026-09-17 — 通过;9 h2、27 代码块一致
- [x] docs/tasks/job/fine-parallel-processing-work-queue/ — 2026-09-17 — 通过;7 h2+1 h3、15 代码块一致
- [x] docs/tasks/job/indexed-parallel-processing-static/ — 2026-09-17 — 通过;4 h2、9 代码块一致
- [x] docs/tasks/job/job-with-pod-to-pod-communication/ — 2026-09-17 — 通过;2 h2+1 h3、4 代码块一致
- [x] docs/tasks/job/parallel-processing-expansion/ — 2026-09-17 — 通过;6 h2+4 h3、20 代码块一致
- [x] docs/tasks/job/pod-failure-policy/ — 2026-09-17 — 通过;3 h2+4 h3+4 h4、28 代码块、failurePolicy 场景一致
- [x] docs/tasks/manage-daemon/ — 2026-09-17 — 通过;组索引一致
- [x] docs/tasks/manage-daemon/create-daemon-set/ — 2026-09-17 — 通过;4 h2、6 代码块一致
- [x] docs/tasks/manage-daemon/pods-some-nodes/ — 2026-09-17 — 通过;2 h2+3 h3、5 代码块一致
- [x] docs/tasks/manage-daemon/rollback-daemon-set/ — 2026-09-17 — 通过;4 h2+3 h3、10 代码块一致
- [x] docs/tasks/manage-daemon/update-daemon-set/ — 2026-09-17 — 通过;
      6 h2+5 h3+5 h4、14 代码块;DaemonSet 链接改新路径(workload-resources→apps,_redirects 577 等价)
- [x] docs/tasks/manage-gpus/scheduling-gpus/ — 2026-09-17 — 通过;设备插件/GPU 调度说明一致
- [x] docs/tasks/manage-hugepages/scheduling-hugepages/ — 2026-09-17 — 通过;HugePages 资源说明一致
- [x] docs/tasks/manage-kubernetes-objects/ — 2026-09-17 — 通过;组索引一致
- [x] docs/tasks/manage-kubernetes-objects/declarative-config/ — 2026-09-17 — 问题(链接悬空):
      md 含 reference/kubectl/# 悬空锚链接;HTML 为 kubectl-commands;
      其余 13 h2+12 h3+3 h4、38 代码块、2 表一致
- [x] docs/tasks/manage-kubernetes-objects/imperative-command/ — 2026-09-17 — 问题(链接悬空):
      同上悬空锚模式;其余 9 h2、5 代码块一致
- [x] docs/tasks/manage-kubernetes-objects/imperative-config/ — 2026-09-17 — 问题(链接悬空):
      同上;其余 11 h2、5 代码块一致
- [x] docs/tasks/manage-kubernetes-objects/kustomization/ — 2026-09-17 — 问题(链接悬空):
      同上;其余 6 h2+3 h3+5 h4、41 代码块、1 表一致
- [x] docs/tasks/manage-kubernetes-objects/storage-version-migration/ — 2026-09-17 — 通过;
      3 h2、26 代码块、feature-state(Beta StorageVersionMigrator)一致
- [x] docs/tasks/manage-kubernetes-objects/update-api-object-kubectl-patch/ — 2026-09-17 — 问题(链接悬空):
      同上;其余 6 h2+4 h3、48 代码块、1 表一致
- [x] docs/tasks/network/ — 2026-09-17 — 通过;组索引一致
- [x] docs/tasks/network/customize-hosts-file-for-pods/ — 2026-09-17 — 通过;3 h2、13 代码块一致
- [x] docs/tasks/network/extend-service-ip-ranges/ — 2026-09-17 — 通过;
      4 h2+3 h3+2 h4、23 代码块、feature-state(Stable MultiCIDRServiceAllocator)一致
- [x] docs/tasks/network/reconfigure-default-service-ip-ranges/ — 2026-09-17 — 通过;
      3 h2+3 h3、feature-state、迁移步骤一致
- [x] docs/tasks/network/validate-dual-stack/ — 2026-09-17 — 通过;dual-stack 验证步骤一致
- [x] docs/tasks/run-application/ — 2026-09-17 — 通过;组索引一致
- [x] docs/tasks/run-application/access-api-from-pod/ — 2026-09-17 — 通过;2 h2+4 h3、2 代码块一致
- [x] docs/tasks/run-application/configure-pdb/ — 2026-09-17 — 通过;9 h2+2 h3、9 代码块一致
- [x] docs/tasks/run-application/delete-stateful-set/ — 2026-09-17 — 通过;3 h2+3 h3、6 代码块一致
- [x] docs/tasks/run-application/force-delete-stateful-set-pod/ — 2026-09-17 — 通过;4 h2+1 h3、4 代码块一致
- [x] docs/tasks/run-application/horizontal-pod-autoscale-walkthrough/ — 2026-09-17 — 通过;
      9 h2+3 h3、31 代码块、HPA 演练(php-apache)一致
- [x] docs/tasks/run-application/run-replicated-stateful-application/ — 2026-09-17 — 通过;9 h2+9 h3、37 代码块一致
- [x] docs/tasks/run-application/run-single-instance-stateful-application/ — 2026-09-17 — 通过;7 h2、13 代码块一致
- [x] docs/tasks/run-application/run-stateless-application-deployment/ — 2026-09-17 — 通过;8 h2、15 代码块一致
- [x] docs/tasks/run-application/scale-deployment/ — 2026-09-17 — 通过;
      9 h2+4 h3、20 代码块、1 表;管理链接改新路径(manage-deployment→workloads/management,486 等价)
- [x] docs/tasks/run-application/scale-stateful-set/ — 2026-09-17 — 通过;4 h2+3 h3、5 代码块一致
- [x] docs/tasks/run-application/update-deployment-rolling/ — 2026-09-17 — 通过;9 h2+9 h3、30 代码块、1 表一致
- [x] docs/tasks/tls/ — 2026-09-17 — 通过;组索引一致
- [x] docs/tasks/tls/certificate-issue-client-csr/ — 2026-09-17 — 通过;
      9 h2、17 代码块;CSR 链接改新路径(_redirects 等价)
- [x] docs/tasks/tls/certificate-rotation/ — 2026-09-17 — 通过;4 h2、1 代码块一致
- [x] docs/tasks/tls/managing-tls-in-a-cluster/ — 2026-09-17 — 通过;10 h2+3 h3、22 代码块一致
- [x] docs/tasks/tls/manual-rotation-of-ca-certificates/ — 2026-09-17 — 通过;2 h2、5 代码块(含 daemonset 遍历脚本)一致
- [x] docs/tasks/tools/ — 2026-09-17 — 通过;4 h2、kubectl/kind/minikube/ctlptl 说明一致
- [x] docs/tasks/tools/install-kubectl-linux/ — 2026-09-17 — 通过;5 h2+10 h3+1 h4、46 代码块、4 种包管理器一致
- [x] docs/tasks/tools/install-kubectl-macos/ — 2026-09-17 — 通过;5 h2+14 h3、47 代码块一致
- [x] docs/tasks/tools/install-kubectl-windows/ — 2026-09-17 — 通过;5 h2+6 h3、25 代码块一致
- [x] docs/test/ — 2026-09-17 — 轻微问题:
      样式测试页;2 个演示小标题(Shutdown of interactive tutorials/Pencil icon)HTML 中为
      shortcode 内的粗体行,md 呈 #### 标题(11 h2/11 h3、18/18 代码块、2+1 引用内表格均一致)
- [x] docs/tutorials/ — 2026-09-17 — 通过;9 个 h3 索引条目一致
- [x] docs/tutorials/cluster-management/ — 2026-09-17 — 通过;组索引一致
- [x] docs/tutorials/cluster-management/admission-policies/ — 2026-09-17 — 通过;5 h2+9 h3+2 h4、17 代码块一致
- [x] docs/tutorials/cluster-management/install-use-dra/ — 2026-09-17 — 通过;
      7 h2+8 h3、45 代码块、feature-state(Stable DRA)一致;
      resource-slice 链接为 API 组新路径(存在)
- [x] docs/tutorials/cluster-management/kubelet-standalone/ — 2026-09-17 — 通过;9 h2+10 h3、41 代码块一致
- [x] docs/tutorials/cluster-management/namespaces-walkthrough/ — 2026-09-17 — 通过;5 h2、30 代码块一致
- [x] docs/tutorials/cluster-management/provision-swap-memory/ — 2026-09-17 — 通过;3 h2+2 h3+2 h4、6 代码块一致
- [x] docs/tutorials/cluster-management/use-pod-level-resource-managers/ — 2026-09-17 — 通过;
      8 h2+8 h3、17 代码块、feature-state(Beta PodLevelResourceManagers)与示例注解一致
- [x] docs/tutorials/configuration/ — 2026-09-17 — 通过;组索引一致
- [x] docs/tutorials/configuration/configure-persistent-volume-storage/ — 2026-09-17 — 通过;
      10 h2+4 h3、36 代码块一致(HTML 多 1 个非代码 pre 装饰,无内容差异)
- [x] docs/tutorials/configuration/configure-redis-using-configmap/ — 2026-09-17 — 通过;4 h2、28 代码块一致
- [x] docs/tutorials/configuration/pod-sidecar-containers/ — 2026-09-17 — 通过;6 h2+3 h3、3 代码块一致
- [x] docs/tutorials/configuration/updating-configuration-via-a-configmap/ — 2026-09-17 — 通过;9 h2、80 代码块一致
- [x] docs/tutorials/hello-minikube/ — 2026-09-17 — 通过;11 h2、33 代码块一致
- [x] docs/tutorials/kubernetes-basics/ — 2026-09-17 — 通过;
      6 个模块卡片(内嵌 module_0X.svg 图)一致,图片资产已复制到
      documents/docs/tutorials/kubernetes-basics/public/images/
- [x] docs/tutorials/kubernetes-basics/create-cluster/ — 2026-09-17 — 通过;模块索引一致
- [x] docs/tutorials/kubernetes-basics/create-cluster/cluster-intro/ — 2026-09-17 — 通过;5 h2+1 h3、2 代码块、module_01_cluster.svg 一致
- [x] docs/tutorials/kubernetes-basics/deploy-app/ — 2026-09-17 — 通过;模块索引一致
- [x] docs/tutorials/kubernetes-basics/deploy-app/deploy-intro/ — 2026-09-17 — 通过;5 h2+3 h3、6 代码块、module_02_first_app.svg 一致
- [x] docs/tutorials/kubernetes-basics/explore/ — 2026-09-17 — 通过;模块索引一致
- [x] docs/tutorials/kubernetes-basics/explore/explore-intro/ — 2026-09-17 — 通过;6 h2+5 h3、9 代码块、pods/nodes 图一致
- [x] docs/tutorials/kubernetes-basics/expose/ — 2026-09-17 — 通过;模块索引一致
- [x] docs/tutorials/kubernetes-basics/expose/expose-intro/ — 2026-09-17 — 通过;5 h2+3 h3、20 代码块、labels 图一致
- [x] docs/tutorials/kubernetes-basics/scale/ — 2026-09-17 — 通过;模块索引一致
- [x] docs/tutorials/kubernetes-basics/scale/scale-intro/ — 2026-09-17 — 通过;5 h2+3 h3、18 代码块、scaling 图×2 一致
- [x] docs/tutorials/kubernetes-basics/update/ — 2026-09-17 — 通过;模块索引一致
- [x] docs/tutorials/kubernetes-basics/update/update-intro/ — 2026-09-17 — 通过;5 h2+3 h3、18 代码块一致
- [x] docs/tutorials/security/ — 2026-09-17 — 通过;组索引一致
- [x] docs/tutorials/security/apparmor/ — 2026-09-17 — 通过;8 h2+2 h3、19 代码块一致
- [x] docs/tutorials/security/cluster-level-pss/ — 2026-09-17 — 通过;
      5 h2、23 代码块;HTML 的 3 个 <h4>Note 为提示框标题,md 以 [!NOTE] 呈现,文字完整
- [x] docs/tutorials/security/ns-level-pss/ — 2026-09-17 — 通过;7 h2、13 代码块、Note 提示框同上呈 [!NOTE]
- [x] docs/tutorials/security/seccomp/ — 2026-09-17 — 通过;10 h2、49 代码块一致
- [x] docs/tutorials/services/ — 2026-09-17 — 通过;组索引一致
- [x] docs/tutorials/services/connect-applications-service/ — 2026-09-17 — 通过;7 h2+2 h3、52 代码块一致
- [x] docs/tutorials/services/pods-and-endpoint-termination-flow/ — 2026-09-17 — 通过;3 h2、12 代码块一致
- [x] docs/tutorials/services/source-ip/ — 2026-09-17 — 通过;
      8 h2+2 h3、44 代码块;mermaid.live 外链图与本地 nodePort 图(md 引用
      docs/images/tutor-service-nodePort-fig01/02.svg,资产已复制)一致
- [x] docs/tutorials/stateful-application/ — 2026-09-17 — 通过;组索引一致
- [x] docs/tutorials/stateful-application/basic-stateful-set/ — 2026-09-17 — 问题(链接悬空):
      md [kubectl scale](…/reference/kubectl/#scale) 类锚链接指向无锚的 kubectl 索引页,
      HTML 为 kubectl-commands;其余 9 h2+13 h3+4 h4、136 代码块一致
- [x] docs/tutorials/stateful-application/cassandra/ — 2026-09-17 — 通过;9 h2+2 h3、21 代码块、1 表一致
- [x] docs/tutorials/stateful-application/mysql-wordpress-persistent-volume/ — 2026-09-17 — 通过;8 h2+1 h3、18 代码块一致
- [x] docs/tutorials/stateful-application/zookeeper/ — 2026-09-17 — 问题(链接悬空):
      md 悬空 kubectl 锚链接同上;其余 8 h2+11 h3、94 代码块一致
- [x] docs/tutorials/stateless-application/ — 2026-09-17 — 通过;组索引一致
- [x] docs/tutorials/stateless-application/expose-external-ip-address/ — 2026-09-17 — 通过;5 h2、15 代码块一致
- [x] docs/tutorials/stateless-application/guestbook/ — 2026-09-17 — 通过;7 h2+8 h3、39 代码块、redis 镜像一致

## 修复闭环(2026-09-17,docs-project-v10)

上述 854 页审查记录暴露的生成器缺陷已统一修复并升版 **docs-project-v10**,
本地 `docs-site/documents` 已重生成。按 TODO 1.6 规格:

- **①②④⑤(裸文本/行内碎片化)**:块级遍历改为"连续行内节点合段",h3 后裸文本、
  `div.lead` 副标题、`alert-secondary` 第三方 callout、`dd` 裸文本、metrics 的
  `metric_name`/`metric_help`、glossary "Also known as:" 全部恢复;警示框内
  裸文本+链接不再拆段。
- **自闭合锚**:`<a id="x" />` 解包为透明容器(授权页 "Warning:Enabling" 粘连消除,
  警示标题按约定丢弃);空文本锚(`[](#term-…)`)不再输出。
- **⑥(列表项块粘连/乱序)**:列表项内容按文档序渲染,代码围栏与警示框独立成行并
  按内容列缩进,嵌套列表保持原位(概念框 "add-on.1." 粘连、gang-scheduling 尾段
  合并、dual-stack/topology-spread 围栏粘连等全部消除)。
- **③(/blog/、/releases/ 等链接丢失)**:同源非 `/docs/` 路径(含重定向目标越出
  `/docs/` 树的 version-skew-policy)以 `-site-origin https://kubernetes.io` 拼成
  绝对外链保留;v9 全库 dropped 294 → v10 179,external 相应增加。
- **⑧**:重定向目标自带的锚点(`#TokenRequest`)保留,目标锚点优先。
- **glossary 术语(原已知问题 1)**:`a.glossary-tooltip` 渲染为纯术语文本,
  全库 294 页不再链接化。
- **空格**:U+00A0 等多字节空白边界按码点处理("step **2** in" 恢复);
  `sup`/`sub` 映射 Unicode 上/下标(`2²⁶`);metrics 徽章 `label`/`span` 相邻补空格。
- **⑪**:rowspan/colspan 降级表格输出 GFM 形态(表头+分隔行),可正常渲染。

复核确认的审查误报(未改,详见 TODO 1.6):networking 图片相对路径、workload-api
尾部列表标记、cpu-management-policies 链接改写均正确;`kubectl-commands#命令` 类
悬空锚为 `_redirects` 指定行为,与线上一致。表格 `<caption>` 不输出仍为已知限制。

v10 验证:854 页集合不变(digest 仅 `docs/home` 一页字节不变);双跑 `diff -r`
一致、workers 1/8 一致;资产集合不变;golden 按 v10 重生成;审查记录中的代表页
逐项复验通过。
