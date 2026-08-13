# Agent Runtime

Breakfix uses Eino, PostgreSQL `AgentRun` records, and a Server-managed remote
OpenSandbox workspace for model capabilities. It has no Claude Code CLI,
`eino-claude-code`, or alternate Agent SDK compatibility path.

## Execution Boundaries

| Role | Executor | Durable boundary | Scheduling |
| --- | --- | --- | --- |
| Authoring | Server | AuthoringSession, messages, AgentRun, private Plan stage | One active run per session. |
| Learning Assistant | Server | Assistant session, messages, AgentRun, read-only evidence | One active run per session. |
| Generator client（网页） | Server：Authoring AgentRun 直接调用共享 GeneratorService | GenerationWorkflow, PlanRevision, CandidateRevision, workflow workspace | 用户确认后的对话回合；同一 workflow 单写者。 |
| Generator client（外部） | 本机 `breakfix-mcp` stdio connector，经 HTTPS 调用同一 GeneratorService | 同上 | 每次 workspace 操作一个显式 MCP 回合。 |
| Judge | Server | GenerationWorkflow, PlanRevision, CandidateRevision | Server independently claims each `Judging` workflow. |
| Classifier | Server | GenerationWorkflow, verified CandidateRevision, immutable RoadmapRevision | Server independently claims each `Classifying` workflow. |
| Roadmap planner and reviewers | Server | RoadmapTask, fixed RoadmapRevision, ChangeSet/Review, AgentRun | Server maintenance workflow. |
| Build, artifact publish, verification, challenge publish, reaping | Runtime Worker | A lease-fenced runtime action | Runtime Worker only. |

`AgentRun` is one complete, auditable logical execution. It may issue multiple
model HTTP requests and tool calls, but is not a resident process or a generic
queue task. A known technical error may create a fresh Eino instance for the
same run up to five times. The durable business aggregate remains authoritative
for its typed result.

## Recovery

Server execution does not use Eino checkpoint/resume, replay tool results, or a
second runtime state store. The Server persists only fixed input references,
business revisions, AgentRun metadata, Authoring/Assistant messages, and the
current Generator workspace binding.

On a Server restart, interrupted Judge/Classifier AgentRuns are marked
`Interrupted`, their claim is released, and the Server starts a new run from
committed facts with a new five-attempt budget. Active Authoring and Assistant
runs are replaced before readiness and automatically continue from their
durable user message and fixed input; an Authoring replacement discards the old
private stage. A stale Authoring tool call is fenced by both Run attempt and
stage revision.

Generator workspace turns are a different boundary. A normal tool failure, a
disconnected turn, or a failed Authoring/MCP request ends that turn and
releases the workflow's workspace binding while keeping the workflow and its
workspace. A Server restart retires every unfinished Generator workspace: the
background reaper deletes the old Sandbox and PVC, and the next Generator
client operation creates a fresh workspace seeded from the most recently
submitted CandidateRevision (or the initial Plan scaffold). Half-finished
commands and tool results are never recovered or replayed, and the local MCP
review cache never participates in recovery.

Roadmap recovery follows the same interruption rule, but its replacement is
selected from the task's committed semantic boundary. If the Planner result is
absent, only an interrupted Planner Run is replaced; once a ChangeSet exists,
only reviewers without a persisted review are replaced. The replacement keeps
the original model input and starts with attempt one. It does not change the
task's semantic round or role-call counter, and does not resume model context
or tool execution. Lease takeover uses the identical rule.

Server bootstrap completes this durable recovery before it exposes HTTP
readiness. Recovered Authoring and Assistant replacements run under an
explicit application lifecycle, while direct browser turns remain active HTTP
requests. Shutdown first stops HTTP intake, then cancels and waits for all
Server-owned Agent execution before closing shared providers.

SSE observes a Server-owned interactive run. Losing the browser connection only
ends that subscription: it neither cancels the AgentRun nor persists partial
stream text. The final Assistant message or Authoring revision is committed
atomically and is available after the page reconnects.

Each confirmed Plan revision owns its own GenerationWorkflow. Explicit Plan
confirmation is idempotent and freezes that Plan revision, so the author can
confirm another revision for another workflow without replacing the first.
The author can only request content changes, request classification changes,
or cancel through the version-bound Generator tools; cancellation is
owner-scoped and retires the workspace in the background. `Failed` is terminal
and never resumes automatically.

The Server dispatches claimable `Judging` and `Classifying` rows independently
and does not introduce a fixed concurrency value, slot, pool, or second queue.
A dispatched execution ends after its current phase commits a state transition;
review and Runtime Worker states do not hold a Server agent execution open. On
shutdown, dispatched executions receive cancellation and are awaited;
cancellation is not recorded as a technical AgentRun retry, and startup
recovery handles the interrupted durable runs.

## Tools And Privileges

Agent roles retain separate prompts, typed outputs, and typed tools. The model
does not receive Kubernetes, Incus, Registry, OpenSandbox, PostgreSQL, or Server
data PVC credentials.

Generator workspace tools run in Server and are fenced by the current
workflow's explicit Generator turn binding. Web Authoring tools and the MCP
connector invoke the identical GeneratorService, and both can read and write
only the bound workspace and execute commands there. The MCP connector adds no
credentials: it only forwards a user JWT, materializes the immutable review
bundle into a disposable local cache, and never uploads local files back to the
Server. Classifier tools can read only their pinned RoadmapRevision. Authoring
tools can only update a private Plan stage. Assistant tools are read-only
against the current learning Environment.

Formal external effects first become business state and are then executed by
the Runtime Worker. The Worker has no model API key, OpenSandbox credential,
PostgreSQL DSN, or Server data PVC access.
