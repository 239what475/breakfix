# CPU、内存与资源限制

**Topic ID：**`cpu-memory-and-resource-limits`
**Domain：**Linux 系统与网络运维
**状态：**首轮核心场景设计；依赖有界资源压力 capability probe

## 范围

本 Topic 关注可观察的资源异常与服务级限制。所有压力必须有明确上限、自动清理路径和隔离边界；不把容器资源限制伪装为裸机内核调优，也不让题目影响其他环境。

## 建议前置知识

进程与 systemd 服务；日志与计划任务；基础文件与配置定位。

## 参考资料

- [`systemd.resource-control(5)`](https://www.freedesktop.org/software/systemd/man/latest/systemd.resource-control.html)
- [`getrlimit(2)`](https://man7.org/linux/man-pages/man2/getrlimit.2.html)
- [`proc(5)`](https://man7.org/linux/man-pages/man5/proc.5.html)

## 学习导读

### 学习目标

- 把 load、进程消耗、服务日志与用户可见延迟关联起来。
- 区分持续重试造成的资源问题与单纯重启服务的短暂缓解。
- 识别 service 级 nofile limit 如何在并发时表现为连接或日志错误。

### 核心心智模型

资源事故要同时看系统症状、具体进程和服务配置。系统级负载只是结果；正确修复应改变造成资源异常的受管行为或受管限制，而不是隐藏症状。

### 诊断证据与工具

使用进程资源观察、journal、受控请求和 unit 配置交叉验证。资源数字本身不可脱离时间和业务现象解释，压力场景也必须具有平台定义的上限。

### 常见误区

- 只杀掉当前进程，保留会再次启动的错误配置或 timer。
- 通过无边界提高系统限制、降低检查负载或关闭日志掩盖故障。
- 将轻载健康检查成功误认为并发下服务可靠。

### 面试追问

- 高 load 与 CPU 使用率、I/O wait、可运行队列之间如何区分？
- 如何判断 `Too many open files` 来自进程 limit、systemd limit 还是连接泄漏？

### 推荐关系

建议在 systemd、日志和磁盘 Topic 之后学习；所有场景必须等待资源压力能力验证。

## 核心场景

### `stop-runaway-orders-worker`：处理失控的后台 worker

- **环境地图：**`ops` 同时运行 `orders-api.service` 和由 timer 触发的 `orders-worker.service`；worker 的维护模式受配置控制。
- **本题相关知识：**load、进程资源、重试循环、受管 worker。
- **学习目标：**从系统负载和日志将资源压力归因到具体 worker 行为，而不是误修 API 服务。
- **初始状态：**`orders-worker.service` 的维护模式读取了错误的批处理配置，产生持续占用 CPU 的重试循环；实验环境为该负载设置硬上限，避免影响其他题目节点。
- **用户看到的症状：**`ops` 上 load 持续升高，`orders-api` 响应变慢，worker journal 重复记录同一失败；停止或重启 API 不能消除根因。
- **任务边界：**恢复 worker 的正常、有间隔的失败处理；不能永久 mask timer、杀死无关进程或用优先级/无限制资源配额掩盖循环。
- **完成条件：**worker 不再产生紧密重试循环，API 响应恢复，后续 timer 触发仍能安全完成维护工作。
- **检查点：**worker 的 CPU 使用回到题目定义的有界水平；重复错误日志停止增长；API 健康响应恢复。
- **提示层级：**方向：先从负载归因到进程，再从进程归因到受管服务；证据：比较 CPU 观察、worker journal 和 API 延迟；概念：重试策略没有退避或终止条件时会形成资源放大器。
- **解答与复盘：**确认 API 是受害者而非根因，定位 worker 配置导致的紧密循环，恢复有界重试后观察负载和业务响应共同回落。生产中应为重试频率、队列积压和 CPU 消耗设置联合告警。
- **作者 verifier：**timer 后续仍可工作；不通过永久停用 worker、杀死无关进程或增加无限制资源达成通过；负载自动清理。
- **能力状态：**仅在“资源压力主机” probe 证明 CPU 观察和自动回收可靠后实现。

### `recover-orders-api-nofile-limit`：恢复文件描述符上限

- **环境地图：**`app` 上的 `orders-api.service` 由 systemd 管理，并在受控并发请求下创建连接和日志文件描述符。
- **本题相关知识：**文件描述符、`LimitNOFILE`、服务级限制、并发症状。
- **学习目标：**从轻载/并发行为和 journal 错误识别资源 limit，并将修复限定在目标 unit。
- **初始状态：**`orders-api.service` 的 drop-in 将 `LimitNOFILE` 降为 32。少量请求正常，并发请求后 journal 出现 `Too many open files`，服务拒绝新的连接。
- **用户看到的症状：**轻载健康检查成功，受控并发请求失败；进程和磁盘空间都没有明显异常。
- **任务边界：**把服务限制恢复到实验系统定义的批准值；不能通过降低检查并发、关闭日志或无边界地提高全局系统限制绕过问题。
- **完成条件：**服务在受控并发下保持响应，systemd 对该 unit 的文件描述符限制正确生效。
- **检查点：**服务进程的 nofile limit 达到批准值；受控并发请求全部成功；journal 不再出现文件描述符耗尽错误。
- **提示层级：**方向：比较轻载与并发下的不同表现；证据：检查 journal、进程 limit 和 unit/drop-in；概念：进程可用的文件描述符上限可由 systemd 独立于全局设置控制。
- **解答与复盘：**根据耗尽错误排除网络和磁盘，验证实际进程 limit 与 unit 配置的差异，恢复目标服务的批准上限并重新加载。生产中应对 FD 使用率和接近上限的趋势建立预警。
- **作者 verifier：**未修改无关全局限制；服务重启后 limit 持续生效；检查负载本身未被降低。
- **能力状态：**仅在“资源压力主机” probe 证明 systemd limit 与可重复负载行为可靠后实现。

## 扩展范围

未来可在能力边界明确后覆盖内存压力、OOM、I/O 延迟和 swap；这些题不可在未验证隔离性的环境中实现。
