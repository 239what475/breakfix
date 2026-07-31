# Open edX Platform 对照审查

## 审查对象

- 本地路径：`/home/what/myproject/breakfix-similar-projects/openedx-platform`
- 审查提交：`3ebe2fa`
- 定位：AGPL-3.0 的大型在线教育平台核心；README 将其描述为 CMS、LMS、独立应用和
  micro-frontend 的组合。

## 已确认的设计

1. `README.rst` 明确区分 CMS/Studio 的内容创作与 LMS 的学习交付，并说明生产部署复杂。
2. `cms/`、`lms/` 与 `openedx/core/` 显示课程内容、学习交付、完成状态、评分、搜索和
   事件属于不同子系统；`lms/envs/common.py` 将 completion、courseware、courseware history
   作为独立应用能力。
3. `cms/lib/xblock/` 以 XBlock 作为可扩展内容单元，支持内容库、结构化标签、发布和
   生命周期事件。

## 与 Breakfix 的对照

Open edX 的核心对象是课程、章节、单元和可插拔教学组件；Breakfix 的核心对象是经过真实
验证的 challenge artifact 和可回收运行环境。Breakfix 已把发布内容、taxonomy snapshot、
用户学习记录和 CRD runtime 分开，这恰好避免了 Open edX 这种通用 LMS 单体需要处理的
复杂性。当前没有班级、作业、证书或组织级集成需求。

## 可以吸收

### 现在吸收

- **内容发布与学习事实分离**：继续把 challenge revision 视为不可变发布物，把用户 attempt
  和 checkpoint event 存 PostgreSQL。题目修订不能重写历史学习数据；新 revision 必须有
  新 CandidateRevision 验证记录和新 taxonomy mapping。这是 Open edX 内容版本/学习记录分离给出的正确
  原则，且与现有 Breakfix 权威来源一致。
- **最小领域事件字典**：围绕已验证的学习行为定义稳定事件，而不是让前端分析日志成为事实。
  起点是 attempt、checkpoint first passed、completed、hint opened、solution opened、
  assistant asked 和 reset；事件携带 challenge ID/revision 与 Environment UID。

### 题库具备数据后再做

- 在具有多个已验证 Skill 领域后，创建人工学习路径。路径是 taxonomy 的只读编排视图，
  用户路径进度是数据库数据；它不演化成 Open edX 的完整课程运行时。
- 若出现机构需求，再评估以 LTI、SSO 或外部 LMS 集成暴露完成事件；不要将 LMS 搬入
  Breakfix。

## 不采用

- 不引入 XBlock 通用插件、内容数据库、CMS/LMS 双系统、课程权限、考试、证书、讨论区或
  微前端架构。它们会把当前可审计的 challenge 格式和发布边界变成难以验证的通用内容系统。
- 不把 Tag/Skill 变成 Studio 可任意编辑的页面字段。taxonomy mapping 已有独立审查和原子
  snapshot 发布，必须保持。

## 结论

Open edX 的有效借鉴是长期内容版本与学习事件建模，不是其平台实现。当前应补最小事件和
revision 历史，再等真实题库证明学习路径或外部 LMS 的必要性。
