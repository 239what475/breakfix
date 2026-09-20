# TODO

已完成的阶段见 git 历史（最近：工程缺口修复含 release 链真实走通，验收章见 1b7c94e；
CI 快车道与按需 nightly 见 dcaac06；E2E 剖面化见 aaaf01d、bf766e5、1446833）；本文件
保留当前阶段与未立项事项。

## PR 门禁与 Dependabot 自动合并（当前阶段，2026-09-20 立项）

背景：仓库转公开 + dependabot.yml 落地后，Dependabot 成为第一个"协作者"（15 个 PR 排
队，含 4 个 critical 依赖告警的修复），而 CI 设计期"无 PR 门禁"的前提（单人直推）不再
完全成立：Dependabot PR 不跑任何检查，盲合并 = 未验证的依赖变更直接进 main。ci.yml 头
注释当时已预留此演进点；GitHub 同时提示 main 未保护（force push/删除无拦截）。

关键决策（2026-09-20 与用户确认，完整版）：

- **门禁加给 PR（主要是机器人），不加给人**：enforce_admins=false，owner 直推 main 完全
  不受影响，也无须自审自批；
- 五个快车道 job 设为 required checks（strict=false 不强制 branch up to date——否则 15
  个 go.mod PR 串行长尾；Dependabot 冲突时自动 rebase 自愈）；
- **自动合并范围 = Dependabot 非 major 更新**：fetch-metadata 判别 + 默认校验 PR head
  确为 Dependabot 签名提交（伪造骗不过）；分组 PR 按构造只含 minor/patch；安全修复多为
  minor 正好自动流入；未来只能跨 major 的安全修复留在队列等人（安全优先于及时）；
- required review 不做（单人仓库自审无意义）；live acceptance/Incus e2e 依旧不进 CI。

设计形态：

```
Dependabot 开 PR ──→ 快车道五 job（pull_request 触发，job 本体不动）
                        │全绿                          │major / 检查挂
                        ▼                              ▼
        非 major → --auto 登记（仅登记，等保护规则放行）   留在队列等人
        → squash 合并 → 删分支 → main push → 快车道幂等重跑
        → 当晚 nightly 因有新提交自动全量回归
你直推 main ──→ 一切如旧（admin 豁免，快车道照跑）
```

提交拆解：

### 提交 1 ci: pull_request trigger and dependabot auto-merge ✅ 9862cab

- [x] ci.yml 加 `pull_request: branches: [main]`（并发组按 PR ref 天然隔离）；
- [x] 新增 dependabot-auto-merge.yml（仅 dependabot[bot]、仅非 major、squash、
      仅 contents:write）。

### 提交 2 仓库设置与分支保护（API，非仓库文件）✅ 2026-09-20

- [x] allow_auto_merge=true、delete_branch_on_merge=true；
- [x] main 分支保护：五 job required checks、strict=false、enforce_admins=false、
      阻止 force push/删除、无 review 要求——GET 复核六项全部按设计生效。

### 存量引导 ✅ 2026-09-20

- [x] 7 个非 major PR 评论 @dependabot rebase；实施中发现 GitHub 防递归设计的
      **修正项**：GITHUB_TOKEN 身份的合并不触发 main 的 push 工作流（快车道不会在
      bot 合并后重跑），覆盖等价性由两处保证——PR 检查本就跑在 merge ref（PR 分支
      +最新 main）上，nightly 的 sha-gate 走 schedule 事件不受此限制，当晚照常全量
      回归；owner 直推的快车道照旧；
- [x] 8 个 major PR 留人工评审（其中 #15 typescript 因 #11 先合并暂为冲突态，
      评审时 @dependabot rebase 即可）。

### 验收（2026-09-20 回填）

- [x] patch/组 PR 全链走通 ×4：#11 web 组、#5 test 组、#6 go 组（13 项）、#16
      pgx critical——rebase → 五 job 绿 → workflow 登记 auto-merge → 保护规则放行
      → squash 合并 → 分支删除，全程无人干预；依赖告警 26 → 5，critical 清零；
      新到修复（#17 jsonparser high）自动带 auto 登记流入；
- [x] major PR 不自动合并（#7–#10、#12–#15 全部无 auto 登记）；
- [x] owner 直推 main 无感（本盖章提交即保护生效后的直推实测）；
- [x] main 保护生效：required checks 实测拦截（合并前全部 BLOCKED）、force push/
      删除已禁、"main isn't protected" 横幅消除。

### 已知后续（不属本阶段）

- major PR（typescript 7、lucide 1.0、@types/node 26、markdown-it 15、四个 action
  主版本）需人工评审分批合；
- 剩余 5 条开放告警待 #3/#4/#17/#18 合并及 ollama 上游出修复版后自然清偿；
- GITHUB_TOKEN 推送的 ghcr 包默认 private，首次真实发布后部署侧需配拉取凭证或调可见
  性；ghcr 里 v0.0.0-rc.1 测试版本可网页删除。

## 挂起待决策（不排期）

- **内容治理/紧急下架**：场景侧非 authoring 内容无法下架、无管理员覆盖;实践内容
  PracticeRevision 一侧 append-only(索引只进不改)是刻意设计,与"紧急摘除"冲突。若做,
  方向是索引摘除/tombstone 而非删除数据——独立设计决策后另行立项,当前不做。
