# TODO

## Current Work

## Registry 单 Authority 重构

- [x] 运行时配置只保留 `registry.repository`，Server、Generate Worker、Catalog Release 工具、Kubelet 和 Kind 开发脚本共享同一 Registry authority。
- [x] OCI Client 从完整 repository 或 image reference 派生 authority；所有操作拒绝 authority 不一致的镜像引用，不做隐式重写。
- [x] Kind 只保留固定 NodePort 作为开发 adapter，证书只有控制面 IP SAN；Generate Worker 的开发 NetworkPolicy 已允许该端口，未增加 DNS、hosts 或 containerd 特例。
- [x] Catalog Release archive 使用 OCI Image Spec 1.1 artifact 表达，内置 Registry 可接受并发布 immutable bundle。
- [x] 已完成部署、开发与架构文档更新；单元测试、生成物验证、lint、构建、Kustomize、真实 Kind HTTPS、镜像拉取、Catalog 安装和浏览器 E2E 均通过。

下一阶段的内容生产与产品方向见 [`NEXT.md`](NEXT.md)；持续风险和待讨论项见 [`REVIEW.md`](REVIEW.md)。

## Working Rules

- 每个新任务先明确唯一的领域、运行时或产品边界，再在这里记录可执行步骤。
- 一个步骤完成后必须完成相关测试和审查，并作为独立提交；不保留旧路径、兼容 wrapper 或重复文档。
- 长期契约写入 [`docs/`](docs/README.md)，本文件只保留当前工作项和其链接。
- Catalog 基线始终通过 immutable Catalog Release 安装；测试也使用相同入口，不能复制运行时 `data/`。

## Structural Baseline

当前布局和依赖规则见[代码布局](docs/architecture/code-layout.md)，Catalog 初始化见
[Catalog Release](docs/architecture/catalog-release.md)，本地开发与验证入口见
[本地开发](docs/operations/development.md)和[测试与真实验收](docs/operations/testing.md)。
