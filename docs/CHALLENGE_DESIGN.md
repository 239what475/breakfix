# 题目系统设计

## 题目模型

题目是 `data/challenges/<id>/` 下的文件系统目录。题目不进数据库，也不是 Kubernetes CRD；Gateway 启动时扫描该目录并建立可查询目录。

每道题必须包含：

```text
challenge.yaml
Dockerfile
generate.sh
problem.md
solution.md
hints/<checkpoint-id>.md
checks/checkpoints.sh
answer.sh
```

`generate.sh` 在题目 Pod 首次启动时由基础镜像的 runtime init 执行，用于构造初始环境。`problem.md`、`solution.md`、提示和检查器会打包进入镜像；`answer.sh` 仅供平台真实验证和题目作者自测使用。

## 检查点

`challenge.yaml` 声明按学习目标组织的检查点。`checks/checkpoints.sh --json` 一次读取当前环境并输出每个检查点的 JSON 结果。检查器只检查环境结果，不能修改环境或要求用户使用固定命令、文件编辑路径。

全部必需检查点通过即为题目完成。不存在独立的 `verify.sh`，也不存在只在某个用户动作时执行的隐藏规则。controller 周期执行同一个检查器协议并保存结果快照；进度接口只展示该权威状态，全部通过后环境自动完成并按闲置策略清理。

## 运行时

`runtime: container` 创建一个用户 workspace Pod。

`runtime: vcluster` 创建一个完整的 `VClusterEnvironment`：隔离 namespace、vcluster、kubeconfig Secret 和带 `kubectl` 的 workspace Pod。用户仍然连接 workspace Pod，再通过其中的 kubeconfig 操作 vcluster。

两种运行时都遵循同一题目包格式和检查点协议。运行环境由 controller 调和，题目本身不需要成为 CRD。

## 真实发布验证

生成工作流将题目 artifact 交给 Gateway。Gateway 创建 `VerifyTask`；controller 在真实对应 runtime 中构建镜像、启动环境、运行 `answer.sh`，再运行所有检查点。全部通过才发布镜像和题目目录；失败只返回结构化检查结果和诊断。

详情与工作台改造计划见 [`../iximiuz/next-steps.md`](../iximiuz/next-steps.md)。
