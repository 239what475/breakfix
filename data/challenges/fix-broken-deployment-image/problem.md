# 修复错误的 Deployment 镜像

你位于一个已配置 `kubectl` 的工作容器中，连接到本题独立的 Kubernetes 环境。

`default` 命名空间已经存在 `web` Deployment 与对应的 Service，但工作负载当前不可用。

请检查 Deployment 的状态和事件，找出无法启动的原因，并直接在集群中修复它。修复后，Deployment 应恢复可用，Service 也必须保持可用。

检查点只读取集群当前状态，不会替你修改资源。
