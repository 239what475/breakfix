# Telepresence 本地调试

Telepresence 用于在保留集群依赖的情况下，在本机运行一个 Breakfix 组件。支持的角色是 Server、Controller、
Generate Worker 和 Taxonomy Worker。被接管组件的日志直接出现在本地终端，适合和 Playwright 或真实运行时
验收并排观察。

## 前置条件

- 已连接目标 Kubernetes context，且已安装 Traffic Manager。
- 本机已配置 `telepresence`、`kubectl`、Go、Node.js 和项目依赖。
- 目标集群已部署当前版本的 Breakfix，并存在 `breakfix-system` namespace。
- 本机能访问模型 API；接管 Generate Worker 时还必须能访问 Incus 和 Registry。

```bash
scripts/dev/telepresence.sh connect
scripts/dev/telepresence.sh status
```

## 接管组件

```bash
scripts/dev/telepresence.sh server
scripts/dev/telepresence.sh controller
scripts/dev/telepresence.sh generate-worker
scripts/dev/telepresence.sh taxonomy-worker
```

脚本会从集群读取配置、为本地进程准备最小身份和 kubeconfig，并在本地构建对应二进制。接管 Worker 时，
脚本会将目标 Deployment 缩容后替换，避免两个副本同时领取同一 Workflow；lease fencing 仍会拒绝迟到结果，
但不能替代对外部副作用的运维判断。

接管结束后：

```bash
scripts/dev/telepresence.sh down
scripts/dev/telepresence.sh disconnect
```

## 排障关联

使用 `GenerationWorkflow` 或 `TaxonomyWorkflow` ID、state、state attempt、AgentRun ID、
CandidateRevision ID 与 Environment UID 关联日志。

Telepresence 不会汇总其他集群工作负载日志。需要同时观察 Controller、Registry 或 Environment 时，单独执行
`kubectl logs` 和 `kubectl describe`；本地接管只替换选定组件。
