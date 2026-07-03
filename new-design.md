# Breakfix K8s Challenge 新设计

## 目标

在保持 Breakfix 现有产品形态不变的前提下，为平台引入 K8s 题支持。

现有主流程保持不变：

```text
challenge -> start challenge -> terminal -> submit -> verify
```

K8s 题不是另一套平台，而是在现有做题模型上增加一个独立的 K8s 环境。

---

## 核心原则

### 1. challenge 仍然是文件系统目录

challenge 不是 CRD，不进数据库，不进 K8s。

权威来源仍然是：

```text
data/challenges/<id>/
```

每道题仍然包含：

```text
challenge.yaml
Dockerfile
generate.sh
question.md
verify.sh
answer.sh
```

### 2. 用户始终进入一个 workspace 容器

无论哪类题，用户入口都不变：

- 浏览器 terminal
- terminal 连接到一个 workspace pod

区别只在于这个 workspace pod 操作的对象不同：

- `runtime=container`：操作当前容器内环境
- `runtime=vcluster`：通过 `kubectl + kubeconfig` 操作独立的 vcluster

也就是说：

```text
浏览器 terminal -> workspace pod -> kubectl -> vcluster
```

### 3. VerifyTask 继续作为最终真实验证器

`VerifyTask` 的职责不变：

- 取 challenge artifact
- 构建镜像
- 启动真实验证环境
- 执行 `answer.sh`
- 执行 `verify.sh`
- 根据结果决定成功或失败

变化的只是验证时启动的环境类型。

---

## challenge 元数据

`challenge.yaml` 新增一个字段：

```yaml
runtime: container
```

或：

```yaml
runtime: vcluster
```

不引入 `runtime.class`、`runtime.backend` 之类的分层字段。

原因：

- 题目只需要表达“用户拿到的做题环境是什么”
- 不需要暴露底层实现分层
- 后续如果引入 kind、kwok，也不应该急着污染 challenge 顶层 schema

---

## 运行环境模型

用户点击 `start challenge` 后，平台在集群中创建“运行环境”。

运行环境才是 CRD，不是题目本身。

第一阶段定义两类环境：

- `ContainerEnvironment`
- `VClusterEnvironment`

### ContainerEnvironment

表示普通题的完整运行环境。

内部资源：

- 独立 namespace
- 一个 workspace pod

### VClusterEnvironment

表示 K8s 题的完整运行环境。

内部资源：

- 独立 namespace
- 一个 vcluster
- 一个 kubeconfig secret
- 一个 workspace pod

`vcluster` 不单独做顶层 CRD。

原因：

- 用户拿到的是完整做题环境，不是裸 vcluster
- TTL、Ready、终端连接、提交、清理都应以整套环境为单位
- 当前没有“一套 vcluster 复用多个 workspace”的需求

---

## 两类环境的职责

### ContainerEnvironment

负责：

- 创建 namespace
- 创建 workspace pod
- 等待 `generate.sh` 完成
- 提供 terminal 连接目标
- 在 submit 时执行 `verify.sh`
- TTL 到期后回收

### VClusterEnvironment

负责：

- 创建 namespace
- 创建 vcluster
- 等待 vcluster API 可用
- 生成 kubeconfig secret
- 创建 workspace pod
- 将 kubeconfig 挂载到 workspace pod
- 设置 `KUBECONFIG`
- 等待 `generate.sh` 通过 `kubectl` 初始化题目环境
- 提供 terminal 连接目标
- 在 submit 时执行 `verify.sh`
- TTL 到期后回收

---

## challenge 文件语义

文件结构不变，但在 `runtime=vcluster` 下语义有所变化。

### runtime=container

- `generate.sh`：修改当前容器环境，制造破损状态
- `verify.sh`：检查当前容器环境是否被修复
- `answer.sh`：在当前容器环境中完成标准修复

### runtime=vcluster

- `generate.sh`：使用 `kubectl` 操作 vcluster，创建题目所需资源与故障
- `verify.sh`：检查 vcluster 中的资源状态是否满足验收条件
- `answer.sh`：使用 `kubectl` 修复 vcluster 中的问题

因此，K8s 题不需要第二套 challenge 包格式。

---

## 用户运行流程

### 普通题

1. 用户点击 `start challenge`
2. server 读取 `challenge.yaml.runtime=container`
3. 创建 `ContainerEnvironment`
4. controller 创建 namespace 和 workspace pod
5. pod 启动后执行 `runtime-init.sh -> generate.sh`
6. `generate.sh` 完成后进入 `sleep infinity`
7. environment 进入 `Ready`
8. 前端 terminal 连接 workspace pod
9. 用户做题并提交

### K8s 题

1. 用户点击 `start challenge`
2. server 读取 `challenge.yaml.runtime=vcluster`
3. 创建 `VClusterEnvironment`
4. controller 创建 namespace
5. controller 在 namespace 中创建 vcluster
6. 等待 vcluster API 可用
7. 生成 kubeconfig secret
8. 创建 workspace pod，并将 kubeconfig 挂载进去
9. workspace pod 启动时执行 `runtime-init.sh -> generate.sh`
10. `generate.sh` 使用 `kubectl` 在 vcluster 内布置故障环境
11. 完成后进入 `sleep infinity`
12. environment 进入 `Ready`
13. 前端 terminal 连接 workspace pod
14. 用户在容器内使用 `kubectl` 操作 vcluster
15. 用户提交

---

## submit 逻辑

submit 仍然是对当前 workspace pod 执行 `verify.sh`。

普通题：

- `verify.sh` 检查容器内环境

K8s 题：

- `verify.sh` 在 workspace pod 内执行
- 但它检查的是 vcluster 中的资源状态

因此，用户提交体验不变。

---

## VerifyTask 的位置

`VerifyTask` 不做概念性重构，只做环境选择上的适配。

它仍然负责：

1. 读取 submission artifact
2. 构建 challenge image
3. 启动真实验证环境
4. 执行 `answer.sh`
5. 执行 `verify.sh`
6. 成功则 publish，失败则返回错误

区别只在第 3 步：

- `runtime=container`：启动临时 `ContainerEnvironment`
- `runtime=vcluster`：启动临时 `VClusterEnvironment`

也就是说，验证逻辑仍然是原来那套，只是运行环境不同。

---

## 建议的状态字段

两类环境应尽量暴露统一状态，方便 API 和前端复用。

公共状态建议包括：

- `phase`
- `namespace`
- `workspacePodName`
- `startedAt`
- `expiresAt`
- `message`

`VClusterEnvironment` 可额外包含：

- `vclusterName`
- `kubeconfigSecretName`

这样前端无需理解底层差异，只需要知道：

- 环境是否 Ready
- 终端应该连哪个 pod
- 当前是否正在销毁或失败

---

## 基础镜像建议

建议保留两套基础镜像：

- `breakfix-base`
- `breakfix-k8s-base`

其中 `breakfix-k8s-base` 额外内置：

- `kubectl`
- 常见 K8s 排障工具
- 必要网络与调试工具

这样 `runtime=vcluster` 的题目 workspace pod 可以直接工作。

---

## API 影响

前端交互模型尽量不变。

仍然保留：

- `start challenge`
- `resume`
- `submit`
- `reset`
- `stop`

只是 server 内部不再围绕旧的 `Instance` 工作，而是围绕对应环境对象工作。

前端只关心：

- 当前环境类型
- 当前 phase
- workspace pod 是否可连接
- submit 是否通过

---

## 迁移路径

### Phase 1

- challenge loader 支持 `runtime`
- 老题默认 `runtime=container`
- 现有普通题先完整映射到 `ContainerEnvironment`

### Phase 2

- 引入 `VClusterEnvironment`
- 做一到两道手写 K8s 题
- 跑真实 e2e：start -> terminal -> submit -> verify

### Phase 3

- 让 generator 支持产出 `runtime=vcluster` 的题
- 最终仍通过统一 `VerifyTask` 发布

---

## 暂不做的内容

当前阶段不做：

- kind
- kwok
- kubeconfig 下载
- 多环境组合
- challenge CRD 化
- 复用一个 vcluster 给多个用户环境

先把 `container + vcluster` 两类环境跑通，并保持产品主流程稳定。

---

## 最终结论

Breakfix 引入 K8s 题时，应坚持以下模型：

- challenge 仍然是文件系统目录
- 用户始终进入一个 workspace pod
- K8s 题是在 workspace pod 基础上额外提供一个 vcluster
- 运行环境用 CRD 表达，而不是题目用 CRD 表达
- `VerifyTask` 继续作为最终真实验证器，不做大改

第一阶段只做 `runtime=vcluster`，这是当前最干净、最贴合现有架构的演进路径。
