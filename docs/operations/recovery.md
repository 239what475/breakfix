# 备份与恢复

本文定义 Breakfix 的恢复单元和人工演练步骤。它不增加恢复服务、对象存储、自动备份控制器或新的 readiness provider。

## 恢复单元

PostgreSQL 与 Server data PVC 是同一个权威恢复单元，必须在同一维护窗口、没有写者时一起备份和恢复：

- PostgreSQL 保存用户、会话、`GenerationWorkflow`、`CatalogRelease`、`RoadmapRevision`、challenge/revision identity、发布 intent 与清理 intent。
- `breakfix-server-data` PVC 保存 materialized challenge 目录、持久化 Catalog source 和 candidate archive。

Registry 与 Incus 是数据库引用的外部 immutable artifact provider，而不是这个单元的一部分：

- K8s artifact 必须仍可按数据库中的 OCI digest 读取。
- Node artifact 必须仍可按数据库中的 Incus fingerprint 读取。

它们各自的备份由部署者维护。不能把 PostgreSQL/PVC 与外部 provider 假装成一个原子快照；恢复时必须显式核验引用。生产根部署不包含 Registry；Kind overlay 的 `breakfix-registry-data` 只是本地开发数据，不是生产恢复方案。

当前默认 namespace 是 `breakfix-system`。固定写入组件是 `breakfix-server`、`breakfix-runtime-worker`、
`breakfix-controller` 与 `breakfix-postgresql`；PVC 名称分别是 `breakfix-server-data` 与 StatefulSet 创建的
`data-breakfix-postgresql-0`。实际 namespace、StorageClass、外部 Registry 和 Incus project 以部署配置为准。

## 维护窗口与备份

1. 在集群入口、Ingress 或负载均衡器停止向 Server 发送新请求。平台没有维护模式 API，不能只靠在浏览器中停止操作。
2. 记录维护开始时间、当前配置的 `catalog.release_reference`、当前 `RoadmapRevision`、PostgreSQL 备份标识和两个 PVC 快照标识。
3. 观察正在执行的 Generation、Catalog 与 Environment 工作，等待可安全完成的工作结束。必须中断的工作允许按现有 lease/恢复语义留下未完成 intent；不要手工伪造完成状态。
4. 停止所有应用写者，再确认 Server data PVC 没有挂载者：

   ```bash
   kubectl -n breakfix-system scale deployment/breakfix-runtime-worker --replicas=0
   kubectl -n breakfix-system scale deployment/breakfix-controller --replicas=0
   kubectl -n breakfix-system scale deployment/breakfix-server --replicas=0
   kubectl -n breakfix-system get pods -o wide
   kubectl -n breakfix-system describe pvc breakfix-server-data
   ```

   等待对应 Pod 完全退出。Server data PVC 是 `ReadWriteOnce`，不得在仍有 Server Pod 写入时执行文件级复制或 VolumeSnapshot。
5. 在这个无写者窗口内备份 PostgreSQL 和 `breakfix-server-data`。可使用部署者已有的逻辑备份、CSI VolumeSnapshot 或存储后端快照；两份备份必须属于同一窗口。若备份 PostgreSQL 的数据卷而非逻辑 dump，先停止 `breakfix-postgresql` StatefulSet，再对 `data-breakfix-postgresql-0` 做一致性快照。
6. 完成后保存备份校验和、存储位置、配置版本和 artifact provider 的备份/保留策略。备份失败或窗口内出现写入时，丢弃这对备份并重新开始，不要混用不同时间点的 PostgreSQL 与 Server PVC。

## 恢复顺序

在完成数据库、PVC 与外部 artifact 核验前，保持 Server、Runtime Worker 与 Controller 为零副本，且保持入口流量关闭。

1. 从同一维护窗口恢复 PostgreSQL。使用逻辑备份时恢复到干净的目标数据库；使用物理快照时恢复
   `data-breakfix-postgresql-0`。先启动 PostgreSQL，确认其数据库、凭据和 `breakfix_schema` 与部署的 Server 二进制一致。
2. 恢复 `breakfix-server-data` PVC。不要通过重新安装 Catalog、复制 Git 工作区或手工重建目录来替代该 PVC；这会破坏数据库中已发布 revision 与 materialized revision 的对应关系。
3. 通过 PostgreSQL 的只读查询列出当前 `RoadmapRevision` 中的 challenge/revision/materialized revision，并在恢复后的
   Server PVC 上核对相同目录和 revision。此时不要重新安装 Catalog、修改 Roadmap binding 或启动任何写入组件。
4. 使用部署者已有的 Registry API/client，对数据库中每个当前或未完成发布 intent 的 `OCIReference` 核验 manifest digest。使用 Incus API，对每个 Node artifact 的 `IncusFingerprint` 核验完整 fingerprint、image project 和镜像类型。`/capabilities/node-provider` 可辅助检查当前 Node provider 连通性，但不是历史 artifact 完整性的替代。
5. 外部 artifact 核验通过后，在仍不接收用户流量的条件下启动一份 Server。`/readyz` 会执行当前 Roadmap 到 materialized source 的完整性检查：

   ```bash
   kubectl -n breakfix-system scale deployment/breakfix-server --replicas=1
   kubectl -n breakfix-system rollout status deployment/breakfix-server
   kubectl -n breakfix-system port-forward service/breakfix-server 9090:9090
   curl --fail http://127.0.0.1:9090/readyz
   ```

   同时以只读方式检查 `/api/challenges` 的题目 identity 和数量符合当前 `RoadmapRevision`。`/readyz` 不会探测 Registry 或 Incus；它通过静态完整性检查，而不是把外部 provider 变成日常启动依赖。
6. 只有 PostgreSQL、PVC、Roadmap/materialized revision、Registry digest 与 Incus fingerprint 都一致时，启动 Runtime Worker 与 Controller，随后恢复入口流量：

   ```bash
   kubectl -n breakfix-system scale deployment/breakfix-runtime-worker --replicas=2
   kubectl -n breakfix-system scale deployment/breakfix-controller --replicas=1
   kubectl -n breakfix-system rollout status deployment/breakfix-runtime-worker
   kubectl -n breakfix-system rollout status deployment/breakfix-controller
   ```

Server 启动后会恢复已有 finalizer、lease 和 Roadmap AgentRun 边界；Runtime Worker 会恢复可领取 action 与资源回收。
不要新建第二套恢复队列，不要手工重放已经持久化的 provider action，也不要把日志当作恢复权威。

## 不一致处置

| 发现 | 处置 |
| --- | --- |
| PostgreSQL 与 Server PVC 不属于同一窗口，或 schema/`RoadmapRevision` 不一致 | 保持所有写入组件停止，恢复匹配的一对备份；不能只重置一侧后宣布恢复完成。 |
| Roadmap binding 指向缺失、变更或 hash 不匹配的 materialized 目录 | 保持入口关闭；不要静默过滤题目或修改 binding。恢复正确的 Server PVC，再重新执行完整性检查。 |
| Registry 缺少数据库引用的 OCI digest，或 digest 指向不同 manifest | 保持相关 workflow/release 不可服务，恢复 Registry artifact 或通过正常 Build/Verify/Publish 生命周期创建新的 revision；不得改写旧 revision 的 artifact reference。 |
| Incus 缺少或不匹配数据库引用的 fingerprint | 恢复匹配 image project/镜像，或通过正常生命周期生成新的 revision；不得用 alias 或当前 base image 冒充历史 fingerprint。 |

任一不一致都不是“忽略后继续”的场景。记录受影响的 workflow、release、challenge revision 和 artifact identity，保持写入
组件停止直到恢复证据完整。现有 finalizer/reaper 只处理已经持久化的幂等 intent，不负责从不一致的备份组合中猜测或重写权威数据。

## 演练验收

每次变更备份策略、StorageClass、Registry 或 Incus 拓扑后，至少在隔离环境演练一次：从一对备份恢复，核验一个
Catalog revision 与一个 authoring revision 的 materialized source、OCI digest 和 Incus fingerprint，启动 Server/Worker/Controller，
并确认未完成 finalizer 与资源 reaper 能自然收敛。演练不得把 Registry/Incus 在线探测加入常规 `/readyz`；那会把外部 provider 的短暂故障错误地变成全站不可用。
