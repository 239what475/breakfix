# Runtime Worker 尺寸与批次并发配对

本文记录 Runtime Worker 资源尺寸的实测依据、镜像层操作的内存行为结论,以及
"文档批次并发阀值 ↔ Worker 副本数"的配对建议。数值来自试点批次的采样
(2026-09-19,3 页迷你库,E2E fixture 工作负载),不是拍脑袋的初值;真实语料
的全量批次上线前应按同一方法复测。

## 采样方法

- 环境:本地 Kind 集群(breakfix-e2e),Runtime Worker 以 Deployment 运行,
  每副本串行处理 1 个 action(空转副本不占 action 槽)。
- 工具:在 Kind node 容器内用 `crictl stats` 每 5 秒采样一次
  `cpu.usageNanoCores`(换算 millicores)与 `memory.workingSetBytes`
  (换算 MiB),覆盖空转基线、materialize 密集段与 verify 密集段。
- 一次完整 practice 分两段占用 Worker:materialize(拉取基础镜像层并物化
  artifact,约 1.5–2 分钟)与 verify(在 vcluster 环境里跑验证计划,约
  3.5–4 分钟)。

## 实测结果

| 阶段 | CPU 峰值 | 内存峰值(working set) |
| --- | --- | --- |
| 空转基线 | 1–2m | 7–16 MiB |
| materialize 密集段 | 116m–565m | 298–348 MiB |
| verify 密集段 | 161m–207m | 170–343 MiB |
| 全程峰值 | ≈ 740m | ≈ 348 MiB |

- 单 action 增量:CPU 峰值约 0.5–0.75 核,内存峰值约 350 MiB;阶段之间
  内存回到基线,多轮 action 未见累积驻留。
- 镜像层操作是流式的:blob 下载走 `io.Copy` 直接写入临时文件并流式计算
  sha256(`internal/adapter/oci`),镜像层不整块驻留内存;manifest 等小对象
  才整体读入。因此内存峰值来自 artifact 打包与验证输出,而不是镜像层缓存,
  尺寸不随镜像层体积线性增长。

## 尺寸表(回填 `deploy/manifests/runtime-worker.yaml`)

| 项 | 值 | 依据 |
| --- | --- | --- |
| requests.cpu | 750m | 实测单 action 峰值 ≈740m,保证调度时有足够余量 |
| requests.memory | 512Mi | 实测峰值 ≈348 MiB + 约 45% 余量 |
| limits.cpu | "2" | 峰值 ≈0.75 核,留约 2.7 倍突发余量 |
| limits.memory | 2Gi | 峰值 ≈348 MiB,留约 5 倍余量,覆盖更大 artifact |
| requests/limits.ephemeral-storage | 2Gi / 10Gi | 镜像层与 artifact 落盘,流式写不占内存但占盘 |

每副本基线(空转)约 2m CPU / 16 MiB 内存,可以忽略;副本的成本主体是
"在跑的那个 action"。

## 批次并发阀值 ↔ 副本数配对

- 每个 Worker 副本同时只跑 1 个 action,而每个批次条目在 Agent 链结束后
  恰好产生 1 个待跑 action。因此配对规则是:
  **副本数 ≥ Σ(各 Running 批次的并发阀值)**,默认部署即默认阀值 2 ↔ 副本 2。
- Server 侧的 Agent 链并发(process 上限 8)只影响 LLM 段,不占 Worker;
  Worker 侧无需与 8 对齐,按批次阀值扩即可。
- 扩容路径:单个批次把并发阀值调到 8 时,把副本扩到 ≥8;多个 Running 批次
  并存时按阀值之和取。副本不足不会丢工作——action 停在队列里,只是批次的
  收尾时间拉长;`runnable_actions.attempt` 上限 5 次的重试语义不变。
- 缩容:批次全部收尾后,副本可以缩回 2(默认),空转副本成本可忽略,
  保留 2 副本是为了让下一批的首个 action 立即被认领。

## 复测清单(真实语料全量批次前)

1. 用真实页面(体积更大的 page)重放上述采样脚本,重点看 verify 段内存。
2. 若单 action 内存峰值超过 1Gi,先把 limits.memory 提到 2 倍峰值再评估
   requests;CPU 峰值超过 1 核时同步上调 requests.cpu。
3. 确认 `document_batch_items` 的 Running 峰值并发与副本数一致;出现长时间
   Pending 即副本不足。
