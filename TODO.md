# TODO

## Current Work

当前没有待执行的仓库结构迁移。下一阶段的内容生产与产品方向见 [`NEXT.md`](NEXT.md)；持续风险和待讨论项见
[`REVIEW.md`](REVIEW.md)。

## Working Rules

- 每个新任务先明确唯一的领域、运行时或产品边界，再在这里记录可执行步骤。
- 一个步骤完成后必须完成相关测试和审查，并作为独立提交；不保留旧路径、兼容 wrapper 或重复文档。
- 长期契约写入 [`docs/`](docs/README.md)，本文件只保留当前工作项和其链接。
- Catalog 基线始终通过 immutable Catalog Release 安装；测试也使用相同入口，不能复制运行时 `data/`。

## Structural Baseline

当前布局和依赖规则见[代码布局](docs/architecture/code-layout.md)，Catalog 初始化见
[Catalog Release](docs/architecture/catalog-release.md)，本地开发与验证入口见
[本地开发](docs/operations/development.md)和[测试与真实验收](docs/operations/testing.md)。
