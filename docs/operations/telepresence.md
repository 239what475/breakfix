# Telepresence 本地调试

当需要对真实 Kind 或 Kubernetes 运行时调试 Server、Controller、Agent Worker 时，使用仓库内
[`dev/telepresence.sh`](../../dev/telepresence.sh)。它以 `telepresence replace` 暂停对应 Pod 中的业务容器，
由本地二进制接管同一 Kubernetes Service、集群 DNS 与运行时环境，避免本地和集群同时运行两个
Server、Controller 或 Worker。

该入口只用于开发集群。它从当前 Kubernetes context 读取 `breakfix-system` 中已部署的配置：

- 临时生成一小时有效的 `breakfix-server` 或 `breakfix-controller` ServiceAccount kubeconfig。
- 从 `breakfix-runtime` Secret 仅向本地子进程注入所需的运行时变量，不把 Secret 写入文件。
- Server 挂载生产 Server 正在使用的 PVC，因此本地读取和写入的是同一份题目与 artifact 数据。
- Controller 使用真实 ServiceAccount、Leader Lease 与 CRD watch；Agent Worker 使用真实 Agent Runtime
  PostgreSQL 连接和 Server 内部 API。

临时配置、kubeconfig 和挂载路径位于已忽略的 `.local/telepresence/`，组件退出或执行清理命令后删除。

## 前置条件

本机需要 `kubectl`、`telepresence`、Go 和可用的当前 Kubernetes context。Controller 还需要 `vcluster`
CLI。Server 会以 SSHFS 挂载其 PVC，因此 FUSE 配置必须允许 `allow_other`：

```bash
sudo sed -i 's/^#user_allow_other$/user_allow_other/' /etc/fuse.conf
```

首次连接会在 `ambassador` namespace 安装 Telepresence Traffic Manager：

```bash
make telepresence-connect
```

可通过环境变量调整 namespace、Traffic Manager namespace、本地端口或临时目录：

```bash
BREAKFIX_TELEPRESENCE_NAMESPACE=breakfix-system \
BREAKFIX_TELEPRESENCE_SERVER_PORT=19091 \
make telepresence-server
```

## 前台接管与 E2E

在三个独立终端中前台运行需要调试的组件。每个命令会先构建对应本地二进制；标准输出就是实时的
Server、Controller 或 Worker 日志。

```bash
make telepresence-server
make telepresence-controller
BREAKFIX_TELEPRESENCE_ALLOW_WORKER_REPLACE=1 make telepresence-worker
```

Server 监听 `http://127.0.0.1:19091`，Controller health 为
`http://127.0.0.1:18081/readyz`。从集群内部访问 `breakfix-server` Service 仍会到达本地 Server，
因此现有的 Service port-forward 或 Playwright E2E 可继续使用。随后在第四个终端运行 E2E，即可将
运行时日志与 Playwright 输出并排观察。

Worker 会先记录远端 Deployment 的副本数并缩容到一，再替换最后一个远端 Worker；这防止本地 Worker
与远端副本竞争同一个 Agent Run。该操作需要显式设置
`BREAKFIX_TELEPRESENCE_ALLOW_WORKER_REPLACE=1`，并只应在确认没有正在执行的 Agent Run 后进行。
本地 Worker 退出后脚本自动恢复原副本数；进程异常终止时使用下方清理命令恢复。

Controller 没有对外服务流量，故不会配置 Telepresence 端口转发；本机 health endpoint 仅用于观察。

## 状态与清理

```bash
make telepresence-status
make telepresence-down        # 恢复三个 Deployment，移除本项目的 traffic-agent
make telepresence-disconnect  # 清理后关闭本地 Telepresence daemon
```

`telepresence-down` 不会卸载共享的 Traffic Manager 或删除 `ambassador` namespace。若当前开发集群不再
需要 Telepresence，应由安装者显式执行：

```bash
telepresence helm uninstall --namespace ambassador
kubectl delete namespace ambassador
telepresence quit --stop-daemons
```

Telepresence 不会汇总集群内其他工作负载的日志。Verify Job、挑战环境 Pod、PostgreSQL 与 Registry 仍需
通过 `kubectl logs` 或其原有观测入口查看。
