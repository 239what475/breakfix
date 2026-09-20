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

### 提交 1 docs-site.sh 环境变量覆盖 + 上游 canary workflow ✅ 9652187、6ef2b09

- [x] docs-site.sh：`DOCS_REVISION`/`DOCS_VERSION` 以 `:-` 默认值形式覆盖 manifest
      读取；调用方传 SHA（非分支名），原有的"checkout 后必须等于指定 revision"严格
      pin 校验原样生效——负向测试（假 SHA 直通 git fetch 被拒）与默认路径回归均过；
- [x] .github/workflows/docs-upstream-canary.yml：每周二 05:00 CST cron +
      workflow_dispatch 预检；ls-remote 解析最新 dev-* 分支 SHA → 覆盖变量跑
      `make docs-sync/docs-build/docs-project/docs-smoke`；缓存独立
      `DOCS_CACHE_DIR`、key 含解析出的 SHA；装 ripgrep；末步 always 输出 pin/canary
      对比结论；
- [x] 首跑实战即产出：dev-1.38 的构建与全量解析本就通过，但 smoke 测试硬编码的
      pinned 身份拒绝 canary 语料（身份断言先于任何真实兼容性信号失败）——修为同一
      覆盖约定（env 覆盖、pinned 值为默认，6ef2b09），本地默认路径回归绿。

### 提交 2 tag 保护（API，非仓库文件）✅ 2026-09-20

- [x] 计划修正：旧 `tags/protection` API 已 closing down（404），改用现行 rulesets
      API——ruleset `protect-release-tags`（id 23729630，target=tag，enforcement=
      active）：`refs/tags/v*` 的 creation/update/deletion 全限制，owner
      （bypass_mode=always）豁免；单 owner 仓库无法实测非 admin 被拒，按 ruleset
      语义非 bypass actor 建改删 v* 一律拒绝（含未来协作者）。

### 提交 3 major PR 评审报告（需用户决策）✅ 2026-09-20（评审即执行）

- [x] 报告产出并经用户批准按建议执行，7 个 major PR 落地：#7/#8/#9/#10（四个
      action 大版本均为 Node 20→24 迁移、输入输出无变化）、#13（@types/node 纯
      类型）、#14（lucide——**1.0.0 为官方误发布，recreate 重定 1.0.1 后合并**）、
      #12（markdown-it 15：含 smartquotes DoS 安全修复；v15 自带类型声明，按预测
      需伴随提交——删 @types/markdown-it、Token 导入改主入口命名导出，伴随提交推
      PR 分支内部，本地 build+20 用例绿后 CI 复验合并）；
- [x] #9/#13/#14 与先行合并的 PR 冲突（同文件/lockfile 基底变化），@dependabot
      rebase 自愈后合并——major 流程的冲突处理路径实证；
- [x] **#15 typescript 7 判决：挂起等上游**——本地实测 vue-tsc 3.3.11（已是最新）
      对 TS7 启动即崩（typescript/lib/tsc 子路径在 Go 原生包 exports 中已移除），
      生态尚无兼容版；PR 留队列，等 vuejs/language-tools 跟进后 Dependabot 自动
      重开。方法论修正（用户提出）：兼容性判断本地先行（0.5 秒出答案），CI 只做
      权威确认；
- [x] A 组验证：v0.0.0-rc.2 一次性 tag 在 login v4/buildx v4/gh-release v3 下
      发布链全绿，产物四引用 digest 化、0 :dev 残留，release/tag 已删；删除 tag
      时 ruleset 实证生效（"Cannot delete this tag" 仅 owner bypass 放行）；
- [x] 遗留清偿：#1–#4 经验证目标版本均已在 main（kin-openapi 0.149>0.144、incus
      7.4>7.2、nanoid/postcss 原样送达），以 owner 身份关闭并留说明；队列唯一留存
      为刻意挂起的 #15；js-yaml ×3 告警等周更分组更新。

### 提交 4 ghcr 收尾（首次真实发布前完成）

- [x] 四个包已为 public——实测纠正计划假设：与公开仓库关联的包继承 public 可见性
      （非旧的"GITHUB_TOKEN 默认 private"规则）；四个包匿名 manifest 拉取全部 200，
      首次正式发布即可无凭证直拉，无需任何操作；
- [x] 包内残留的 v0.0.0-rc.1/rc.2 测试版本已由用户网页删除（2026-09-20）。

### 验收（提交 1、2 于 2026-09-20 回填）

- [x] canary 首跑有结论 ✅：对 dev-1.38（cedecba）全链绿——sync、hugo 0.144.2
      构建（无需升 hugo）、全量语料解析、目录树校验、身份+语义 smoke 全部通过；
      **下次 manifest bump 是例行操作**。附注：bump 时需同步改 manifest 的
      revision/version 与 smoke 测试默认值（两处 pin 拷贝）；
- [x] workflow_dispatch 手动预检路径可用（两次 dispatch 实测，二次因缓存命中
      缩短至 ~6 分钟）；
- [x] tag 保护生效（ruleset active，配置经 GET 复核）；
- [x] major PR 评审报告产出并交付用户 ✅（评审即执行，7 合 1 挂起）；
- [x] ghcr 可见性已实证为 public（匿名 200）；测试版本已清理（用户网页操作）。

### 已知后续（不属本阶段）

- 剩余 js-yaml ×3 告警等 Dependabot 下周分组更新自然清偿；quic-go（#18）在途；
- ollama critical 告警无修复版，只能等上游；
- 首次真实发布后部署侧验证拉取（包 public 化后应无凭证直拉）。

## fixture 退役：文档/admin 套件转 live 验收（当前阶段，2026-09-20 立项）

背景：document-agent-fixture（e2e 内确定性假模型服务）经用户决策退役（2026-09-20，
"这个设计没有意义"）。理由与代价已向用户说明：其他 agent 套件本就是 live-only 模式，
文档流水线是唯一进 CI 的 agent 特性；退役后 nightly 不再覆盖文档实践流水线的
batch/门禁/发布链，该覆盖转由本地 live 验收承担。

- [x] 删除 fixture 三件套 + .gitleaks.toml allowlist ✅ 994ec0c；孤儿 e2e-reset-database.sh
      一并移除，document-agent-fixture/documentation-fixture-key 加入 verify-legacy-removal
      防复活清单；
- [x] e2e-documentation-prepare.sh ✅：三处补丁移除；前置校验拒绝空值与三个占位
      （unused-in-core-profile、unused-until-documentation-prepare、
      placeholder-replaced-by-prepare——本地 target 快照实测占位即拒）；库生成/
      ConfigMap/volume 换源/catalog 等待全部保留；
- [x] run-e2e.sh ✅：documentation/admin 加 RUN_AGENT_LIVE_E2E=1 门禁，与其他 agent
      套件同款，不限剖面；
- [x] run-e2e-regression.sh ✅：标准 prepare + k8s → ui（+full 的 node、recovery）；
- [x] e2e-bootstrap-core.sh 假 key 更名 unused-in-core-profile（CI 永不用真实模型）；
- [x] testing.md 六处同步 ✅（分层表、剖面表、编排段、live 清单+key 前置校验说明、
      nightly 边界、本地相关性表）。

### 验收（2026-09-20 回填）

- [x] 凭证通道 ✅（机制经两轮实测收敛）：导出 `DEEPSEEK_API_KEY` → prepare 注入
      runtime Secret。首版注入位置在 deferred prepare 之前，被 prepare 内部 reset 的
      快照还原覆盖（模型带占位 key 出去，401）；移到 reset 之后、唯一一次 Server
      rollout 之前（fix 于同日提交），401 消失、真实模型调用打通——值不进仓库；
- [ ] live documentation 套件：机制已通，但**真模型暴露产品 bug**——review agent 的
      submit_document_review 工具结果连续 8 次被拒（"invalid role or decision"），
      纠偏反馈不收敛，循环至 ChatModel 迭代上限（"exceeds max iterations"）；
      fixture 罐头输出永远合法，完全遮蔽此缺陷。第二个失败（"admin role required"）
      为级联（首个 spec 中途失败改变注册序）。**转产品缺陷修复**：review 工具的
      role/decision 契约解析或纠偏反馈需对真实模型输出鲁棒，修复合后重跑验收；
- [ ] RUN_AGENT_LIVE_E2E=1 make test-e2e-admin：同因挂起；
- [x] 回归编排 full 剖面四套件绿 ✅：k8s、ui 链内通过；node 首跑被外部 rollout 干扰
      失败（用户并行会话同时操作同一 target，controller 被链外重启，与本次改动无关），
      空闲后单独复跑 3/3 绿；recovery 2/2 绿；集群内 19h 遗留 fixture Deployment 已清；
- [x] 占位 key 拒绝路径实测生效；无门禁变量的拒绝为与 agent-assistant 等五处已验
      门禁完全同构的两行守卫；
- [x] 快车道五 job 全绿（run 35520691719）；nightly 配置无已删路径引用，当晚 nightly
      将按 gate 自动跑新链。

## 挂起待决策（不排期）

- **内容治理/紧急下架**：场景侧非 authoring 内容无法下架、无管理员覆盖;实践内容
  PracticeRevision 一侧 append-only(索引只进不改)是刻意设计,与"紧急摘除"冲突。若做,
  方向是索引摘除/tombstone 而非删除数据——独立设计决策后另行立项,当前不做。
