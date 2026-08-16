# Agent Runtime

Breakfix uses Eino in Server, PostgreSQL `AgentRun` records, and Server-managed
OpenSandbox workspaces for authoring. It has no Claude Code CLI,
`eino-claude-code`, alternate Agent SDK compatibility path, or generic agent
queue.

## Execution Boundaries

| Role | Executor | Durable boundary | Scheduling |
| --- | --- | --- | --- |
| Authoring | Server | AuthoringSession, messages, AgentRun, private Plan stage | One active run per session; a browser message has a stable idempotency key. |
| Learning Assistant | Server | Assistant session, messages, AgentRun, read-only evidence | One active run per session. |
| Generator client (web) | Server Authoring Agent calling GeneratorService directly | GenerationWorkflow, PlanRevision, CandidateRevision, workflow workspace | A user-confirmed conversation turn. |
| Generator client (external) | Local `breakfix-mcp` stdio connector calling the same GeneratorService over HTTPS | Same as web | One explicit MCP workspace turn per operation. |
| Judge | Server | GenerationWorkflow, PlanRevision, CandidateRevision | Independently claims `Judging` workflows. |
| Classifier | Server | GenerationWorkflow, verified CandidateRevision, immutable RoadmapRevision | Independently claims `Classifying` workflows. |
| Roadmap planner and reviewers | Server | RoadmapTask, fixed RoadmapRevision, ChangeSet/Review, AgentRun | Server maintenance workflow. |
| Build, artifact publish, verification, challenge publish, and provider cleanup | Runtime Worker | Lease-fenced runtime action | Runtime Worker only. |

`AgentRun` is one complete, auditable logical execution. It can make many model
HTTP requests and tool calls; it is neither a resident process nor a generic
queue item. The durable business aggregate remains authoritative for every
typed result.

## Authoring Turns

An Authoring run has one `DeadlineAt`, configured by
`agent.authoring_run_deadline` and defaulting to 30 minutes. The deadline, a
single model request timeout, and the model context window are separate limits.
Authoring uses `math.MaxInt` for the Eino iteration and transient model
transport retry ceilings, so a fixed tool/model iteration count or a fixed
retry count cannot terminate legitimate work before the run deadline.

Temporary model transport failures such as stream receive failures, request
timeouts, HTTP 5xx, and rate limits stay inside the same Eino run while its
context remains valid. Accepted tool results are not replayed. Model
configuration, protocol, or other permanent executor errors end the run. The
public `attempt` field remains an audit value; it is not an Authoring budget.
Judge, Classifier, and Roadmap roles retain their own short, finite retry
policies because they are separate typed background operations.

Workspace tool outcomes are explicit:

- Known command and validation failures are tool results for the model to
  inspect and handle in the same run.
- If a side-effecting call times out or loses its connection after dispatch,
  the tool returns a structured `unknown` outcome. Breakfix does not replay the
  call, retire the workspace, or assume that the command did not run. The
  model can inspect files, processes, services, or other observable state and
  decide what to do next.
- A database persistence retry only retries persistence. It never reruns the
  model or repeats a tool call.

The browser persists a user message, private stage, and AgentRun together. A
repeat POST with the same idempotency key returns the existing run rather than
adding another user message or starting another Eino execution. Only the
request that created the run owns its SSE stream; a reconnect observes durable
session state and does not create a second stream executor.

When a run reaches its deadline, Server stops, Server restarts, or a permanent
executor error occurs, one durable transaction ends the run, discards its
private Plan stage, and appends one deterministic `role=event` message. The
event has a closed reason set: `deadline_exceeded`, `server_stopping`,
`server_restarted`, or `permanent_executor_error`. It is author-visible status,
not model context, so the next Eino input contains only user and assistant
messages. Breakfix never creates an automatic replacement Authoring run; the
next author message starts a new run from durable conversation and workspace
state. Losing an SSE connection alone does not end a run.

## Generator Workspace Recovery

A `GenerationWorkflow` owns a Server-created OpenSandbox Sandbox and dedicated
PVC only while its workspace is active. A Generator turn obtains the workflow's
single-writer binding before it can read, write, execute, archive, or submit.
Another user turn is rejected; an internal snapshot holder is short-lived, and
an interactive turn waits for it rather than exposing that implementation
detail as a client conflict.

The workspace snapshotter is a Server-owned background service. It tries every
30 seconds and is also prompted after a turn releases its binding. While it
holds the same writer fence, it archives only `/workspace` using the canonical,
safe archive format, verifies the digest, writes an immutable file on the
Server data PVC, and then atomically publishes that digest to the workflow.
It does not snapshot the Sandbox operating system, command processes, or tool
execution state. Unchanged snapshots do not create a new version or extend an
existing idle deadline.

`generator_workspace_idle_ttl` is a Server-wide policy and defaults to 24
hours. Idle time begins only after a valid snapshot is published. When the TTL
expires with no active turn, the workspace is marked for deletion; the existing
WorkspaceReaper asynchronously deletes the Sandbox and PVC. The snapshotter
never performs provider cleanup itself. A new turn that wins the row lock first
clears idle status and reuses the current workspace.

A normal Authoring deadline, a tool `unknown` outcome, or a browser/MCP
disconnect preserves the workspace. A Server restart is different: Server
retires every incomplete Generator workspace and the reaper deletes its old
Sandbox and PVC asynchronously. The next Generator operation creates a new
workspace and seeds it in this order: latest valid workspace snapshot, latest
CandidateRevision archive, then an empty scaffold. A corrupt snapshot pointer
is cleared before the fallback. No model context, half-finished command, or
tool result is resumed or replayed.

`submit_candidate` remains the only authoritative candidate validation point.
An invalid submission has no CandidateRevision or workflow-state side effect
and leaves the workspace available for repair. A successful submission makes
the immutable CandidateRevision the next durable fallback; old snapshot files
are reclaimed asynchronously after their grace period.

## Startup And Privileges

Before HTTP readiness, Server recovers durable state. It marks unfinished
Authoring runs interrupted without replacement, interrupts and later reclaims
unfinished Judge/Classifier phases through their workflow state, retires stale
Generator workspaces, and starts the snapshotter and reaper. Learning
Assistant recovery remains separate because its read-only request can be
rebuilt from durable environment facts.

Agent roles have separate prompts, typed outputs, and typed tools. Models do
not receive Kubernetes, Incus, Registry, OpenSandbox, PostgreSQL, or Server
data PVC credentials. Web Authoring tools and the MCP connector call the same
GeneratorService and can operate only on the caller-owned, bound workspace.
The MCP connector only forwards a user JWT and materializes the immutable
review bundle on the client machine; it never uploads local files back to
Server.

Formal external effects become business state first and are then executed by
the Runtime Worker. The Worker has no model API key, OpenSandbox credential,
PostgreSQL DSN, or Server data PVC access.
