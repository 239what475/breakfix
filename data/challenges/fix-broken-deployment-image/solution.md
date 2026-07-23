# 解答：修复错误的 Deployment 镜像

## 第一步：检查资源

```bash
kubectl get deployment,service,pods -n default
kubectl describe deployment web -n default
```

Deployment 使用了不存在的镜像标签，因此不会出现可用副本。

## 第二步：修复镜像并等待滚动更新

```bash
kubectl set image deployment/web web=nginx:1.25.5 -n default
kubectl rollout status deployment/web -n default --timeout=120s
```

## 第三步：确认服务仍可选择工作负载

```bash
kubectl get service web -n default -o yaml
kubectl get deployment web -n default -o jsonpath='{.status.readyReplicas}/{.spec.replicas}{"\n"}'
```

不要删除并重建 Service。修复镜像后等待 Deployment 的可用副本数达到期望值即可。
