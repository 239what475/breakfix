# TODO

已完成的阶段见 git 历史（最近：CI 快车道与按需 nightly，验收章见 dcaac06；E2E 剖面化
core/full 见 aaaf01d、bf766e5、1446833）；本文件保留当前阶段与未立项事项。

## 工程缺口修复：release 链与安全卫生（当前阶段，2026-09-20 立项）

背景：仓库转公开后的缺口清点（2026-09-20）确认七项，本阶段全部落地。转公开前的全历史
gitleaks 扫描（392 提交 7 命中均良性并 allowlist）是本阶段密钥防护的基线。

- [x] 仓库安全开关（网页设置项，经 API 开启，非仓库文件）：secret scanning、push
      protection、dependabot security updates 三项 enabled——GitHub API 现只接受
      `{"status":"enabled"}` 嵌套形态，旧文档的裸字符串形态一律 422。开启即回扫全部
      历史：0 告警，与 gitleaks 结论互相印证；dependabot 亦 0 告警。non-provider
      patterns 与 validity checks 留关：前者对测试密集仓库（fixture 占位密钥多）噪音
      大于收益，后者只在有检出密钥时才有意义，当前 0 检出空转；
- [ ] **release 链修复（最高优先）**：server 清单以 image volume 挂载
      `ghcr.io/breakfix/breakfix-documentation-library:dev`（server.yaml:180 一带），
      而发布流程只对 server/controller/runtime-worker 三个组件做 sed→digest 替换，
      库镜像既不构建也不推送——按发布清单部署直接 ImagePullBackOff。修复：release
      从 pinned 站点构建库镜像（nightly 同款 docs-sync/build 链 + .local/docs 缓存 +
      ripgrep），四组件统一 push/digest/sed，`! grep` 断言扩展到 library；sed 覆盖度
      已本地实证（渲染产物 4 处 :dev 全替换、0 残留）。整条 release 链自 6 月创建以来
      无执行记录，验收要求以一次性 tag 真实走通；
- [ ] dependabot 版本更新：.github/dependabot.yml（github-actions/gomod/npm×2，
      周更，minor+patch 按生态分组，major 不分组单独评审）；
- [ ] nightly 补 docs 语义校验：`make docs-smoke`（依赖链内含 docs-check）挂在库镜像
      构建之后——库镜像那步已产出全量语料，smoke 只是复用它跑一个 Go 测试，近乎白送；
- [ ] 快车道挂 gitleaks：secrets-scan job（fetch-depth: 0，gitleaks-action@v2 沿用
      .gitleaks.toml allowlist），转公开后的新提交从此有自动密钥防护；
- [ ] test-race：DB-free race 扫描（`env -u BREAKFIX_TEST_DATABASE_URL`，DB 套件自
      跳过），独立 go-race job 并行跑，不占用 go-test 的 PG service；
- [ ] README 验证小节补一句 CI 分工（快车道/nightly 边界指向 testing.md）。

### 验收

- [ ] 快车道含 secrets-scan 与 go-race 全绿；
- [ ] nightly 含 docs-smoke 全绿；
- [ ] release 链以一次性 tag（如 v0.0.0-rc.1）真实走通：四镜像入 ghcr、发布产物
      四个镜像引用全部 digest 化且无 :dev 残留；测试 release/tag 事后清理（ghcr 包
      版本受 delete:packages scope 限制可能残留，无害）；
- [ ] 已知后续（不属本阶段）：GITHUB_TOKEN 推送的 ghcr 包默认 private，首次真实
      发布后部署侧需配置拉取凭证或调整包可见性。

## 挂起待决策（不排期）

- **内容治理/紧急下架**：场景侧非 authoring 内容无法下架、无管理员覆盖;实践内容
  PracticeRevision 一侧 append-only(索引只进不改)是刻意设计,与"紧急摘除"冲突。若做,
  方向是索引摘除/tombstone 而非删除数据——独立设计决策后另行立项,当前不做。
