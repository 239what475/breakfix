<script setup lang="ts">
import { computed, onMounted, onScopeDispose, ref, watch } from "vue";
import { FileCode2, MessageSquareText, Send } from "lucide-vue-next";
import { APIError, api, streamAuthoringMessage } from "../../api/client";
import type {
  AuthoringAsset,
  AuthoringFileDiff,
  AuthoringMessage,
  AuthoringSession,
  GeneratorGeneration,
  GeneratorWorkflow,
} from "../../api/types";
import MarkdownDocument from "../workspace/MarkdownDocument.vue";
import "./authoring.css";

const props = defineProps<{ initialSessionId?: string }>();
const session = ref<AuthoringSession>();
const generation = ref<GeneratorGeneration>();
const selectedWorkflowID = ref("");
const message = ref("");
const busy = ref(false);
const error = ref("");
const activeTab = ref("overview");
const activeAsset = ref("");
const activeDiff = ref("");
const narrowPane = ref<"plan" | "chat">("plan");
const pendingMessages = ref<AuthoringMessage[]>([]);
let pollTimer: number | undefined;
let streamController: AbortController | undefined;

const sessionStateLabel: Record<string, string> = {
  DraftConversation: "等待题意",
  IntentReview: "题意约定",
  Published: "已发布",
};
const workflowStateLabel: Record<string, string> = {
  Generating: "等待生成回合",
  Judging: "正在审查",
  Building: "正在构建",
  ArtifactPublishing: "正在发布候选产物",
  Verifying: "正在真实验证",
  NeedsAuthorReview: "等待内容审核",
  ScenarioPublishing: "正在发布挑战",
  Published: "已发布",
  Failed: "基础设施失败",
  Cancelled: "已取消",
};

const workflows = computed(() => session.value?.workflows ?? []);
const selectedWorkflow = computed<GeneratorWorkflow | undefined>(() =>
  workflows.value.find((workflow) => workflow.id === selectedWorkflowID.value) ?? workflows.value[0],
);
const activeWorkflow = computed<GeneratorWorkflow | undefined>(() => {
  const reviewed = generation.value?.workflow;
  if (reviewed && reviewed.id === selectedWorkflowID.value) return reviewed;
  return selectedWorkflow.value;
});
const candidate = computed(() => generation.value?.candidate);
const verified = computed(() => generation.value?.verified);
const verification = computed(() => generation.value?.verification);
const assets = computed(() => generation.value?.assets ?? []);
const diff = computed(() => generation.value?.diff ?? []);
const usingVerifiedRevision = computed(() => !!verified.value);
const displayMetadata = computed(
  () => verified.value?.metadata ?? session.value?.intent.metadata,
);
const checkpoints = computed(() => {
  if (verified.value) {
    return verified.value.checkpoints.map((checkpoint, index) => ({
      id: checkpoint.id,
      title: checkpoint.title,
      markdown: `${checkpoint.description}${checkpoint.hint ? `\n\n提示文件：\`${checkpoint.hint}\`` : ""}${checkpoint.node ? `\n\n执行节点：\`${checkpoint.node}\`` : ""}`,
      position: index + 1,
    }));
  }
  return [...(session.value?.intent.checkpoints ?? [])].sort(
    (left, right) => left.position - right.position,
  );
});
const tabs = computed(() => {
  const entries = [
    { id: "overview", label: "概览" },
    ...checkpoints.value.map((checkpoint, index) => ({
      id: `checkpoint:${checkpoint.id}`,
      label: `检查点 ${index + 1}`,
    })),
  ];
  if (candidate.value) {
    entries.push({ id: "assets", label: "Assets" });
    entries.push({ id: "diff", label: "Diff" });
  }
  if (verification.value) entries.push({ id: "verification", label: "验证" });
  return entries;
});
const overview = computed(() => {
  const metadata = displayMetadata.value;
  if (!metadata) return "";
  const problem = assets.value.find((asset) => asset.path === "problem.md")?.content;
  const body = usingVerifiedRevision.value
    ? problem || "已验证题目未包含可展示的 problem.md。"
    : session.value?.intent.overview || "等待 agent 写入题意约定。";
  return `# ${metadata.title || "未命名题目"}\n\n${metadata.description || "等待 agent 根据题意补全简介。"}\n\n- **运行时**：${metadata.runtime || "待定"}\n\n${body}`;
});
const activeCheckpoint = computed(() => {
  const id = activeTab.value.replace("checkpoint:", "");
  return checkpoints.value.find((checkpoint) => checkpoint.id === id);
});
const selectedAsset = computed<AuthoringAsset | undefined>(() =>
  assets.value.find((asset) => asset.path === activeAsset.value) ?? assets.value[0],
);
const selectedDiff = computed<AuthoringFileDiff | undefined>(() =>
  diff.value.find((entry) => entry.path === activeDiff.value) ?? diff.value[0],
);
const workflowState = computed(() => activeWorkflow.value?.state);
const displayState = computed(() => workflowState.value ?? session.value?.state ?? "");
const displayStateLabel = computed(() =>
  workflowState.value
    ? workflowStateLabel[workflowState.value]
    : sessionStateLabel[session.value?.state ?? ""],
);
const canCompose = computed(() => {
  const current = session.value;
  return !!current && !busy.value && !current.authoring_turn_active && current.state !== "Published";
});
const canSend = computed(() => canCompose.value && message.value.trim().length > 0);
const displayMessages = computed(() => [
  ...(session.value?.messages ?? []),
  ...pendingMessages.value,
]);

function clearPoll() {
  if (pollTimer !== undefined) window.clearTimeout(pollTimer);
  pollTimer = undefined;
}

function shouldPoll() {
  if (session.value?.authoring_turn_active) return true;
  return workflows.value.some((workflow) => [
    "Judging",
    "Building",
    "ArtifactPublishing",
    "Verifying",
    "ScenarioPublishing",
  ].includes(workflow.state));
}

function schedulePoll() {
  clearPoll();
  if (!shouldPoll() || !session.value) return;
  pollTimer = window.setTimeout(() => void refresh(), 3000);
}

function syncWorkflowSelection() {
  const selected = selectedWorkflow.value;
  const nextID = selected?.id ?? "";
  if (nextID === selectedWorkflowID.value) return;
  selectedWorkflowID.value = nextID;
  generation.value = undefined;
  activeAsset.value = "";
  activeDiff.value = "";
}

async function refreshGeneration() {
  const workflowID = selectedWorkflowID.value;
  if (!workflowID) {
    generation.value = undefined;
    return;
  }
  try {
    const value = await api.getGeneration(workflowID);
    if (selectedWorkflowID.value === workflowID) generation.value = value;
  } catch (cause) {
    if (selectedWorkflowID.value === workflowID) {
      error.value = cause instanceof Error ? cause.message : "读取生成审核失败";
    }
  }
}

async function refresh(force = false) {
  if (!session.value || (!force && busy.value)) return;
  try {
    session.value = await api.getAuthoringSession(session.value.id);
    syncWorkflowSelection();
    await refreshGeneration();
    syncSelections();
  } catch (cause) {
    error.value = cause instanceof Error ? cause.message : "读取作者会话失败";
  } finally {
    schedulePoll();
  }
}

function syncSelections() {
  const currentTabs = new Set(tabs.value.map((entry) => entry.id));
  if (!currentTabs.has(activeTab.value)) activeTab.value = "overview";
  if (!activeAsset.value && assets.value[0]) activeAsset.value = assets.value[0].path;
  if (!activeDiff.value && diff.value[0]) activeDiff.value = diff.value[0].path;
}

async function selectWorkflow() {
  generation.value = undefined;
  activeAsset.value = "";
  activeDiff.value = "";
  await refreshGeneration();
  syncSelections();
}

async function createOrResume() {
  busy.value = true;
  error.value = "";
  try {
    if (props.initialSessionId) {
      session.value = await api.getAuthoringSession(props.initialSessionId);
    } else {
      try {
        session.value = await api.getCurrentAuthoringSession();
      } catch (cause) {
        if (!(cause instanceof APIError) || cause.status !== 404) throw cause;
        session.value = await api.createAuthoringSession();
      }
    }
    syncWorkflowSelection();
    await refreshGeneration();
    syncSelections();
  } catch (cause) {
    error.value = cause instanceof Error ? cause.message : "读取作者会话失败";
  } finally {
    busy.value = false;
    schedulePoll();
  }
}

async function send() {
  if (!session.value || !canSend.value) return;
  const sessionID = session.value.id;
  const content = message.value.trim();
  const createdAt = new Date().toISOString();
  const userMessage: AuthoringMessage = {
    id: `pending-author-${Date.now()}`,
    role: "user",
    content,
    created_at: createdAt,
  };
  const agentMessage: AuthoringMessage = {
    id: `pending-agent-${Date.now()}`,
    role: "agent",
    content: "",
    created_at: createdAt,
  };
  pendingMessages.value = [userMessage, agentMessage];
  message.value = "";
  busy.value = true;
  error.value = "";
  try {
    const controller = new AbortController();
    streamController = controller;
    const idempotencyKey = crypto.randomUUID();
    await streamAuthoringMessage(
      sessionID,
      content,
      idempotencyKey,
      {
        onEvent(event) {
          if (event.type === "delta" && event.content) {
            agentMessage.content += event.content;
            pendingMessages.value = [...pendingMessages.value];
          }
        },
        onComplete() {},
        onError(value) {
          error.value = value;
        },
      },
      controller.signal,
    );
  } catch (cause) {
    if (cause instanceof DOMException && cause.name === "AbortError") return;
    error.value = cause instanceof Error ? cause.message : "发送消息失败";
  } finally {
    streamController = undefined;
    busy.value = false;
    pendingMessages.value = [];
    await refresh(true);
    schedulePoll();
  }
}

function focusChange() {
  activeTab.value = candidate.value ? "diff" : "overview";
}

function messageLabel(role: string) {
  if (role === "user") return "你";
  if (role === "agent") return "Breakfix agent";
  if (role === "system") return "真实验证";
  return "流程事件";
}

function messageContent(message: AuthoringMessage) {
  const event = message.event;
  if (message.role !== "event" || !event) return message.content;
  const reason = {
    deadline_exceeded: "本轮已达到时间预算并结束。",
    server_stopping: "Server 停止，本轮已安全结束。",
    server_restarted: "Server 已重启，之前的回合已结束。",
    permanent_executor_error: "本轮因执行器故障结束。",
  }[event.reason];
  const recovery = {
    workspace: "工作区已保留，可以在下一条消息中继续。",
    snapshot: "工作区快照已保留，可以在下一条消息中恢复。",
    candidate: "可以从最近一次 candidate 继续。",
    empty: "没有未提交的工作区内容，可以继续讨论。",
  }[event.recovery];
  return `${reason} ${recovery}`;
}

watch(
  () => `${session.value?.state ?? ""}:${session.value?.authoring_turn_active ?? false}:${workflows.value.map((workflow) => `${workflow.id}:${workflow.state}:${workflow.updated_at}`).join(",")}`,
  () => schedulePoll(),
);

onMounted(() => void createOrResume());
onScopeDispose(() => {
  clearPoll();
  streamController?.abort();
});
</script>

<template>
  <section class="authoring-workspace" aria-label="Scenario authoring workspace">
    <nav class="authoring-narrow-tabs" aria-label="作者工作区视图">
      <button :class="{ active: narrowPane === 'plan' }" @click="narrowPane = 'plan'">内容</button>
      <button :class="{ active: narrowPane === 'chat' }" @click="narrowPane = 'chat'">对话</button>
    </nav>

    <div class="authoring-body">
      <section class="authoring-plan-pane" :class="{ 'narrow-hidden': narrowPane !== 'plan' }">
        <header class="authoring-plan-heading">
          <div>
            <p class="eyebrow">{{ candidate ? "Candidate review" : "Intent revision" }}</p>
            <h1>{{ candidate ? "题目审核" : "题意约定" }}</h1>
          </div>
          <div class="authoring-plan-actions">
            <div v-if="session" class="authoring-status" :data-state="displayState">
              <i></i><span>{{ displayStateLabel }}</span>
              <span v-if="activeWorkflow">Plan r{{ activeWorkflow.plan_revision }}</span>
              <span v-else>Plan r{{ session.intent_revision }}</span>
              <span v-if="session.updated_at">{{ new Date(session.updated_at).toLocaleTimeString() }}</span>
            </div>
            <label v-if="workflows.length" class="authoring-workflow-picker">
              <span>任务</span>
              <select v-model="selectedWorkflowID" aria-label="生成任务" @change="selectWorkflow">
                <option v-for="workflow in workflows" :key="workflow.id" :value="workflow.id">
                  r{{ workflow.plan_revision }} · {{ workflowStateLabel[workflow.state] || workflow.state }}
                </option>
              </select>
            </label>
            <div v-if="session" class="authoring-meta">
              <span>{{ displayMetadata?.runtime || "runtime 待定" }}</span>
            </div>
          </div>
        </header>
        <nav class="authoring-tabs" aria-label="Authoring review tabs">
          <button v-for="tab in tabs" :key="tab.id" :class="{ active: activeTab === tab.id }" @click="activeTab = tab.id">
            {{ tab.label }}
          </button>
        </nav>
        <div class="authoring-content">
          <div v-if="!session" class="authoring-empty"><strong>正在创建作者会话</strong></div>
          <MarkdownDocument v-else-if="activeTab === 'overview'" :source="overview" />
          <MarkdownDocument v-else-if="activeCheckpoint" :source="`# ${activeCheckpoint.title}\n\n${activeCheckpoint.markdown}`" />
          <div v-else-if="activeTab === 'assets'" class="authoring-code-view">
            <select v-if="assets.length" v-model="activeAsset" aria-label="候选文件">
              <option v-for="asset in assets" :key="asset.path" :value="asset.path">{{ asset.path }}</option>
            </select>
            <div v-else class="authoring-empty"><strong>候选文件仍在准备</strong></div>
            <pre v-if="selectedAsset"><code>{{ selectedAsset.content }}</code></pre>
          </div>
          <div v-else-if="activeTab === 'diff'" class="authoring-code-view">
            <select v-if="diff.length" v-model="activeDiff" aria-label="候选差异文件">
              <option v-for="entry in diff" :key="entry.path" :value="entry.path">{{ entry.path }}</option>
            </select>
            <div v-else class="authoring-empty"><strong>当前 candidate 没有可显示的文件差异</strong></div>
            <pre v-if="selectedDiff"><code>{{ selectedDiff.diff }}</code></pre>
          </div>
          <div v-else-if="activeTab === 'verification'" class="authoring-verification">
            <strong>{{ verification?.passed ? "真实验证已通过" : "真实验证未通过" }}</strong>
            <p>{{ verification?.summary || activeWorkflow?.last_error || "验证没有返回摘要" }}</p>
            <dl>
              <div><dt>生成工作流</dt><dd>{{ activeWorkflow?.state || "-" }}</dd></div>
              <div><dt>标准解答</dt><dd>{{ verification?.answers.filter((entry) => entry.exit_code === 0).length || 0 }} / {{ verification?.answers.length || 0 }}</dd></div>
              <div><dt>检查点</dt><dd>{{ verification?.checkpoints.filter((entry) => entry.passed).length || 0 }} / {{ verification?.checkpoints.length || 0 }}</dd></div>
            </dl>
          </div>
        </div>
      </section>

      <section class="authoring-chat-pane" :class="{ 'narrow-hidden': narrowPane !== 'chat' }">
        <header class="authoring-chat-heading"><strong><MessageSquareText :size="15" /> 题目讨论</strong><span>{{ activeWorkflow ? workflowStateLabel[activeWorkflow.state] : "题意约定" }}</span></header>
        <p v-if="error" class="authoring-alert">{{ error }}</p>
        <div class="authoring-timeline">
          <div v-if="session && !displayMessages.length" class="authoring-empty">
            <FileCode2 :size="22" /><strong>描述你希望学习者解决的真实场景</strong>
          </div>
          <article v-for="entry in displayMessages" :key="entry.id" class="authoring-message" :class="entry.role">
            <span class="authoring-message-label">{{ messageLabel(entry.role) }}</span>
            <div class="authoring-bubble">{{ messageContent(entry) }}</div>
            <div v-for="change in entry.changes" :key="`${entry.id}-${change.revision}-${change.kind}`" class="authoring-change-card">
              <span>revision {{ change.revision }} · {{ change.kind }}</span>
              <button type="button" @click="focusChange">{{ change.summary || "查看题意变更" }}</button>
            </div>
          </article>
        </div>
        <form class="authoring-composer" @submit.prevent="send">
          <textarea v-model="message" :disabled="!canCompose" rows="2" placeholder="继续讨论题意、生成、审核、调整或取消…"></textarea>
          <button type="submit" title="发送消息" aria-label="发送消息" :disabled="!canSend"><Send :size="16" /></button>
          <p class="authoring-composer-note">{{ busy || session?.authoring_turn_active ? "agent 正在处理这条消息…" : session?.state === "Published" ? "该作者会话已经发布。" : "通过对话继续当前题目。" }}</p>
        </form>
      </section>
    </div>
  </section>
</template>
