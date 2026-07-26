<script setup lang="ts">
import { computed, onMounted, onScopeDispose, ref, watch } from "vue";
import { FileCode2, MessageSquareText, Send } from "lucide-vue-next";
import { api } from "../../api/client";
import type {
  AuthoringAsset,
  AuthoringFileDiff,
  AuthoringSession,
} from "../../api/types";
import MarkdownDocument from "../workspace/MarkdownDocument.vue";
import "./authoring.css";

const props = defineProps<{ initialSessionId?: string }>();
const emit = defineEmits<{ published: [challengeId: string] }>();
const session = ref<AuthoringSession>();
const message = ref("");
const busy = ref(false);
const error = ref("");
const activeTab = ref("overview");
const activeAsset = ref("");
const activeDiff = ref("");
const narrowPane = ref<"plan" | "chat">("plan");
let pollTimer: number | undefined;

const stateLabel: Record<string, string> = {
  DraftConversation: "等待题意",
  IntentReview: "题意约定待确认",
  GeneratingAndVerifying: "正在生成并验证",
  VerificationInfrastructureFailed: "验证基础设施故障",
  AwaitingVerifiedReview: "等待已验证题目审核",
  RevisingAndVerifying: "正在生成并验证修订题目",
  Publishing: "正在发布",
  Published: "已发布",
};

const usingVerifiedRevision = computed(() => !!session.value?.verified);
const displayMetadata = computed(
  () => session.value?.verified?.metadata ?? session.value?.intent.metadata,
);
const checkpoints = computed(() => {
  if (session.value?.verified) {
    return session.value.verified.checkpoints.map((checkpoint, index) => ({
      id: checkpoint.id,
      title: checkpoint.title,
      markdown: `${checkpoint.description}${checkpoint.hint ? `\n\n提示文件：\`${checkpoint.hint}\`` : ""}${checkpoint.depends_on?.length ? `\n\n依赖：${checkpoint.depends_on.join("、")}` : ""}`,
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
  if (session.value?.artifact) {
    entries.push({ id: "assets", label: "Assets" });
    entries.push({ id: "diff", label: "Diff" });
  }
  if (session.value?.verification) {
    entries.push({ id: "verification", label: "验证" });
  }
  return entries;
});
const overview = computed(() => {
  const metadata = displayMetadata.value;
  if (!metadata) return "";
  const problem = session.value?.assets.find(
    (asset) => asset.path === "problem.md",
  )?.content;
  const body = usingVerifiedRevision.value
    ? problem || "已验证题目未包含可展示的 problem.md。"
    : session.value?.intent.overview || "等待 agent 写入题意约定。";
  return `# ${metadata.title || "未命名题目"}\n\n${metadata.description || "等待 agent 根据题意补全简介。"}\n\n- **运行时**：${metadata.runtime || "待定"}\n- **难度**：${metadata.difficulty || "待定"}\n\n${body}`;
});
const activeCheckpoint = computed(() => {
  const id = activeTab.value.replace("checkpoint:", "");
  return checkpoints.value.find((checkpoint) => checkpoint.id === id);
});
const selectedAsset = computed<AuthoringAsset | undefined>(() => {
  const assets = session.value?.assets ?? [];
  return assets.find((asset) => asset.path === activeAsset.value) ?? assets[0];
});
const selectedDiff = computed<AuthoringFileDiff | undefined>(() => {
  const diffs = session.value?.diff ?? [];
  return diffs.find((entry) => entry.path === activeDiff.value) ?? diffs[0];
});
const canCompose = computed(
  () =>
    !!session.value &&
    !busy.value &&
    ["DraftConversation", "IntentReview", "AwaitingVerifiedReview"].includes(
      session.value.state,
    ),
);
const canSend = computed(
  () => canCompose.value && message.value.trim().length > 0,
);
const canGenerate = computed(
  () => session.value?.state === "IntentReview" && checkpoints.value.length > 0,
);
const canPublish = computed(
  () => session.value?.state === "AwaitingVerifiedReview" && !!session.value.artifact,
);
const canOpenPublished = computed(
  () =>
    session.value?.state === "Published" &&
    !!session.value.verification?.challenge_id,
);
const actionLabel = computed(() => {
  if (canGenerate.value) return "生成并验证题目";
  if (canPublish.value) return "发布挑战";
  if (canOpenPublished.value) return "查看已发布题目";
  return "";
});

function clearPoll() {
  if (pollTimer !== undefined) window.clearTimeout(pollTimer);
  pollTimer = undefined;
}
function shouldPoll() {
  return ["GeneratingAndVerifying", "RevisingAndVerifying", "Publishing"].includes(
    session.value?.state ?? "",
  );
}
function schedulePoll() {
  clearPoll();
  if (!shouldPoll() || !session.value) return;
  pollTimer = window.setTimeout(() => void refresh(), 3000);
}
async function refresh() {
  if (!session.value || busy.value) return;
  try {
    session.value = await api.getAuthoringSession(session.value.id);
    syncSelections();
  } catch (err) {
    error.value = err instanceof Error ? err.message : "读取作者会话失败";
  } finally {
    schedulePoll();
  }
}
function syncSelections() {
  const currentTabs = new Set(tabs.value.map((entry) => entry.id));
  if (!currentTabs.has(activeTab.value)) activeTab.value = "overview";
  if (!activeAsset.value && session.value?.assets[0]) {
    activeAsset.value = session.value.assets[0].path;
  }
  if (!activeDiff.value && session.value?.diff[0]) {
    activeDiff.value = session.value.diff[0].path;
  }
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
      } catch (err) {
        if (!(err instanceof Error) || err.message !== "authoring session not found") {
          throw err;
        }
        session.value = await api.createAuthoringSession();
      }
    }
    syncSelections();
  } catch (err) {
    error.value = err instanceof Error ? err.message : "读取作者会话失败";
  } finally {
    busy.value = false;
  }
}
async function send() {
  if (!session.value || !canSend.value) return;
  busy.value = true;
  error.value = "";
  try {
    session.value = await api.sendAuthoringMessage(
      session.value.id,
      message.value.trim(),
    );
    message.value = "";
    syncSelections();
  } catch (err) {
    error.value = err instanceof Error ? err.message : "发送消息失败";
  } finally {
    busy.value = false;
    schedulePoll();
  }
}
async function confirmAction() {
  if (!session.value || busy.value) return;
  busy.value = true;
  error.value = "";
  try {
    if (canOpenPublished.value && session.value.verification?.challenge_id) {
      emit("published", session.value.verification.challenge_id);
      return;
    }
    session.value = canGenerate.value
      ? await api.confirmAuthoringGeneration(session.value.id)
      : await api.publishAuthoringRevision(session.value.id);
    syncSelections();
  } catch (err) {
    error.value = err instanceof Error ? err.message : "确认操作失败";
  } finally {
    busy.value = false;
    schedulePoll();
  }
}
function focusChange(revision: number) {
  activeTab.value =
    revision === session.value?.visible_revision && session.value?.artifact
      ? "diff"
      : "overview";
}
function messageLabel(role: string) {
  if (role === "user") return "你";
  if (role === "agent") return "Breakfix agent";
  if (role === "system") return "真实验证";
  return "流程事件";
}

watch(
  () => session.value?.state,
  () => schedulePoll(),
);
onMounted(() => void createOrResume());
onScopeDispose(clearPoll);
</script>

<template>
  <section class="authoring-workspace" aria-label="Challenge authoring workspace">
    <nav class="authoring-narrow-tabs" aria-label="作者工作区视图">
      <button :class="{ active: narrowPane === 'plan' }" @click="narrowPane = 'plan'">内容</button>
      <button :class="{ active: narrowPane === 'chat' }" @click="narrowPane = 'chat'">对话</button>
      <button v-if="actionLabel" class="authoring-narrow-action" :disabled="busy" @click="confirmAction">{{ actionLabel }}</button>
    </nav>

    <div class="authoring-body">
      <section class="authoring-plan-pane" :class="{ 'narrow-hidden': narrowPane !== 'plan' }">
        <header class="authoring-plan-heading">
          <div><p class="eyebrow">{{ session?.artifact ? "Verified revision" : "Intent revision" }}</p><h1>{{ session?.artifact ? "已验证题目" : "题意约定" }}</h1></div>
          <div class="authoring-plan-actions">
            <div v-if="session" class="authoring-status" :data-state="session.state"><i></i><span>{{ stateLabel[session.state] }}</span><span>{{ session.artifact ? "已验证" : "题意" }} r{{ session.visible_revision }}</span><span v-if="session.intent_revision !== session.visible_revision">题意 r{{ session.intent_revision }}</span><span v-if="session.updated_at">{{ new Date(session.updated_at).toLocaleTimeString() }}</span></div>
            <div v-if="session" class="authoring-meta"><span>{{ displayMetadata?.runtime || "runtime 待定" }}</span><span>{{ displayMetadata?.difficulty || "difficulty 待定" }}</span></div>
            <button v-if="actionLabel" class="primary-button authoring-primary-action" :disabled="busy" @click="confirmAction">{{ actionLabel }}</button>
          </div>
        </header>
        <nav class="authoring-tabs" aria-label="Authoring plan tabs">
          <button v-for="tab in tabs" :key="tab.id" :class="{ active: activeTab === tab.id }" @click="activeTab = tab.id">
            {{ tab.label }}
          </button>
        </nav>
        <div class="authoring-content">
          <div v-if="!session" class="authoring-empty"><strong>正在创建作者会话</strong></div>
          <MarkdownDocument v-else-if="activeTab === 'overview'" :source="overview" />
          <MarkdownDocument v-else-if="activeCheckpoint" :source="`# ${activeCheckpoint.title}\n\n${activeCheckpoint.markdown}`" />
          <div v-else-if="activeTab === 'assets'" class="authoring-code-view">
            <select v-if="session.assets.length" v-model="activeAsset" aria-label="已验证文件">
              <option v-for="asset in session.assets" :key="asset.path" :value="asset.path">{{ asset.path }}</option>
            </select>
            <div v-else class="authoring-empty"><strong>已验证文件仍在准备</strong></div>
            <pre v-if="selectedAsset"><code>{{ selectedAsset.content }}</code></pre>
          </div>
          <div v-else-if="activeTab === 'diff'" class="authoring-code-view">
            <select v-if="session.diff.length" v-model="activeDiff" aria-label="已验证差异文件">
              <option v-for="entry in session.diff" :key="entry.path" :value="entry.path">{{ entry.path }}</option>
            </select>
            <div v-else class="authoring-empty"><strong>当前 revision 没有可显示的文件差异</strong></div>
            <pre v-if="selectedDiff"><code>{{ selectedDiff.diff }}</code></pre>
          </div>
          <div v-else-if="activeTab === 'verification'" class="authoring-verification">
            <strong>{{ session.verification?.phase === "Succeeded" ? "真实验证已通过" : session.verification?.report?.class === "infrastructure" ? "真实验证因基础设施故障未完成" : "真实验证未通过" }}</strong>
            <p>{{ session.verification?.report?.summary || session.verification?.message || "全部检查点通过" }}</p>
            <dl>
              <div><dt>镜像构建</dt><dd>{{ session.verification?.report?.build_passed ? "通过" : "-" }}</dd></div>
              <div><dt>标准解答</dt><dd>{{ session.verification?.report?.answer_passed ? "通过" : "-" }}</dd></div>
              <div><dt>全部检查点</dt><dd>{{ session.verification?.report?.checkpoints_passed ? "通过" : "-" }}</dd></div>
            </dl>
          </div>
        </div>
      </section>

      <section class="authoring-chat-pane" :class="{ 'narrow-hidden': narrowPane !== 'chat' }">
        <header class="authoring-chat-heading"><strong><MessageSquareText :size="15" /> 题意讨论</strong><span>agent 仅通过受控函数修改题意约定</span></header>
        <p v-if="error" class="authoring-alert">{{ error }}</p>
        <div class="authoring-timeline">
          <div v-if="session && !session.messages.length" class="authoring-empty">
            <FileCode2 :size="22" /><strong>描述你希望学习者解决的真实场景</strong>
          </div>
          <article v-for="entry in session?.messages" :key="entry.id" class="authoring-message" :class="entry.role">
            <span class="authoring-message-label">{{ messageLabel(entry.role) }}</span>
            <div class="authoring-bubble">{{ entry.content }}</div>
            <div v-for="change in entry.changes" :key="`${entry.id}-${change.revision}-${change.kind}`" class="authoring-change-card">
              <span>revision {{ change.revision }} · {{ change.kind }}</span>
              <button @click="focusChange(change.revision)">{{ change.summary || '查看题意变更' }}</button>
              <span class="authoring-change-impact">难度影响：{{ change.difficulty_impact }}</span>
            </div>
          </article>
        </div>
        <form class="authoring-composer" @submit.prevent="send">
          <textarea v-model="message" :disabled="!canCompose" rows="2" placeholder="描述题目想法，或说明希望 agent 如何修改题意约定…"></textarea>
          <button type="submit" title="发送消息" aria-label="发送消息" :disabled="!canSend"><Send :size="16" /></button>
          <p class="authoring-composer-note">{{ busy ? 'agent 正在处理这条消息…' : canSend ? '通过自然语言指导 agent；左侧内容保持只读。' : '生成、验证或发布期间不能修改题意约定。' }}</p>
        </form>
      </section>
    </div>
  </section>
</template>
