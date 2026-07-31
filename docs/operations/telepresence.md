# Telepresence 本地调试

需要调试部署在真实 Kind 或 Kubernetes 集群中的组件时，使用仓库内 [`dev/telepresence.sh`](../../dev/telepresence.sh)。它通过 `telepresence replace` 以本地前台进程接管一个 Deployment，保留该角色的集群 DNS、Service identity、挂载凭据和 Kubernetes ServiceAccount，不会让本地与集群同时消费同一份工作。

支持的角色是 Server、Controller、Agent Worker、Builder Worker、Publisher Worker 和 Verifier Worker。固定 Worker 的日志会直接显示在本地终端，可与 Playwright 或真实运行时验收并排观察。

## 前置条件

本机需要 `kubectl`、`telepresence`、Go、Make 和可用的当前 Kubernetes context。Controller replacement 还需要 `vcluster` 在 `PATH`。Server replacement 通过 SSHFS 挂载 Server data PVC，因此 FUSE 必须允许 `allow_other`：

```bash
sudo sed -i 's/^#user_allow_other$/user_allow_other/' /etc/fuse.conf
```

首次连接会在 `ambassador` namespace 安装 Telepresence Traffic Manager：

```bash
make telepresence-connect
```

临时 kubeconfig、渲染配置、挂载路径和恢复状态保存在已忽略的 `.local/telepresence/`。脚本只从 `breakfix-system` 当前部署的 ConfigMap 和 Secret 读取本角色需要的值；若配置了内部 Registry CA，Server 和 Publisher replacement 会从只读 `breakfix-registry-ca` ConfigMap 挂载该公开根证书，绝不会导出 Registry TLS 私钥。

## 接管一个角色

每个命令以前台方式执行，标准输出就是实时日志：

```bash
make telepresence-server
make telepresence-controller
BREAKFIX_TELEPRESENCE_ALLOW_WORKER_REPLACE=1 make telepresence-agent-worker
BREAKFIX_TELEPRESENCE_ALLOW_WORKER_REPLACE=1 make telepresence-builder
BREAKFIX_TELEPRESENCE_ALLOW_WORKER_REPLACE=1 make telepresence-publisher
BREAKFIX_TELEPRESENCE_ALLOW_WORKER_REPLACE=1 make telepresence-verifier
```

Server 默认监听 `http://127.0.0.1:19091`；Controller health 默认在 `http://127.0.0.1:18081/readyz`。集群内访问 `breakfix-server` Service 时仍会路由到被接管的本地 Server，因此可以在另一个终端运行浏览器或运行时验收。

Worker replacement 需要显式的 `BREAKFIX_TELEPRESENCE_ALLOW_WORKER_REPLACE=1`。脚本记录原 Deployment replica 数，将其缩为一后再替换最后一个 Pod，退出时恢复原副本数。只在确认该类 WorkItem 没有正在进行的外部副作用时接管；WorkItem lease 会围栏迟到的结果，但不会替代运维判断。

可通过环境变量调整 namespace、Traffic Manager namespace、本地 Server port 或状态目录：

```bash
BREAKFIX_TELEPRESENCE_NAMESPACE=breakfix-system \
BREAKFIX_TELEPRESENCE_SERVER_PORT=19091 \
make telepresence-server
```

## 清理与定位

```bash
make telepresence-status
make telepresence-down
make telepresence-disconnect
```

`telepresence-down` 恢复本项目被接管的 Deployment 和 Worker 副本数，但不会卸载共享的 Traffic Manager。确实不再需要开发调试组件时，安装者可以显式执行：

```bash
telepresence helm uninstall --namespace ambassador
kubectl delete namespace ambassador
telepresence quit --stop-daemons
```

Telepresence 不会汇总其他集群工作负载的日志。使用 WorkItem ID、kind、attempt 和 Environment UID 关联 Server、Controller、固定 Worker、Registry 和环境日志。
