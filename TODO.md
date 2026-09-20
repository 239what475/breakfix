# TODO

已完成的阶段见 git 历史（最近：PR 门禁与 Dependabot 自动合并，验收章见 3b2c47d；工程
缺口修复含 release 链真实走通见 1b7c94e；CI 快车道与按需 nightly 见 dcaac06；E2E 剖面
化见 aaaf01d、bf766e5、1446833）；本文件保留当前阶段与未立项事项。

## docs 上游 canary 与发布前收尾（当前阶段，2026-09-20 立项）

背景：nightly 已覆盖 pinned 文档链的全量真实解析（sync→build→docs-project→check→
smoke，有提交必跑 + 周日兜底），但 manifest 钉死单个 SHA，**没有任何信号告诉我们下一
次 bump 时解析器还能不能活**——k8s website 改版会让升级从例行变成带压抢修。同时发布
链已被证明"真的会发东西"，发布前收尾项也该排上。

关键决策（2026-09-20 与用户确认方向）：

- canary 是**前瞻预警**不是门禁：独立 workflow、失败语义独立（红 = "下次 bump 会痛"，
  不阻塞 nightly、不代表 main 坏了）；
- "最新上游"取 kubernetes/website 最新 `dev-*` release 分支的 SHA（bump 的现实目标），
  hugo 版本保持钉住——若最新分支需要更新 hugo 才能构建，canary 失败同样是有效信号；
- 镜像发布物 = 公开代码构建产物，ghcr 包倾向 public（正式发布前与用户最终确认）。

提交拆解：

### 提交 1 docs-site.sh 环境变量覆盖 + 上游 canary workflow

- [ ] docs-site.sh：`DOCS_REVISION`/`DOCS_VERSION` 以 `:-` 默认值形式覆盖 manifest
      读取；调用方传 SHA（非分支名），原有的"checkout 后必须等于指定 revision"严格
      pin 校验原样生效；
- [ ] .github/workflows/docs-upstream-canary.yml：每周一 cron（错开 nightly）+
      workflow_dispatch（手动 bump 前预检）；步骤 = ls-remote 解析最新 dev-* 分支
      SHA → 覆盖变量跑 `make docs-sync/docs-build/docs-project/docs-smoke`（内含
      check）→ 失败输出"pinned 可用、latest 差距"的对比结论；缓存走独立
      `DOCS_CACHE_DIR`、key 含解析出的 SHA（重跑便宜、自然轮换）；装 ripgrep。

### 提交 2 tag 保护规则（API，非仓库文件）

- [ ] `v*` tag 仅 admin 可推——防手滑 tag 直接触发真实发布（推镜像 + 建公开
      Release）；一行 API，用后验证非 admin 被拒。

### 提交 3 major PR 评审报告（需用户决策）

- [ ] 8 个 major PR 出评审报告：四个 action 主版本（login/buildx/gitleaks/
      gh-release）属机械升级；四个 npm major 需看 breaking changes——typescript 7
      （动整个 web 工具链）、markdown-it 15、lucide-vue-next 1.0、@types/node 26；
      给出风险与建议合并顺序，用户拍板后执行（#15 typescript 现为冲突态，评审时
      @dependabot rebase）。

### 提交 4 ghcr 收尾（首次真实发布前完成）

- [ ] 四个包可见性定 public（用户点头后 API 执行）；
- [ ] 网页删除 v0.0.0-rc.1 测试版本（API 无 delete:packages scope，网页 Packages
      页可删）。

### 验收

- [ ] canary 首跑有结论：最新 dev-* 分支全量解析通过（绿），或产出明确的前瞻差距
      报告（哪些环节断、预估 bump 成本）；
- [ ] workflow_dispatch 手动预检路径可用（未来 bump 前先点一次）；
- [ ] tag 保护生效；
- [ ] major PR 评审报告产出并交付用户；
- [ ] ghcr 可见性与测试版本清理完成。

### 已知后续（不属本阶段）

- 剩余 js-yaml ×3 告警等 Dependabot 下周分组更新自然清偿；quic-go（#18）在途；
- ollama critical 告警无修复版，只能等上游；
- 首次真实发布后部署侧验证拉取（包 public 化后应无凭证直拉）。

## 挂起待决策（不排期）

- **内容治理/紧急下架**：场景侧非 authoring 内容无法下架、无管理员覆盖;实践内容
  PracticeRevision 一侧 append-only(索引只进不改)是刻意设计,与"紧急摘除"冲突。若做,
  方向是索引摘除/tombstone 而非删除数据——独立设计决策后另行立项,当前不做。
