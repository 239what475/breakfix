<script setup lang="ts">
import { computed, onMounted, onScopeDispose, ref, watch } from "vue";
import { FileCode2, MessageSquareText, Send } from "lucide-vue-next";
import { APIError, api, streamAuthoringMessage } from "../../api/client";
import type {
  AuthoringAsset,
  AuthoringFileDiff,
	AuthoringMessage,
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
const pendingMessages = ref<AuthoringMessage[]>([]);
let pollTimer: number | undefined;
let streamController: AbortController | undefined;
let displayedClassificationReview = "";

const sessionStateLabel: Record<string, string> = {
  DraftConversation: "等待题意",
  IntentReview: "题意约定待确认",
  Published: "已发布",
};
const workflowStateLabel: Record<string, string> = {
  Generating: "正在生成",
  Judging: "正在审查",
  Building: "正在构建",
  ArtifactPublishing: "正在发布候选产物",
  Verifying: "正在真实验证",
  NeedsAuthorReview: "等待作者审核",
  Classifying: "正在分类",
  NeedsClassificationReview: "等待分类审核",
  ChallengePublishing: "正在发布挑战",
  Published: "已发布",
  Failed: "基础设施失败",
  Cancelled: "已取消",
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
  if (session.value?.candidate) {
    entries.push({ id: "assets", label: "Assets" });
    entries.push({ id: "diff", label: "Diff" });
  }
  if (session.value?.verification) {
    entries.push({ id: "verification", label: "验证" });
  }
  if (session.value?.classification) {
    entries.push({ id: "topic", label: "Topic" });
    entries.push({ id: "tags", label: "Tags" });
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
const classification = computed(() => session.value?.classification);
const classificationInReview = computed(
  () =>
    session.value?.workflow?.state === "NeedsClassificationReview" &&
    !!classification.value,
);
const classificationProposed = computed(
  () => classificationInReview.value && classification.value?.result === "proposed",
);
const classificationUnclassifiable = computed(
  () =>
    classificationInReview.value &&
    classification.value?.result === "unclassifiable",
);
const workflowState = computed(() => session.value?.workflow?.state);
const displayState = computed(
  () => workflowState.value ?? session.value?.state ?? "",
);
const displayStateLabel = computed(
  () =>
    workflowState.value
      ? workflowStateLabel[workflowState.value]
      : sessionStateLabel[session.value?.state ?? ""],
);
const canCompose = computed(
  () => {
    const current = session.value;
    if (!current || busy.value || current.authoring_turn_active || current.state === "Published") {
      return false;
    }
    if (classificationProposed.value || classificationUnclassifiable.value) {
      return true;
    }
    return !current.workflow || ["NeedsAuthorReview", "Failed", "Cancelled"].includes(current.workflow.state);
  },
);
const canSend = computed(
  () => canCompose.value && message.value.trim().length > 0,
);
const canGenerate = computed(
  () =>
    !session.value?.authoring_turn_active &&
    session.value?.state === "IntentReview" &&
    (!session.value.workflow ||
      ["Failed", "Cancelled"].includes(session.value.workflow.state) ||
      (session.value.workflow.state === "NeedsClassificationReview" &&
        session.value.intent_revision !== session.value.visible_revision)) &&
    checkpoints.value.length > 0,
);
const canConfirmContent = computed(
  () =>
    !session.value?.authoring_turn_active &&
    session.value?.workflow?.state === "NeedsAuthorReview" &&
    session.value.intent_revision === session.value.visible_revision &&
    !!session.value.candidate &&
    session.value.verification?.passed === true,
);
const canOpenPublished = computed(
  () =>
    session.value?.state === "Published" &&
    !!session.value.publish_challenge_id,
);
const canPublishClassification = computed(
  () =>
    classificationProposed.value &&
    !session.value?.authoring_turn_active &&
    session.value?.intent_revision === session.value?.visible_revision &&
    !!session.value?.workflow &&
    !!session.value?.candidate &&
    session.value?.verification?.passed === true,
);
const canCancelGeneration = computed(() => {
  const workflow = session.value?.workflow;
  return (
    !!workflow &&
    !busy.value &&
    !session.value?.authoring_turn_active &&
    !["Published", "Failed", "Cancelled"].includes(workflow.state)
  );
});
const actionLabel = computed(() => {
  if (canGenerate.value) return "生成并验证题目";
  if (canConfirmContent.value) return "确认题目内容";
  if (canPublishClassification.value) return "确认分类并发布";
  if (canOpenPublished.value) return "查看已发布题目";
  return "";
});
const topicMarkdown = computed(() => {
  const proposal = classification.value;
  if (!proposal) return "";
  if (proposal.result === "unclassifiable") {
    return `# Topic\n\n当前题目无法可靠归入现有课程 Topic。\n\n## 原因\n\n${proposal.unclassifiable_reason || "分类 Agent 没有返回原因。"}\n\n## 建议\n\n${proposal.adjustment_suggestion || "请调整题目内容后重新生成。"}`;
  }
  const topic = proposal.topic;
  if (!topic) return "# Topic\n\n分类提案尚未提供 Topic。";
  if (topic.existing) {
    const value = topic.existing;
    return `# 已有 Topic\n\n## ${value.title}\n\n${value.definition}\n\n- **领域**：${value.domain.title}\n- **范围**：${value.scope}\n- **非范围**：${value.non_goals}\n\n## 题目归属指引\n\n${value.challenge_guidance}\n\n## 本题归属理由\n\n${topic.reason}`;
  }
  if (topic.new) {
    const value = topic.new;
    return `# 新建 Topic\n\n## ${value.title}\n\n${value.definition}\n\n- **领域**：${value.domain.title}\n- **范围**：${value.scope}\n- **非范围**：${value.non_goals}\n\n## 题目归属指引\n\n${value.challenge_guidance}\n\n## 本题归属理由\n\n${topic.reason}`;
  }
  return "# Topic\n\n分类提案不完整。";
});
const tagsMarkdown = computed(() => {
  const proposal = classification.value;
  if (!proposal) return "";
  if (proposal.result === "unclassifiable") {
    return "# Tags\n\n当前题目尚不能建立可靠分类，因此没有 Tag 提案。";
  }
  if (!proposal.tags.length) {
    return "# Tags\n\n当前题目不需要跨 Topic 的横向筛选 Tag。";
  }
  const sections = proposal.tags.map((tag, index) => {
    if (tag.existing) {
      return `## ${index + 1}. 已有 Tag：${tag.existing.title}\n\n${tag.existing.description}\n\n**本题使用理由**：${tag.reason}`;
    }
    if (tag.new) {
      return `## ${index + 1}. 新建 Tag：${tag.new.title}\n\n${tag.new.description}\n\n**本题使用理由**：${tag.reason}`;
    }
    return `## ${index + 1}. 不完整 Tag 提案`;
  });
  return `# Tags\n\n${sections.join("\n\n")}`;
});
const chatTitle = computed(() => {
  if (classificationProposed.value) return "分类调整";
  if (classificationUnclassifiable.value) return "内容调整";
  return "题意讨论";
});
const chatDescription = computed(() => {
  if (classificationProposed.value) return "agent 只调整当前私有分类提案";
  if (classificationUnclassifiable.value) return "需要调整题目内容，不能直接发布";
  return "agent 仅通过受控函数修改题意约定";
});
const composerPlaceholder = computed(() => {
  if (classificationProposed.value) return "说明希望如何调整 Topic 或 Tags…";
  if (classificationUnclassifiable.value) return "说明如何调整题目内容以便可靠分类…";
  return "描述题目想法，或说明希望 agent 如何修改题意约定…";
});
const composerNote = computed(() => {
  if (busy.value || session.value?.authoring_turn_active) return "agent 正在处理这条消息…";
  if (classificationProposed.value) return "反馈会由分类 agent 判断并只修改私有分类提案。";
  if (classificationUnclassifiable.value) return "此结果只能通过调整题目内容解决。";
  if (canSend.value) return "通过自然语言指导 agent；左侧内容保持只读。";
  return "生成、验证或发布期间不能修改题意约定。";
});
const displayMessages = computed(() => [
	...(session.value?.messages ?? []),
	...pendingMessages.value,
]);

function clearPoll() {
  if (pollTimer !== undefined) window.clearTimeout(pollTimer);
  pollTimer = undefined;
}
function shouldPoll() {
  const currentWorkflow = session.value?.workflow;
  return (
    session.value?.authoring_turn_active ||
    (!!currentWorkflow &&
      !["NeedsAuthorReview", "NeedsClassificationReview", "Published", "Failed", "Cancelled"].includes(
        currentWorkflow.state,
      ))
  );
}
function schedulePoll() {
  clearPoll();
  if (!shouldPoll() || !session.value) return;
  pollTimer = window.setTimeout(() => void refresh(), 3000);
}
async function refresh(force = false) {
	if (!session.value || (!force && busy.value)) return;
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
  const currentClassification = session.value?.classification;
  const currentWorkflow = session.value?.workflow;
  const classificationReview =
    currentWorkflow?.state === "NeedsClassificationReview" && currentClassification
      ? `${currentWorkflow.id}:${currentClassification.revision}`
      : "";
  if (classificationReview && classificationReview !== displayedClassificationReview) {
    activeTab.value = "topic";
  }
  displayedClassificationReview = classificationReview;
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
        if (!(err instanceof APIError) || err.status !== 404) {
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
  const current = session.value;
  const sessionID = current.id;
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
  const workflow = current.workflow;
  const candidate = current.candidate;
  const proposal = current.classification;
  const adjustsClassification =
    classificationProposed.value &&
    !!workflow &&
    !!candidate &&
    !!proposal;
  pendingMessages.value = adjustsClassification ? [userMessage] : [userMessage, agentMessage];
  message.value = "";
  busy.value = true;
  error.value = "";
  try {
    if (adjustsClassification && workflow && candidate && proposal) {
      session.value = await api.requestAuthoringClassificationAdjustment(sessionID, {
        workflow_id: workflow.id,
        candidate_revision_id: candidate.id,
        proposal_revision: proposal.revision,
        feedback: content,
        idempotency_key: authoringIdempotencyKey(),
      });
      syncSelections();
      return;
    }
    const controller = new AbortController();
    streamController = controller;
    await streamAuthoringMessage(
      sessionID,
      content,
      {
        onEvent(event) {
          if (event.type === "delta" && event.content) {
            agentMessage.content += event.content;
            pendingMessages.value = [...pendingMessages.value];
          }
        },
        onComplete() {},
        onError(message) {
          error.value = message;
        },
      },
      controller.signal,
    );
  } catch (err) {
		if (err instanceof DOMException && err.name === "AbortError") return;
    error.value = err instanceof Error ? err.message : "发送消息失败";
  } finally {
    streamController = undefined;
    busy.value = false;
    pendingMessages.value = [];
    await refresh(true);
    schedulePoll();
  }
}
async function confirmAction() {
  if (!session.value || busy.value) return;
  busy.value = true;
  error.value = "";
  try {
    if (canOpenPublished.value && session.value.publish_challenge_id) {
      emit("published", session.value.publish_challenge_id);
      return;
    }
    if (canGenerate.value) {
      session.value = await api.confirmAuthoringGeneration(session.value.id, {
        plan_revision: session.value.intent_revision,
        idempotency_key: authoringIdempotencyKey(),
      });
    } else if (canConfirmContent.value && session.value.workflow && session.value.candidate) {
      session.value = await api.confirmAuthoringContent(session.value.id, {
        workflow_id: session.value.workflow.id,
        candidate_revision_id: session.value.candidate.id,
        idempotency_key: authoringIdempotencyKey(),
      });
		} else if (canPublishClassification.value && session.value.workflow && session.value.candidate && session.value.classification) {
			session.value = await api.publishAuthoringRevision(session.value.id, {
				workflow_id: session.value.workflow.id,
				candidate_revision_id: session.value.candidate.id,
				proposal_revision: session.value.classification.revision,
				idempotency_key: authoringIdempotencyKey(),
			});
    }
    syncSelections();
  } catch (err) {
    error.value = err instanceof Error ? err.message : "确认操作失败";
  } finally {
    busy.value = false;
    schedulePoll();
  }
}
async function cancelGeneration() {
  const current = session.value;
  const workflow = current?.workflow;
  if (!current || !workflow || !canCancelGeneration.value) return;
  busy.value = true;
  error.value = "";
  try {
    session.value = await api.cancelAuthoringGeneration(current.id, workflow.id);
    syncSelections();
  } catch (err) {
    error.value = err instanceof Error ? err.message : "取消生成失败";
  } finally {
    busy.value = false;
    schedulePoll();
  }
}
function authoringIdempotencyKey() {
	return globalThis.crypto?.randomUUID?.() ?? `authoring-${Date.now()}-${Math.random().toString(36).slice(2)}`;
}
function focusChange(revision: number) {
  activeTab.value =
    revision === session.value?.visible_revision && session.value?.candidate
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
  () => `${session.value?.state ?? ""}:${session.value?.workflow?.state ?? ""}:${session.value?.authoring_turn_active ?? false}`,
  () => schedulePoll(),
);
onMounted(() => void createOrResume());
onScopeDispose(() => {
	clearPoll();
	streamController?.abort();
});
</script>

<template>
  <section class="authoring-workspace" aria-label="Challenge authoring workspace">
    <nav class="authoring-narrow-tabs" aria-label="作者工作区视图">
      <button :class="{ active: narrowPane === 'plan' }" @click="narrowPane = 'plan'">内容</button>
      <button :class="{ active: narrowPane === 'chat' }" @click="narrowPane = 'chat'">对话</button>
      <button v-if="actionLabel" class="authoring-narrow-action" :disabled="busy" @click="confirmAction">{{ actionLabel }}</button>
      <button v-if="canCancelGeneration" class="authoring-narrow-cancel" :disabled="busy" @click="cancelGeneration">取消</button>
    </nav>

    <div class="authoring-body">
      <section class="authoring-plan-pane" :class="{ 'narrow-hidden': narrowPane !== 'plan' }">
        <header class="authoring-plan-heading">
          <div><p class="eyebrow">{{ session?.classification ? "Classification proposal" : session?.candidate ? "Verified revision" : "Intent revision" }}</p><h1>{{ session?.classification ? "分类审核" : session?.candidate ? "已验证题目" : "题意约定" }}</h1></div>
          <div class="authoring-plan-actions">
            <div v-if="session" class="authoring-status" :data-state="displayState"><i></i><span>{{ displayStateLabel }}</span><span>{{ session.candidate ? "已验证" : "题意" }} r{{ session.visible_revision }}</span><span v-if="session.workflow">{{ session.workflow.state }}</span><span v-if="session.intent_revision !== session.visible_revision">题意 r{{ session.intent_revision }}</span><span v-if="session.updated_at">{{ new Date(session.updated_at).toLocaleTimeString() }}</span></div>
            <div v-if="session" class="authoring-meta"><span>{{ displayMetadata?.runtime || "runtime 待定" }}</span><span>{{ displayMetadata?.difficulty || "difficulty 待定" }}</span></div>
            <button v-if="canCancelGeneration" class="secondary-button authoring-cancel-action" :disabled="busy" @click="cancelGeneration">取消生成</button>
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
          <MarkdownDocument v-else-if="activeTab === 'topic'" :source="topicMarkdown" />
          <MarkdownDocument v-else-if="activeTab === 'tags'" :source="tagsMarkdown" />
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
            <strong>{{ session.verification?.passed ? "真实验证已通过" : "真实验证未通过" }}</strong>
            <p>{{ session.verification?.summary || session.last_error || "验证没有返回摘要" }}</p>
            <dl>
              <div><dt>生成工作流</dt><dd>{{ session.workflow?.state || "-" }}</dd></div>
              <div><dt>标准解答</dt><dd>{{ session.verification?.answers.filter((entry) => entry.exit_code === 0).length || 0 }} / {{ session.verification?.answers.length || 0 }}</dd></div>
              <div><dt>检查点</dt><dd>{{ session.verification?.checkpoints.filter((entry) => entry.passed).length || 0 }} / {{ session.verification?.checkpoints.length || 0 }}</dd></div>
            </dl>
          </div>
        </div>
      </section>

      <section class="authoring-chat-pane" :class="{ 'narrow-hidden': narrowPane !== 'chat' }">
        <header class="authoring-chat-heading"><strong><MessageSquareText :size="15" /> {{ chatTitle }}</strong><span>{{ chatDescription }}</span></header>
        <p v-if="error" class="authoring-alert">{{ error }}</p>
        <div class="authoring-timeline">
          <div v-if="session && !displayMessages.length" class="authoring-empty">
            <FileCode2 :size="22" /><strong>{{ classificationProposed ? "说明希望如何调整当前分类" : classificationUnclassifiable ? "说明如何调整题目内容" : "描述你希望学习者解决的真实场景" }}</strong>
          </div>
          <article v-for="entry in displayMessages" :key="entry.id" class="authoring-message" :class="entry.role">
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
          <textarea v-model="message" :disabled="!canCompose" rows="2" :placeholder="composerPlaceholder"></textarea>
          <button type="submit" title="发送消息" aria-label="发送消息" :disabled="!canSend"><Send :size="16" /></button>
          <p class="authoring-composer-note">{{ composerNote }}</p>
        </form>
      </section>
    </div>
  </section>
</template>
