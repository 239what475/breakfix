# TODO

已完成的阶段见 git 历史（最近：E2E 剖面化 core/full 与回归编排/bootstrap，验收章见
aaaf01d、bf766e5、1446833）；本文件保留当前阶段与未立项事项。

## CI：push 快车道 + 按需 nightly（当前阶段，2026-09-20 立项）

背景：剖面化与 bootstrap 落地后，托管 runner 上已无任何需要特殊引导的环节，CI 重写
只剩 workflow 本身。本仓库单人直推 main，无协作者，PR 门禁语义不存在（2026-09-20 与
用户确认）；将来有协作者时把触发器从 push 扩到 pull_request 即可，job 本体不动。

关键决策（2026-09-20 与用户确认）：

- **无 PR**：快车道由 push 到 main 触发，作为干净环境兜底（本地绿 ≠ CI 绿：工具链
  漂移、缓存污染、漏跑入口），不做分支保护/required checks；
- **nightly 按需**：gate 比较当前 main HEAD 与 nightly workflow 上一次成功运行的
  head_sha，相同则跳过全部重 job——无提交的夜晚零成本；nightly 失败后无新提交也会
  自动重试（上次成功 sha 停留在更早处）；周日额外无条件跑一次（外部漂移兜底：浮动
  tag 基础镜像、runner 镜像更新），workflow_dispatch 手动强制；
- **e2e 托管形态**（已本地验证的入口，CI 直接调用）：
  `make e2e-bootstrap-core` + `BREAKFIX_E2E_PROFILE=core make test-e2e-regression`；
  node/recovery 留本地 full target，live acceptance 永不进 CI；
- **顺带修复现有 ci.yml 的已知问题**：golangci-lint-action v6 跑不了 v2 配置（升 v7
  并钉 lint 版本）、quality 与 build 两个 job 重复 `make build`、Vitest 层缺失、Go
  版本在 go.mod/ci/release 三处手工钉（改 `go-version-file: go.mod`）。

前置已定：仓库将由 private 转为 public（2026-09-20 与用户确认）。转公开前完成全历史
密钥扫描：gitleaks 覆盖 392 个提交，7 个命中经逐一甄别均为测试常量、fixture 占位值
或生成代码（taxonomy-e2e 测试键、mcp 幂等键、假 TOTP、内嵌 OpenAPI 规范、Makefile
生成路径变量），真实凭据只存在于 Kind 集群与环境，敏感路径（.local/、local 配置、
证书私钥）从未入库；已知良性模式已加 .gitleaks.toml allowlist，复扫归零。配额约束
随之解除，"周日无条件跑"与"每次 push 必跑快车道"两项保留。转公开后需在仓库设置
开启 push protection（GitHub 网页操作，非仓库文件）。

设计形态：

```
push 到 main ──→ 快车道（并发取消旧跑）
                   go-test（PG service + make test-unit）
                   web-test（make web-test-unit）
                   contracts-build（verify-generated + make build 唯一一次
                                    + kubectl kustomize 两套 + lint v7）
夜晚 ──→ gate：HEAD ≠ 上次成功 nightly 的 sha？
                   │是                          │否
                   ▼                            ▼
     e2e-core：bootstrap + core 全量回归        跳过
     docs：docs-sync/build/check/smoke
     vk8s：make test-vk8s-network
周日无条件跑 + workflow_dispatch 手动
```

提交拆解：

### 提交 1 ci: push fast lane replaces the PR pipeline ✅ 7ab88fb（+ 计划外 lint 烧债 688e8c9）

- [x] ci.yml 重写：push 触发（去 PR）、concurrency cancel、go-version-file、
      build 只跑一次、补 web-test、lint 升 action v7 钉 v2.13.2、各 job timeout-minutes；
- [x] release.yml 同步：go-version-file 收敛；
- [x] 计划外：修好的 lint 暴露 6 周存量（70 项，golangci 对同型消息去重后实为百余站点），
      全部清零——机械修复、11 处死代码删除、三处 eino 重试钩迁移 ShouldRetry、controller
      Requeue 弃用改固定步进、投影 chmod 与批次点火带显式 gosec 豁免、测试文件整体排除
      gosec、make lint 限定模块真实包；本地 v2.13.2 与 CI 双归零，test-unit 44 包绿；
      CI 首跑即绿（go-test 3m30s、web-test 17s、contracts-build 2m42s）。

### 提交 2 ci: gated nightly with the core regression ✅ 9ef62bf（调试期修补 7f34284、f39934a、a2016f6）

- [x] nightly.yml：gate job（API 查 nightly 上次成功 sha）+ core job（ripgrep、
      docs-sync/build/fixture、actions/cache 缓存 .local/docs、钉版 kind CLI 与
      Playwright chromium、清缓存后构建 documentation-library 镜像、空集群
      bootstrap + core 全量回归、失败上传 .local/e2e 诊断 artifact）+ vk8s job
      （钉版 kind/vcluster）；周日无条件仅对 schedule 生效，dispatch 走正常 gate，
      force 输入强制全跑；
- [x] 权限最小化（contents: read、actions: read 供 gate 查询）；
- [x] 调试期发现并修复三处：runner 无 ripgrep 且 verify-legacy-removal 在 rg 缺失时
      空真通过（Makefile 改为缺 rg 即失败，两个 workflow 显式安装）；根清单引用的
      documentation-library 镜像 ephemeral runner 没有（nightly 从 pinned 站点解析并
      构建真镜像，prune 顺序前置）；周日豁免误拦 dispatch 的 sha 判断（限 schedule）。

### 提交 3 docs: record the CI tiers and local relevance map ✅ 9ef62bf

- [x] docs/operations/testing.md 补"CI 与本地的分工"：push 快车道、按需 nightly 的
      gate 语义、永不进 CI 的边界，以及本地"变更区域 → 最相关测试"映射表。

验收（2026-09-20 回填）：

- [x] push 后快车道全绿（首次包含 Vitest 层；run 35503790139：go-test 3m30s、
      web-test 17s、contracts-build 2m42s 含 lint）；
- [x] workflow_dispatch 全量 nightly 在托管 runner 全绿（run 35505673068：core
      25m14s 含 docs 链 + 库镜像 + core 全量回归，vk8s 1m57s；后随修补两次复跑
      全绿，末次 24m15s）；
- [x] gate 语义：同 sha 二次 dispatch，core/vk8s 均 0 秒跳过（run 35508119362）；
      HEAD 移动与周日无条件分支亦分别实证；
- [x] make test-unit（44 包）、verify-generated、web-test-unit（20 用例）本地绿。

## 挂起待决策（不排期）

- **内容治理/紧急下架**：场景侧非 authoring 内容无法下架、无管理员覆盖;实践内容
  PracticeRevision 一侧 append-only(索引只进不改)是刻意设计,与"紧急摘除"冲突。若做,
  方向是索引摘除/tombstone 而非删除数据——独立设计决策后另行立项,当前不做。
