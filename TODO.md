# TODO

已完成的阶段见 git 历史（最近：CI 快车道与按需 nightly，验收章见 dcaac06；E2E 剖面化
core/full 见 aaaf01d、bf766e5、1446833）；本文件保留当前阶段与未立项事项。

## 工程缺口修复：release 链与安全卫生（当前阶段，2026-09-20 立项）

背景：仓库转公开后的缺口清点（2026-09-20）确认七项，本阶段全部落地。转公开前的全历史
gitleaks 扫描（392 提交 7 命中均良性并 allowlist）是本阶段密钥防护的基线。

- [x] 仓库安全开关（网页设置项，经 API 开启，非仓库文件）：secret scanning、push
      protection、dependabot security updates 三项 enabled——GitHub API 现只接受
      `{"status":"enabled"}` 嵌套形态，旧文档的裸字符串形态一律 422。开启即回扫全部
      历史：0 告警，与 gitleaks 结论互相印证。non-provider patterns 与 validity
      checks 留关：前者对测试密集仓库（fixture 占位密钥多）噪音大于收益，后者只在
      有检出密钥时才有意义，当前 0 检出空转。Dependabot 索引完成后补发 26 条依赖
      告警（Go 19：4 critical；npm 8），自动修复 PR 已排队（见下方"后续"）；
- [x] **release 链修复（最高优先）** ✅ 1e0bfc2：server 清单以 image volume 挂载
      `ghcr.io/breakfix/breakfix-documentation-library:dev`，原流程只替换三个运行时
      组件且从不构建/推送库镜像——按发布清单部署直接 ImagePullBackOff。修复：release
      从 pinned 站点构建库镜像（nightly 同款 docs-sync/build 链 + .local/docs 缓存 +
      ripgrep），四组件统一 push/digest/sed，`! grep` 断言扩展到 library；
- [x] dependabot 版本更新 ✅ 05f74a7：.github/dependabot.yml（github-actions/gomod/
      npm×2，周更，minor+patch 按生态分组，major 不分组单独评审）；
- [x] nightly 补 docs 语义校验 ✅ 7192072：`make docs-smoke`（依赖链内含 docs-check）
      挂在库镜像构建之后，复用该步已产出的全量语料；
- [x] 快车道挂 gitleaks ✅ 505ce61：secrets-scan job（fetch-depth: 0，
      gitleaks-action@v2 沿用 .gitleaks.toml allowlist）；
- [x] test-race ✅ 505ce61：DB-free race 扫描（`env -u BREAKFIX_TEST_DATABASE_URL`，
      DB 套件自跳过），独立 go-race job 并行；
- [x] README 验证小节补 CI 分工 ✅ da0ebb2。

### 验收（2026-09-20 回填）

- [x] 快车道含 secrets-scan 与 go-race 全绿（run 35509688964：五 job，secrets-scan
      首跑即绿，go-race 与 go-test 并行不占 PG service）；
- [x] nightly 含 docs-smoke 全绿（run 35509845294：gate/core/vk8s，日志确认
      "passed official public tree checks" + TestPinnedKubernetesPodLifecycle ok）；
- [x] release 链以一次性 tag 真实走通 ✅（run 35509870235，自 6 月创建以来首次
      执行即绿）：四镜像入 ghcr.io/239what475/*，发布产物四个镜像引用全部 digest
      化（含库镜像 image volume reference），0 `:dev` 残留；测试 release/tag 已删，
      ghcr 测试版本因 token 无 delete:packages scope 残留（private 无害）；
- [x] make test-race 本地绿（44 包，DB 套件自跳）。

### 后续（新阶段候选，不属本阶段）

- **Dependabot 告警清偿**：26 条依赖告警（4 critical：kin-openapi≤0.143.0、pgx/v5
  <5.9.0 ×2、ollama 无修复版），15 个修复/更新 PR 已排队（#1–#15）；Go 侧升级需
  跑回归，npm 侧多为构建链；
- GITHUB_TOKEN 推送的 ghcr 包默认 private，首次真实发布后部署侧需配拉取凭证或调
  整包可见性；ghcr 里的 v0.0.0-rc.1 测试版本可网页删除。

## 挂起待决策（不排期）

- **内容治理/紧急下架**：场景侧非 authoring 内容无法下架、无管理员覆盖;实践内容
  PracticeRevision 一侧 append-only(索引只进不改)是刻意设计,与"紧急摘除"冲突。若做,
  方向是索引摘除/tombstone 而非删除数据——独立设计决策后另行立项,当前不做。
