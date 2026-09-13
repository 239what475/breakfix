# TODO

“可复现运维现场”第一阶段已经完成。具体实施细节、长期契约和真实验收记录分别保留在
`NEXT.md`、`docs/architecture/`、`docs/operations/` 以及 Git 提交历史中。

下一阶段是 `NEXT.md` 定义的“文档实践化”首个 Kubernetes 文档最小闭环。本文件只规划文档展示和阅读集成；Agent 场景生成、场景增量维护和实践环境生成继续保留在 `NEXT.md`。

总体架构是“独立 Hugo 文档镜像 + Breakfix Vue 外层阅读器”：Hugo 负责 Markdown、shortcode、目录、链接、版本和多语言渲染；Breakfix 负责文档入口、阅读上下文以及后续实践入口。Breakfix 不自行解析 Kubernetes Markdown，不把 Hugo 迁移进 Vue，也不把文档反向代理到 Breakfix 的同源路径。

```text
kubernetes/website 固定 commit
        ↓
独立 Hugo 构建和发布
        ↓
docs.breakfix.example
        ↓ iframe
Breakfix Vue 文档阅读器
        ↑ postMessage
页面路径、版本、语言、URL 锚点
```

## 实施计划

### 1. 固定版本的 Hugo 文档镜像

- [x] 新增 `docs-site/manifest.yaml`，固定上游仓库、revision、对外版本、语言、文档前缀和 Hugo 版本。
- [x] 首个快照先使用当前已验证的 Kubernetes website commit `ce98a43f24257385a9766003a6dadc95e962dc63`，对外版本标记为
      `snapshot-ce98a43`；确认对应 Kubernetes 发布版本后再改用正式版本号，不凭日期猜测 `v1.37` 等版本。
- [x] 固定 Hugo `0.144.2`，构建时拉取固定 revision，不把完整上游仓库复制进 Breakfix；使用上游 Dockerfile 在容器内完成构建，宿主机不依赖 Hugo、Node.js 或 npm。Node.js 及 npm 仅作为上游镜像内部依赖，版本遵循上游 Dockerfile。
- [x] 使用 upstream Dockerfile 构建文档镜像，在容器内执行生产 Hugo 构建，并将容器 `/tmp/public` 直接绑定到被忽略的 `docs-site/public/`；宿主机不安装 Hugo、Node.js 或 npm，不把 `content/en/docs` 单独作为新的 `contentDir`，也不重新实现 Hugo 的资源处理流程。
- [x] 原样保留 upstream 的完整 `public/` 目录结构并部署到独立 docs origin；不删除 `/blog`、`/case-studies` 等页面，不对生成后的 HTML、CSS、JS 或链接做 URL 重写。
- [x] Breakfix 的产品入口只指向 docs origin 的 `/docs/`，文档站的其他 upstream 页面不在 Breakfix 导航中暴露；是否限制直接访问由 docs origin/CDN 路由策略决定，不通过篡改 Hugo 产物实现。
- [x] 保留 CC BY 4.0 署名、来源链接、修改说明；未单独确认许可的第三方图片、嵌入和资源暂不同步。
- [x] 生成公开的 `build-info.json`，至少包含 source、revision、version、locale 和构建时间。

首个闭环使用一个固定版本的 Kubernetes website 快照，先验证英文 `/docs/` 入口即可，不要求 Breakfix 暴露全站导航。该快照以独立 docs origin 的根路径部署，例如
`https://docs.breakfix.example/`，由构建配置记录当前 `source`、`version` 和 `revision`；Hugo `baseURL` 必须与该 origin 一致，内部链接和静态资源路径必须在 smoke test 中验证。后续多版本优先使用独立的 docs 部署或 origin；若未来采用路径前缀，必须先验证 upstream 官方配置和部署层支持，不能依赖产物后处理。

### 2. Breakfix 文档阅读器

- [x] 在现有 `AppTopbar`/`AppShell` 中增加公开可见的 `Documentation` 一级入口；未登录用户可以阅读文档，Operations 和 My space 继续按现有登录规则显示。
- [x] 新增 `DocumentationPage.vue` 及对应样式，使用 `iframe` 加载固定文档入口。
- [x] 将文档来源抽象成配置对象，即使首版只有 Kubernetes 一个来源，也包含 `source`、`version`、`locale`、`origin` 和 `entryPath`。
- [x] 支持 iframe 加载中、加载失败、重试和空状态；iframe 内部链接保持在文档 origin 内正常导航。
- [x] 使用 `/documentation?source=...&version=...&path=...&hash=...` 保存阅读位置；进入文档时使用一次 `pushState`，页面变化使用 `replaceState`，刷新后恢复并校验当前页面。
- [x] 在阅读器中展示当前来源、版本和页面路径；文档内部前进/后退由 iframe 处理，Breakfix 外层历史负责离开 Documentation。
- [x] 不读取 iframe 内部 DOM，不把文档正文复制成 Vue 组件。

生产环境使用独立 origin，例如：

```text
app.breakfix.example
docs.breakfix.example
```

### 3. Hugo 上下文脚本与消息契约

- [x] 在 Hugo 公共模板或 partial 中注入统一脚本，不修改每个 Markdown 文件。
- [x] 脚本在首次加载、页面导航、`hashchange` 和前进/后退时发送文档位置消息。
- [x] 第一版消息格式固定为：

```json
{
  "type": "breakfix:document-location",
  "source": "kubernetes",
  "version": "snapshot-ce98a43",
  "locale": "en",
  "path": "/kubernetes/snapshot-ce98a43/en/docs/concepts/services-networking/service/",
  "hash": "#publishing-services"
}
```

- [x] 通过 `BREAKFIX_PARENT_ORIGIN` 在构建时注入允许的 parent origin；本地使用 `http://localhost:5173`，生产使用明确的 Breakfix 域名，不从 iframe URL 接收任意 origin。
- [x] 消息只包含公开的文档位置元数据，脚本使用配置的 `targetOrigin` 发送，不携带 JWT、localStorage 内容或 API 数据。
- [x] Vue 端同时校验 `event.origin`、`event.source`、消息类型、字段格式和允许的文档路径前缀。
- [x] 非配置 origin、其他窗口或伪造消息必须被忽略并可在调试日志中区分。
- [x] 第一版只识别 URL 路径和锚点；使用 `IntersectionObserver` 识别当前 `h2/h3`，以及在标题旁显示实践按钮，列为后续增强。

文档页面不得接触 Breakfix JWT、`localStorage` 或 API。生产文档站响应头只允许明确的 Breakfix origin 作为 `frame-ancestors`，Breakfix 响应头的 `frame-src` 也只允许配置的 docs origin，不能开放任意站点嵌入。

### 4. 构建、运行时镜像和生产发布

- [x] 增加 `make docs-sync`、`make docs-build` 和 `make docs-check`；源码缓存和静态产物均放在被忽略的 `.local/docs/` 下，不提供本地预览服务器。
- [x] `docs-build` 默认使用 Docker/Podman 构建上游工具镜像，将固定 commit 渲染为完整生产 `public/`；固定快照阶段不做自动更新或增量更新。
- [x] 将已校验的完整 `public/` 构建为只包含静态文件和 HTTP 服务器的运行时 Docker 镜像，供本地和 Kind 部署；镜像内容不改写 upstream HTML、CSS、JavaScript 或链接。
- [ ] 生产将 upstream 完整 Hugo `public/` 静态产物部署到独立域名或 CDN 根路径，不与 Breakfix Go 服务共享 origin。
- [ ] 配置 upstream 支持的 `baseURL`、缓存、失败页、`frame-ancestors` 和 Breakfix 的 `frame-src` 响应头；不通过改写产物制造版本路径。
- [ ] 同一镜像内的文档链接和资源路径保持 upstream 规则并留在 docs origin；指向其他站点的链接打开新标签页；版本不存在或跳出允许 origin 的链接在部署配置检查中报告。
- [ ] 记录本地、预发布和生产的 parent origin、docs origin 以及文档版本配置，避免构建产物与环境不匹配。

### 5. 验收测试

- [x] 浏览器测试验证 Documentation 入口和固定版本首页可以打开。
- [x] 浏览器测试验证 iframe 内部导航、锚点变化和当前路径展示。
- [x] 测试验证非法 origin、非法 `event.source`、未知消息类型和无效字段都会被拒绝。
- [x] 测试验证文档加载失败、重试以及文档站非文档路径拒绝。
- [x] 新增固定的 `test/fixtures/documentation/` 静态文档 fixture，由独立端口提供，不依赖 `/tmp` checkout 或外网。
- [x] 使用真实固定 upstream 执行一次官方生产构建 smoke test，验证完整 `public/` 结构、`/docs/` 页面模板、脚本注入、baseURL、资源路径和 `build-info.json`。
- [x] 在本地双端口配置下完成一次构建验证。
- [ ] 在生产独立域名配置下完成一次构建验证。

## 提交拆分

每完成一个独立步骤及其对应验证就立即提交，不把多项工作积累成一次大提交：

1. **Hugo 文档镜像基础**：固定 Kubernetes website revision，新增 `docs-site/` 官方构建调用、独立 origin 部署说明、许可证署名和镜像元数据；完成固定版本完整 `public/` 和 `/docs/` 入口构建验证。
2. **文档阅读器入口**：增加 Documentation 顶层入口、来源配置和 iframe 阅读器；完成 Vue 构建以及加载、失败和重试状态验证。
3. **文档上下文桥接**：在 Hugo 公共模板注入位置上报脚本，在 Vue 中实现 `postMessage` 契约、来源校验和页面状态展示；完成页面导航与锚点传递验证。
4. **运行时镜像与生产部署**：补充静态站点运行时镜像、独立 docs origin 部署配置、版本部署说明、CSP/iframe 响应头和运行说明；完成容器构建、镜像运行与独立域名配置检查。
5. **嵌入验收收尾**：补齐浏览器测试，覆盖文档入口、iframe 导航、非法消息拒绝、加载失败和非文档路径拒绝；执行本阶段约定的静态、构建和浏览器验收。

本阶段明确不做：Agent 阅读和场景生成、正文提取、标题滚动识别、实践环境启动、文档更新后的场景增量复验。这些任务属于 `NEXT.md`。
