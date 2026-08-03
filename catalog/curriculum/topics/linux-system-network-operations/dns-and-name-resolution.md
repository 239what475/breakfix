# DNS 与名称解析

**Topic ID：**`dns-and-name-resolution`
**Domain：**Linux 系统与网络运维
**状态：**首轮核心场景设计，需私有 resolver capability probe

## 范围

本 Topic 训练学习者区分应用实际使用的名称解析路径、`/etc/hosts` 覆盖和私有 DNS 记录。所有名称、解析器、记录和目标服务都在训练私网内，不依赖公网 DNS 或宿主机 resolver。

## 建议前置知识

Shell、文件与配置定位；内部 Web 服务链路的基本拓扑。

## 参考资料

- [`hosts(5)`](https://man7.org/linux/man-pages/man5/hosts.5.html)
- [`resolv.conf(5)`](https://man7.org/linux/man-pages/man5/resolv.conf.5.html)
- [`getent(1)`](https://man7.org/linux/man-pages/man1/getent.1.html)

## 学习导读

### 学习目标

- 区分直接 DNS 查询与系统实际名称服务解析结果。
- 理解 `/etc/hosts`、NSS 顺序、resolver 和 zone 记录的优先关系。
- 在不把服务硬编码为 IP 的前提下恢复私网名称访问。

### 核心心智模型

名称解析是一条链路：应用通常通过系统 resolver 查询，而系统 resolver 可能先命中本地 files，再查询 DNS。`dig` 的正确结果不能证明应用最终会使用同一地址。

### 诊断证据与工具

分别查询指定 resolver、系统名称服务结果和实际 HTTP 请求。通过“按地址可用、按名称失败”或“DNS 正确、系统解析错误”缩小故障层，而不是一开始修改每个 client。

### 常见误区

- 将 `dig` 成功等同于应用可通过名称访问服务。
- 在所有 client 上添加 hosts 条目以绕过权威 zone 问题。
- 把稳定的内部服务名替换成硬编码 IP。

### 面试追问

- 为什么 `dig` 与 `getent hosts` 可能返回不同结果？
- `/etc/hosts` 出现过期记录时，为什么服务端 DNS 记录正确仍不足够？

### 推荐关系

建议在基础文件定位之后学习，并作为 HTTP、TLS 和 SSH 多节点题的前置。

## 核心场景

### `remove-stale-hosts-override`：移除过期的 hosts 覆盖

- **环境地图：**`client -> gateway -> app`；`resolver` 中 `api.training.internal` 正确指向 gateway，client 的系统名称服务优先读取本地 files。
- **本题相关知识：**`/etc/hosts`、NSS 顺序、系统解析、权威 DNS 记录。
- **学习目标：**解释 DNS 查询正确但应用访问错误的原因，恢复系统解析与私有 DNS 的一致性。
- **初始状态：**`resolver` 中 `api.training.internal` 的 A 记录正确指向 gateway，但 `client` 的 `/etc/hosts` 仍把该名称指向已退役地址；名称服务顺序优先使用 files。
- **用户看到的症状：**向 resolver 查询得到正确地址，应用和普通系统解析却仍访问错误主机，HTTP 请求失败。
- **任务边界：**恢复规定名称的正确解析路径；不能把应用改为硬编码 IP、修改正确的 resolver 记录，或删除其他仍需要的 hosts 条目。
- **完成条件：**client 的系统解析与私有 DNS 一致，并通过名称访问 gateway 的 API。
- **检查点：**`getent` 返回 gateway 的规定地址；`api.training.internal` 的 HTTP 请求成功；resolver 仍返回同一权威记录。
- **提示层级：**方向：比较直接 DNS 查询和系统名称服务的不同结果；证据：检查 nameservice 顺序与本地映射；概念：`/etc/hosts` 可在 DNS 查询之前命中。
- **解答与复盘：**由 DNS 与系统解析不一致定位本地覆盖，只修复冲突映射后从客户端名称请求验证。生产中应将临时 hosts 修改纳入到期清理，避免它们长期覆盖服务发现。
- **作者 verifier：**无关 hosts 映射未删除；未以硬编码 IP 取代服务名；重启 resolver 或客户端后解析仍正确。
- **能力状态：**可在“内部 Web 服务链路”实现。

### `repair-orders-api-a-record`：修复私有 DNS A 记录

- **环境地图：**`resolver` 托管 `training.internal`；`orders-api.training.internal` 应指向 gateway，app 与 gateway 均在私网中运行。
- **本题相关知识：**DNS zone、A 记录、权威 resolver、服务名称与地址直连。
- **学习目标：**在地址访问已正常时，把 `NXDOMAIN` 限定为权威记录或 resolver 配置问题。
- **初始状态：**`resolver` 托管 `training.internal`，`orders-api.training.internal` 的 A 记录因手工变更丢失；app 与 gateway 都在运行，按地址访问服务正常。
- **用户看到的症状：**client 用服务名称请求时得到 `NXDOMAIN`，使用文档提供的私网地址能得到正确 HTTP 响应。
- **任务边界：**只修复私有 zone 中缺失的记录和必要的 reload；不能在每个 client 上添加 hosts 条目、将名称指向公网或改变服务监听。
- **完成条件：**私有 resolver 对服务名称返回规定 gateway 地址，client 可通过名称稳定访问 API。
- **检查点：**resolver 返回正确 A 记录；client 的系统解析一致；通过名称的 HTTP 请求成功。
- **提示层级：**方向：先验证应用链路是否能按地址工作；证据：比较权威查询、系统解析和 zone 内容；概念：`NXDOMAIN` 说明权威名称不存在，而不是服务端口失败。
- **解答与复盘：**利用地址直连排除 HTTP 服务故障，再恢复 zone 中唯一的服务记录并安全生效。生产中应对 zone 变更进行语法检查和名称解析回归验证。
- **作者 verifier：**未在 client 上加入替代 hosts 记录；无关 zone 记录不变；resolver reload 后记录持续存在。
- **能力状态：**可在“私网访问链路”实现。

## 扩展范围

搜索域、CNAME、负缓存、多个 resolver 与 DNSSEC 不在首轮范围；新增题必须仍可在环境私网内确定性验证。
