# Agent Workflow — 自动生成题目

## 概览

用户先提交一个 challenge idea，系统先扩展为结构化 draft，再通过 K8s Job + 多 Agent 协作自动生成完整题目。

```
Web UI / API 提交 challenge idea
  → Agent 评审并扩展为 reviewed draft
  → Server 创建 K8s Job
  → Job Pod 内 Agent 生成、实验验证、正式验证
  → 成功后 Server 注册题目
  → "✓ 题目 cleanup-kubernetes-logs 已上线"
```

## 技术栈

| 层 | 选型 |
|------|------|
| Agent 框架 | eino (bytedance/cloudwego) |
| LLM 后端 | DeepSeek v4 pro（Anthropic 兼容接口） |
| CLI 引擎 | Claude Code（`claude` CLI 二进制） |
| 编排库 | eino-claude-code（Claude Code → eino Agent） |
| 镜像构建 | BuildKit (Go library, `client.Solve()`) |
| K8s 操作 | client-go |
| MCP | eino Tool → MCP server → Claude Code 可调用 |

## 本地 vs 生产

同一套 generator 二进制，区别只在环境变量。本地通过 `CHALLENGE_DRAFT_JSON` 注入 reviewed draft，生产通过 K8s ConfigMap/Secret 注入。

| | local | prod |
|------|------|------|
| Kubeconfig | env `KUBECONFIG` | InClusterConfig（自动） |
| Registry | env `REGISTRY=localhost:5000` | env `REGISTRY=<ACR VPC>` |
| Build | BuildKit → push registry | BuildKit → push registry |
| MCP lab | kubectl → Kind | kubectl → ACK |
| LLM | env `ANTHROPIC_*` | env `ANTHROPIC_*` |

### 配置文件 (breakfix.yaml)

```yaml
# Agent workflow — LLM API
llm:
  base_url: https://api.deepseek.com/anthropic
  model: deepseek-v4-pro
  haiku_model: deepseek-v4-flash
  effort: max
  api_key: sk-your-deepseek-api-key
```

> `breakfix.yaml` 已 gitignored，api_key 可以安全存放其中。

### 配置到环境变量映射

| 配置文件 | 环境变量 |
|------|------|
| `llm.base_url` | `ANTHROPIC_BASE_URL` |
| `llm.model` | `ANTHROPIC_MODEL`, `ANTHROPIC_DEFAULT_OPUS_MODEL`, `ANTHROPIC_DEFAULT_SONNET_MODEL` |
| `llm.haiku_model` | `ANTHROPIC_DEFAULT_HAIKU_MODEL`, `CLAUDE_CODE_SUBAGENT_MODEL` |
| `llm.effort` | `CLAUDE_CODE_EFFORT_LEVEL` |
| `llm.api_key` | `ANTHROPIC_AUTH_TOKEN` |

### 全部环境变量

```bash
CHALLENGE_DRAFT_JSON # 结构化 reviewed challenge draft
REGISTRY           # 镜像仓库地址 (localhost:5000 或 ACR VPC)
ACR_NAMESPACE      # registry 命名空间
KUBECONFIG         # (仅 local) kubeconfig 路径

# LLM (从 breakfix.yaml llm 段读取)
ANTHROPIC_BASE_URL
ANTHROPIC_MODEL / ANTHROPIC_DEFAULT_OPUS_MODEL / ANTHROPIC_DEFAULT_SONNET_MODEL
ANTHROPIC_DEFAULT_HAIKU_MODEL
CLAUDE_CODE_SUBAGENT_MODEL
CLAUDE_CODE_EFFORT_LEVEL
ANTHROPIC_AUTH_TOKEN
```

### 本地 registry 要求

registry 容器需接入 Kind 网络，使 Kind Pod 能直接 pull：

```bash
docker run -d -p 5000:5000 --name registry registry:2
docker network connect kind registry
```

## Job Pod 结构

```
┌────────────────────────────────────────────┐
│  Job Pod (ACK / Kind 集群内)                │
│                                             │
│  container: agent                           │
│    image: breakfix-generator                │
│    securityContext.privileged: true          │
│                                             │
│    内嵌:                                     │
│      generator binary    ← Go + eino 编排    │
│      claude CLI          ← DeepSeek v4 pro  │
│      buildkitd           ← BuildKit daemon  │
│      buildkit-runc       ← OCI 运行时       │
│      buildkit-cni-*      ← 网络插件         │
│      client-go           ← K8s 操作库       │
│                                             │
│    env:                                      │
│      CHALLENGE_DRAFT_JSON, REGISTRY, ACR_NAMESPACE │
│      ANTHROPIC_BASE_URL, ANTHROPIC_AUTH_TOKEN│
│      KUBECONFIG (仅 local)                   │
│                                             │
│    SA (仅 prod): breakfix-generator          │
│      └── pods, pods/exec, pods/log           │
└────────────────────────────────────────────┘
```

## 三阶段流程

```
  Challenge Idea
    │
    ▼
  Reviewed Draft
    │
    ▼
┌─────────────────────────────────────────────────┐
│  Phase 1: GENERATE                               │
│                                                   │
│  Worker Agent (persistent session)                │
│    System prompt: 题目设计师                       │
│    Tools:                                         │
│      built-in: Read, Write, Edit, Bash             │
│      MCP (Go 代码提供):                            │
│        lab_create()    → 创建临时 lab pod           │
│        lab_exec(name, script) → 执行脚本            │
│        lab_verify(name) → cp verify.sh + exec     │
│        lab_logs(name)    → 获取 pod 日志            │
│        lab_destroy(name) → 删除 lab pod             │
│                                                   │
│  同一个 session 内持续迭代:                         │
│    生成文件 → lab_create → 注入故障                 │
│    → 跑 answer.sh → 跑 verify.sh                  │
│    → 看结果 → 改文件 → lab_destroy → new lab_create│
│    → 自己满意 → 进入 Phase 2                       │
└───────────────────────┬─────────────────────────┘
                        ↓
┌─────────────────────────────────────────────────┐
│  Phase 2: JUDGE                                  │
│                                                   │
│  Judge Agent (每次全新 session, 隔离上下文)         │
│    System prompt: 严格的审题人                      │
│    Tools: Read（只能读, 不能改）                   │
│    输入: reviewed draft + Phase 1 产出的文件         │
│                                                   │
│  审查: 文件一致性、验证逻辑正确、难度合理、可解性     │
│                                                   │
│  输出:                                             │
│    PASS → 进入 Phase 3                             │
│    FAIL → 返回具体问题 → 回到 Phase 1               │
│          (Worker 在已有 session 上继续修改)         │
└───────────────────────┬─────────────────────────┘
                        ↓
┌─────────────────────────────────────────────────┐
│  Phase 3: VERIFY (Go 代码, 确定性执行)            │
│                                                   │
│  1. 启动 buildkitd, client.Solve() 构建+推 registry│
│  2. client-go CreatePod（用正式镜像）              │
│  3. client-go ExecInPod -- bash answer.sh         │
│  4. client-go ExecInPod -- bash verify.sh         │
│                                                   │
│  PASS → output JSON → exit 0                      │
│  FAIL → 回到 Phase 2 (Judge 分析失败原因)          │
│                                                   │
│  最多 5 轮全局循环                                  │
└─────────────────────────────────────────────────┘
```

## 关键设计原则

### Worker 持久 session

Phase 1 的 Worker 使用同一个 Claude Code session。上次迭代的文件、lab 实验日志、失败记录都在上下文里。Agent 可以在已有基础上修改，而不是每次重新开始。

### Judge 隔离上下文

Phase 2 每次都是全新 session，Judge 只看到：
- 当前题目文件内容
- reviewed draft 和规格要求

Judge 不知道 Worker 的思考过程，不知道它试了几次、怎么想的。这防止确认偏差——"既然 Worker 觉得对，那应该对吧"。

### MCP 工具（Go 代码实现）

lab 操作不经过 LLM 决策——Go 代码通过 client-go 提供确定性的 MCP 工具，Claude Code 调用：

```go
labCreate(ctx)    → client-go CreatePod (breakfix-base 镜像)
labExec(pod, script) → client-go ExecInPod
labVerify(pod)    → client-go CopyToPod(verify.sh) + ExecInPod
labLogs(pod)      → client-go GetPodLogs
labDestroy(pod)   → client-go DeletePod + DeleteNamespace
```

> 复用 `internal/k8s/client.go` 中已有的 Pod/Exec/Wait/Cleanup 方法。

### 题目文件结构

Worker 在 `/workspace/challenges/<id>/` 下生成：

```
challenge.yaml     # 元数据
Dockerfile         # FROM base + COPY question.md + RUN generate.sh
generate.sh        # 注入故障 (docker build 时执行)
question.md        # 用户看到的任务说明书
verify.sh          # 验收脚本 (server 持有, 不进镜像)
answer.sh          # 标准答案 (agent 自验证用, 不进镜像)
```

## 本地开发

```bash
# generator 调试
export CHALLENGE_DRAFT_JSON='{"title":"Cleanup runaway logs","difficulty":"medium","tags":["linux","logs"],"description":"Investigate disk pressure caused by unbounded logs and repair the cleanup flow.","operator_story":"You are the on-call engineer responding to a disk usage alert on a single Linux host.","broken_state":"The machine is healthy enough to inspect, but logs keep growing and the cleanup automation is misconfigured.","expected_fix":"Find the source of log growth, correct the cleanup path or policy, and ensure the remediation survives verification.","verification_expectations":"Disk usage drops to an acceptable level and the broken cleanup behavior is fixed.","constraints":"Use standard shell tooling inside the container; do not remove unrelated data.","notes":"Prefer a realistic journald or file-log workflow over toy files."}'
make generator-dev

# 内部做的事：
# 1. go build -o bin/generator ./cmd/generator
# 2. 注入 env:
#    REGISTRY=localhost:5000
#    ACR_NAMESPACE=break-fix
#    KUBECONFIG=~/.kube/config
#    CHALLENGE_DRAFT_JSON='...'
# 3. ./bin/generator
# 4. 生成的题目写入 data/challenges/<id>/
```

### 调试完整流程

```bash
make dev                     # Kind + registry + server 启动
export CHALLENGE_DRAFT_JSON='...'
make generator-dev           # 跑 agent workflow
# → 题目落地 data/challenges/<id>/
# → server SyncChallenges() 自动注册
```

## 文件结构（新增）

```
cmd/generator/
  ├── main.go           # 入口, 读 env, 编排 3 阶段循环
  ├── mcp/
  │   ├── lab.go        # lab_create / lab_exec / lab_verify / lab_logs / lab_destroy
  │   └── server.go     # eino Tool → MCP server
  ├── buildkit.go        # BuildKit build + push registry
  ├── prompts.go         # Worker + Judge system prompts
  ├── phases.go          # Phase 1-3 编排逻辑
  └── Dockerfile         # 构建 generator 镜像
```

## Server 集成（后续）

```
1. Server 新增 HTTP API: CreateGenerationJob(draft) → job id
2. Server 创建 Job, 注入 env (CHALLENGE_DRAFT_JSON, REGISTRY 等)
3. Job exit 0 → Server 读 logs → 解析 JSON → 写入 data/challenges/
4. challenge.SyncChallenges() → 上线
5. Web UI 轮询 job 状态并展示结果
```

## 未来优化

- 难度评估模型（Judge 给难度打分）
- 弱模型降成本（deepseek-v4-flash 替换 pro）
- 题目模板库（减少 Worker 生成自由度，提高成功率）
- BuildKit 缓存层（registry type export-cache 加速重复构建）
- K8s-native CRD 化：将题目、实例、用户建模为 K8s 自定义资源，通过 Controller 调和状态。`kubectl get challenges` 查看题库，`kubectl apply -f challenge.yaml` 创建题目，GitOps 友好，和 ACK 生态深度整合。
