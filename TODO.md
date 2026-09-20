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

### 提交 1 ci: push fast lane replaces the PR pipeline

- [ ] ci.yml 重写：push 触发（去 PR）、concurrency cancel、go-version-file、
      build 只跑一次、补 web-test、lint 升 action v7 钉版本、各 job timeout-minutes；
- [ ] release.yml 同步：go-version-file 收敛。

### 提交 2 ci: gated nightly with the core regression

- [ ] nightly.yml：gate job（API 查上次成功 sha）+ e2e-core（装 kind、建临时集群、
      bootstrap、core 全量回归、失败上传 .local/e2e 诊断 artifact）+ docs 管线
      （actions/cache 缓存 .local/docs）+ vk8s；周日 schedule 不带 gate；
      workflow_dispatch 强制全跑；
- [ ] 权限最小化（contents: read、actions: read 供 gate 查询）。

### 提交 3 docs: record the CI tiers and local relevance map

- [ ] docs/operations/testing.md 补"本地只跑最相关测试"的变更区域 → 套件映射
      （web→vitest+ui；transport/handler→test-unit+admin/documentation；
      runtime/k8s→k8s；runtime/node→node；scripts/deploy→受影响套件），以及
      CI 层级（push 快车道 / 按需 nightly / 本地 full / 人工 live）的说明。

验收：

- [ ] push 后快车道全绿（首次包含 Vitest 层）；
- [ ] workflow_dispatch 强制 nightly：bootstrap + core 全量回归 + docs + vk8s 在
      托管 runner 全绿；
- [ ] gate 语义：同 sha 再次 dispatch 时重 job 被跳过；
- [ ] make test-unit、verify-generated、web-test-unit 本地绿（不回归）。

## 挂起待决策（不排期）

- **内容治理/紧急下架**：场景侧非 authoring 内容无法下架、无管理员覆盖;实践内容
  PracticeRevision 一侧 append-only(索引只进不改)是刻意设计,与"紧急摘除"冲突。若做,
  方向是索引摘除/tombstone 而非删除数据——独立设计决策后另行立项,当前不做。
