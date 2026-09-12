# 备份与恢复

本文定义 Breakfix 的恢复单元和人工演练步骤。它不增加恢复服务、对象存储、自动备份控制器或新的 readiness provider。

## 恢复单元

PostgreSQL 与 Server data PVC 是同一个权威恢复单元，必须在同一维护窗口、没有写者时一起备份和恢复：

- PostgreSQL 保存用户、会话、`GenerationWorkflow`、`CatalogRelease`、Scenario/revision identity、发布 intent 与清理 intent。
- `breakfix-server-data` PVC 保存 materialized 场景目录、持久化 Catalog source 和 candidate archive。

Registry 与 Incus 是数据库引用的外部 immutable artifact provider，不属于此恢复单元：K8s artifact 必须仍可按 OCI digest 读取，
Node artifact 必须仍可按 Incus fingerprint 读取。它们的备份由部署者维护。不能把 PostgreSQL/PVC 与外部 provider 假装为原子快照；
恢复时必须显式核验引用。

默认 namespace 是 `breakfix-system`。固定写入组件是 `breakfix-server`、`breakfix-runtime-worker`、
`breakfix-controller` 与 `breakfix-postgresql`；PVC 是 `breakfix-server-data` 与 `data-breakfix-postgresql-0`。实际 namespace、
StorageClass、Registry 与 Incus project 以部署配置为准。

## 维护窗口与备份

1. 在 Ingress 或负载均衡器停止向 Server 发送新请求。平台没有维护模式 API。
2. 记录维护开始时间、当前 `catalog.release_reference`、已发布 Scenario/revision 清单、PostgreSQL 备份标识和两个 PVC 快照标识。
3. 观察正在执行的 Generation、Catalog 与 Environment 工作，等待可安全完成的工作结束。必须中断的工作保留未完成 intent；
   不要手工伪造完成状态。
4. 停止所有应用写者，并确认 Server data PVC 没有挂载者：

   ```bash
   kubectl -n breakfix-system scale deployment/breakfix-runtime-worker --replicas=0
   kubectl -n breakfix-system scale deployment/breakfix-controller --replicas=0
   kubectl -n breakfix-system scale deployment/breakfix-server --replicas=0
   kubectl -n breakfix-system get pods -o wide
   kubectl -n breakfix-system describe pvc breakfix-server-data
   ```

5. 在无写者窗口备份 PostgreSQL 和 `breakfix-server-data`。可以使用已有的逻辑备份、CSI VolumeSnapshot 或存储后端快照，
   但两份备份必须属于同一窗口。若备份 PostgreSQL 数据卷而非逻辑 dump，先停止 `breakfix-postgresql` StatefulSet。
6. 保存备份校验和、位置、配置版本和 artifact provider 的保留策略。备份失败或窗口中出现写入时，丢弃这对备份并重新开始。

## 恢复顺序

在完成数据库、PVC 与外部 artifact 核验前，保持 Server、Runtime Worker 与 Controller 为零副本，入口流量关闭。

1. 从同一维护窗口恢复 PostgreSQL。先启动 PostgreSQL，确认数据库、凭据和 `breakfix_schema` 与部署的 Server 二进制一致。
2. 恢复 `breakfix-server-data` PVC。不要通过重新安装 Catalog、复制 Git 工作区或手工重建目录替代该 PVC；这会破坏已发布 revision
   与 materialized revision 的对应关系。
3. 通过 PostgreSQL 的只读查询列出当前 active Scenario/revision/materialized revision，并在恢复后的 Server PVC 核对相同目录和
   revision。不要修改 active pointer 或启动任何写入组件。
4. 使用部署者已有的 Registry API/client 核验每个当前或未完成发布 intent 的 `OCIReference` manifest digest；使用 Incus API 核验每个
   Node artifact 的完整 fingerprint、image project 和镜像类型。`/capabilities/node-provider` 可辅助检查当前连通性，但不能替代历史
   artifact 完整性核验。
5. 外部 artifact 通过后，在仍不接收流量的条件下启动一份 Server：

   ```bash
   kubectl -n breakfix-system scale deployment/breakfix-server --replicas=1
   kubectl -n breakfix-system rollout status deployment/breakfix-server
   kubectl -n breakfix-system port-forward service/breakfix-server 9090:9090
   curl --fail http://127.0.0.1:9090/readyz
   ```

   `/readyz` 会核验 active revision 到 materialized source 的完整性，但不会探测 Registry 或 Incus。以只读方式检查
   `/api/operations/scenarios` 的 identity、revision 和数量符合恢复清单。
6. 只有 PostgreSQL、PVC、active revision、Registry digest 与 Incus fingerprint 一致时，启动 Runtime Worker 与 Controller，随后恢复
   入口流量。

Server 启动后恢复已有 finalizer、lease、Catalog installer、Judge 和 interactive AgentRun 边界；Runtime Worker 恢复可领取 action
与资源回收。不要新建第二套恢复队列、手工重放已持久化的 provider action，或把日志当作恢复权威。

## 不一致处置

| 发现 | 处置 |
| --- | --- |
| PostgreSQL 与 Server PVC 不属于同一窗口，或 schema/active revision 不一致 | 保持所有写入组件停止，恢复匹配的一对备份；不能只重置一侧后宣布恢复完成。 |
| active revision 指向缺失、变更或 hash 不匹配的 materialized 目录 | 保持入口关闭；不要静默过滤场景或修改 active pointer。恢复正确 PVC 后重新完整核验。 |
| Registry 缺少数据库引用的 OCI digest，或 digest 指向不同 manifest | 保持相关 workflow/release 不可服务，恢复 Registry artifact 或通过正常 Build/Verify/Publish 创建新 revision；不得改写旧 revision 的 artifact reference。 |
| Incus 缺少或不匹配数据库引用的 fingerprint | 恢复匹配 image project/镜像，或通过正常生命周期生成新 revision；不得用 alias 或当前 base image 冒充历史 fingerprint。 |

任一不一致都不是“忽略后继续”的场景。记录受影响 workflow、release、Scenario revision 和 artifact identity，保持写入组件停止直到
恢复证据完整。现有 finalizer/reaper 只处理已经持久化的幂等 intent，不会从不一致备份组合猜测或重写权威数据。

## 演练验收

每次变更备份策略、StorageClass、Registry 或 Incus 拓扑后，至少在隔离环境演练一次：从一对备份恢复，核验一个 Catalog revision 和
一个 authoring revision 的 materialized source、OCI digest 和 Incus fingerprint，启动 Server/Worker/Controller，并确认未完成
finalizer 与资源 reaper 能自然收敛。演练不得把 Registry/Incus 在线探测加入常规 `/readyz`。
