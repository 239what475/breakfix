# 本地开发

本地开发不复制 `data/`、不绕过 Catalog Release，也不修改生产节点配置。运行时配置从
[`config/app/local.example.yaml`](../../config/app/local.example.yaml) 复制到被忽略的 `config/app/local.yaml`；密钥只通过
环境变量或 Kubernetes Secret 提供。

## 本机构建

```bash
cp config/app/local.example.yaml config/app/local.yaml
# 填写 PostgreSQL、模型、OpenSandbox、Registry 与 Incus 参数。
make build
./bin/breakfix-server -config config/app/local.yaml
```

`make build` 在 `web/package-lock.json` 未变化时复用 `web/node_modules`，不会反复执行 `npm ci`；缺少依赖或锁文件更新时
才重新安装。需要主动刷新前端依赖时运行 `make web-deps`。三个可执行文件都接受相同的 `-config` 参数。

## Kind

Kind 仅用于开发。创建运行时 Secret、Registry 认证 Secret 和 Incus 身份 Secret 后运行：

```bash
make deploy-kind
```

该命令构建本地镜像、加载 Server/Controller/Worker image、准备 Kind Registry，再按顺序应用生产根包和 Kind Registry
overlay。Kind Registry 使用固定 `NodePort 30443`；kubelet、Server 和 Generate Worker 共享同一个 Kind node
IP authority。生产部署不能复用该地址。

需要重新清理本地运行时状态时：

```bash
make reset-kind
```

Node runtime 的 Incus project、桥接网络、基础 system-container image 和 role-specific mTLS 身份由
`scripts/incus/bootstrap.sh` 准备。脚本输出的 `.local/incus/` 只属于本地或管理员环境，不能提交。

## Telepresence

Telepresence 用于保留集群依赖的同时，在本机运行一个 Breakfix 组件。支持 Server、Controller 和 Generate Worker；
被接管组件的日志直接出现在本机终端，适合与 Playwright 或真实运行时验收并排观察。

前置条件：已连接目标 Kubernetes context 和 Traffic Manager，本机具备 `telepresence`、`kubectl`、Go、Node.js 及项目依赖；
目标集群已部署当前版本，Generate Worker 的本地进程还能访问模型 API、Incus 与 Registry。

```bash
scripts/dev/telepresence.sh connect
scripts/dev/telepresence.sh status

scripts/dev/telepresence.sh server
scripts/dev/telepresence.sh controller
scripts/dev/telepresence.sh generate-worker
```

脚本读取集群配置、准备最小身份和 kubeconfig，并在本机构建对应二进制。接管 Worker 时会缩容目标 Deployment，避免两个副本
同时领取一个 Workflow；lease fencing 仍会拒绝迟到结果，但不能代替对外部副作用的运维判断。

结束接管：

```bash
scripts/dev/telepresence.sh down
scripts/dev/telepresence.sh disconnect
```

排障时以 `GenerationWorkflow`、CatalogRelease、AgentRun、CandidateRevision 与 Environment UID
关联日志。Telepresence 不会汇总其他集群工作负载日志；Controller、Registry 和 Environment 仍用 `kubectl logs` 或
`kubectl describe` 观察。
