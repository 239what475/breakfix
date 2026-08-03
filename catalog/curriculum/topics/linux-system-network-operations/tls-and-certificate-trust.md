# TLS 与证书信任

**Topic ID：**`tls-and-certificate-trust`
**Domain：**Linux 系统与网络运维
**状态：**首轮核心场景设计，需私有 CA 和固定证书资产 capability probe

## 范围

本 Topic 覆盖私有服务中的证书有效期、服务身份和客户端 CA 信任。所有 CA、证书、名称与服务都由训练环境提供；修复不能通过 `--insecure`、关闭 hostname 校验或信任任意自签证书达成。

## 建议前置知识

DNS 与名称解析；端口、TCP 与 HTTP 服务；用户、组与权限。

## 参考资料

- [`openssl-s_client(1)`](https://docs.openssl.org/master/man1/openssl-s_client/)
- [`x509(1)`](https://docs.openssl.org/master/man1/openssl-x509/)
- [RFC 5280: X.509 PKI Certificate and CRL Profile](https://www.rfc-editor.org/rfc/rfc5280)
- [RFC 6125: Service Identity in TLS](https://www.rfc-editor.org/rfc/rfc6125)

## 学习导读

### 学习目标

- 将 TCP/HTTP 可达与 TLS 身份、有效期和信任链问题区分开来。
- 理解服务端证书部署与客户端 trust store 分别由谁负责。
- 在保持验证开启的前提下恢复内部 HTTPS 服务。

### 核心心智模型

TLS 成功需要同时满足连接可达、证书时间有效、请求名称匹配 SAN，以及客户端信任签发链。服务端提供新证书不能自动使每个 client 信任其 CA；`--insecure` 只是跳过验证，不是修复信任。

### 诊断证据与工具

从正常 TLS 客户端报错、握手中实际提供的证书、证书元数据和系统 trust store 分别收集证据。不要只检查磁盘上的证书文件，因为 Nginx 可能尚未加载它。

### 常见误区

- 用 `curl -k` 或关闭 hostname verification 作为长期方案。
- 仅替换证书而没有验证私钥配对、SAN 或已加载版本。
- 将单个 leaf 证书误装入全局 CA 信任库。

### 面试追问

- 有效证书仍报 hostname mismatch 时，应检查什么字段？
- 如何区分服务端未发送正确证书链和客户端缺少根 CA？

### 推荐关系

建议在 DNS 和 HTTP 链路 Topic 之后学习；权限 Topic 对私钥访问边界提供补充。

## 核心场景

### `replace-expired-gateway-certificate`：更换过期的 gateway 证书

- **环境地图：**gateway 为 `api.training.internal` 提供 HTTPS；client 已信任训练私有 CA，Nginx 从固定 TLS 资产路径加载证书对。
- **本题相关知识：**证书有效期、SAN、证书/私钥配对、TLS 握手、Nginx reload。
- **学习目标：**从客户端证书校验失败定位服务端实际提供的过期证书，并安全部署给定的新证书对。
- **初始状态：**gateway 为 `api.training.internal` 提供 HTTPS。当前证书已过期，训练资产目录中提供了由同一私有 CA 签发、名称匹配且配对正确的新证书和私钥。
- **用户看到的症状：**Nginx 仍在服务，但 client 的正常 TLS 校验因证书有效期失败；使用 `-k` 可以掩盖故障。
- **任务边界：**安全安装题目提供的正确证书对并使 Nginx 生效；不能禁用 TLS 校验、改用 HTTP、使用不匹配私钥或将私钥放宽为普通用户可读。
- **完成条件：**client 通过正常信任链连接 `https://api.training.internal`，服务提供未过期且名称正确的新证书。
- **检查点：**TLS 握手验证成功；服务提供的 leaf 证书有效且包含正确服务名称；HTTPS API 返回预期响应。
- **提示层级：**方向：比较 client 在握手中看到的证书与磁盘上的候选资产；证据：检查有效期、SAN、加载配置和 key pair；概念：客户端校验的是服务实际提供的证书，而非管理员预期存在的文件。
- **解答与复盘：**由过期错误确认 TLS 层问题，验证新证书对并使 Nginx 加载它，再从 client 进行完整握手和 API 验证。生产中应在到期前轮换并对线上提供的证书做独立监测。
- **作者 verifier：**私钥权限保持严格；未关闭验证或降级为 HTTP；服务 reload/restart 后仍提供新证书。
- **能力状态：**需“内部 Web 服务链路”中的私有 CA probe。

### `install-training-ca-trust`：安装训练私有 CA 信任

- **环境地图：**gateway 的 `api.training.internal` 证书由训练 CA 签发；client 通过系统 trust store 执行正常 HTTPS 请求。
- **本题相关知识：**CA、trust store、证书链、hostname 验证、验证绕过。
- **学习目标：**区分服务身份正确但 client 缺少信任根的故障，并用系统支持的信任路径恢复验证。
- **初始状态：**gateway 的服务证书有效且包含正确 SAN，`client` 却没有安装训练 CA。一个调用脚本临时加入了 `curl --insecure`，掩盖了真实性能和信任问题。
- **用户看到的症状：**正常 TLS 请求报告 issuer 不可信；带 `--insecure` 的脚本看似成功。
- **任务边界：**把给定私有 CA 安装到 client 的系统信任路径并移除不安全绕过；不能关闭全局验证或把任意服务证书加入信任库。
- **完成条件：**标准 HTTP 客户端在不使用绕过选项时信任 gateway，同时未知自签证书仍不可信。
- **检查点：**普通 TLS 请求成功；训练 CA 签发的服务可被系统 client 信任；未由训练 CA 签发的测试证书仍被拒绝。
- **提示层级：**方向：区分“服务端身份错误”与“client 信任根缺失”；证据：比较证书 issuer、client 报错和本机 trust store；概念：安装一个 CA 只信任它签发的链，不等同于跳过所有验证。
- **解答与复盘：**确认服务证书本身正确后，将给定 CA 加入 client 的受支持信任库并更新信任数据，再验证正常请求与不可信证书都得到正确结果。生产中应通过受控 CA 分发而非修改每个调用命令的校验选项。
- **作者 verifier：**调用脚本不含 `--insecure`；未将 leaf 证书或任意证书作为全局根信任；信任更新在 client 重启后保持。
- **能力状态：**需“内部 Web 服务链路”中的私有 CA probe。

## 扩展范围

fullchain、mTLS、backend TLS 验证和证书名称迁移可在首轮证书资产稳定后加入；不得以关闭验证作为解决方案。
