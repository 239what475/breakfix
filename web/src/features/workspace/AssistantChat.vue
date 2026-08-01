<script setup lang="ts">
import { computed, nextTick, onUnmounted, ref, watch } from "vue";
import { ArrowUp } from "lucide-vue-next";
import MarkdownIt from "markdown-it";
import {
  api,
  streamAssistantMessage,
  type AssistantStreamHandlers,
} from "../../api/client";
import type { AssistantMessage, AssistantTerminalContext } from "../../api/types";

const props = defineProps<{
  challengeId: string;
  currentNode: string;
  currentWindow: string;
  terminals: AssistantTerminalContext[];
}>();
const loading = ref(false);
const sending = ref(false);
const error = ref("");
const status = ref("");
const draft = ref("");
const messages = ref<AssistantMessage[]>([]);
const activeRunID = ref("");
const timeline = ref<HTMLElement>();
let streamController: AbortController | undefined;
let animationFrame: number | undefined;
let pendingDelta = "";

const markdown = new MarkdownIt({
  html: false,
  breaks: true,
  linkify: true,
});

markdown.renderer.rules.link_open = (tokens, index, options, _env, self) => {
  const token = tokens[index];
  token.attrSet("target", "_blank");
  token.attrSet("rel", "noopener noreferrer");
  return self.renderToken(tokens, index, options);
};

const canSend = computed(() => draft.value.trim().length > 0 && !sending.value);

function toolStatus(tool?: string) {
  switch (tool) {
    case "get_terminal_scrollback":
      return "正在查看终端...";
    case "get_checkpoint_status":
      return "正在查看检查点...";
    case "list_environment_files":
      return "正在查看环境文件...";
    case "read_environment_file":
      return "正在读取环境文件...";
    case "get_solution":
      return "正在核对解答...";
    default:
      return "正在分析...";
  }
}

function renderAssistantMarkdown(content: string) {
  return markdown.render(content);
}

async function scrollToLatest() {
  await nextTick();
  if (timeline.value) timeline.value.scrollTop = timeline.value.scrollHeight;
}

function isAbortError(err: unknown) {
  return err instanceof DOMException && err.name === "AbortError";
}

function partialID(runID: string) {
	return `assistant-run-${runID}`;
}

function stopSubscription() {
  streamController?.abort();
  streamController = undefined;
  if (animationFrame !== undefined) cancelAnimationFrame(animationFrame);
  animationFrame = undefined;
  pendingDelta = "";
}

function partialMessage(runID: string) {
	const id = partialID(runID);
  let message = messages.value.find((item) => item.id === id);
  if (!message) {
    message = {
      id,
      role: "assistant",
      content: "",
      created_at: new Date().toISOString(),
    };
    messages.value = [...messages.value, message];
  }
  return message;
}

function flushDelta() {
  animationFrame = undefined;
	const runID = activeRunID.value;
	if (!runID || !pendingDelta) return;
	const message = partialMessage(runID);
	message.content += pendingDelta;
  pendingDelta = "";
  void scrollToLatest();
}

function queueDelta(content: string) {
  if (!content) return;
  pendingDelta += content;
  if (animationFrame === undefined) animationFrame = requestAnimationFrame(flushDelta);
}

function finishTurn(complete: { run_id: string; message: AssistantMessage }) {
	flushDelta();
	const id = partialID(complete.run_id);
  let replaced = false;
  messages.value = messages.value.map((message) =>
    message.id === id ? ((replaced = true), complete.message) : message,
  );
  if (!replaced) messages.value = [...messages.value, complete.message];
	activeRunID.value = "";
  sending.value = false;
  status.value = "";
  void scrollToLatest();
}

function failTurn(message: string) {
	flushDelta();
	const runID = activeRunID.value;
	if (runID) messages.value = messages.value.filter((item) => item.id !== partialID(runID));
	activeRunID.value = "";
  sending.value = false;
  status.value = "";
  error.value = message;
}

function streamHandlers(): AssistantStreamHandlers {
	return {
		onEvent(event) {
			if (event.type === "ready") {
				activeRunID.value = event.run_id;
				partialMessage(event.run_id);
				sending.value = true;
        error.value = "";
        status.value = "正在分析...";
      }
      if (event.type === "tool") status.value = toolStatus(event.tool);
			if (event.type === "reset" && activeRunID.value) {
				pendingDelta = "";
				partialMessage(activeRunID.value).content = "";
        status.value = "正在重新分析...";
      }
      if (event.type === "delta") queueDelta(event.content ?? "");
    },
    onComplete(complete) {
      finishTurn(complete);
    },
    onError(message) {
      failTurn(message);
    },
  };
}

async function loadConversation() {
  stopSubscription();
  loading.value = true;
  error.value = "";
  try {
		const conversation = await api.getChallengeAssistant(props.challengeId);
		messages.value = conversation.messages;
		await scrollToLatest();
  } catch (err) {
    error.value = err instanceof Error ? err.message : "Unable to load assistant";
  } finally {
    loading.value = false;
  }
}

async function send() {
  const content = draft.value.trim();
  if (!content || sending.value) return;
  const userMessage: AssistantMessage = {
    id: `pending-user-${Date.now()}`,
    role: "user",
    content,
    created_at: new Date().toISOString(),
  };
  draft.value = "";
  messages.value = [...messages.value, userMessage];
  sending.value = true;
  error.value = "";
  status.value = "正在分析...";
  await scrollToLatest();
  try {
    const controller = new AbortController();
    streamController = controller;
		await streamAssistantMessage(
      props.challengeId,
      {
        content,
        current_node: props.currentNode || undefined,
        current_window: props.currentWindow,
        terminals: props.terminals,
      },
      streamHandlers(),
			controller.signal,
		);
	} catch (err) {
		if (isAbortError(err)) return;
		error.value = err instanceof Error ? err.message : "Assistant request failed";
	} finally {
		if (!activeRunID.value) {
      sending.value = false;
      status.value = "";
    }
  }
}

function onKeydown(event: KeyboardEvent) {
  if (event.key === "Enter" && !event.shiftKey) {
    event.preventDefault();
    void send();
  }
}

watch(
  () => props.challengeId,
  async () => {
    stopSubscription();
    loading.value = false;
    sending.value = false;
    error.value = "";
    status.value = "";
    draft.value = "";
    messages.value = [];
		activeRunID.value = "";
    await loadConversation();
  },
  { immediate: true },
);

onUnmounted(stopSubscription);
</script>

<template>
  <section class="assistant-chat" aria-label="Challenge assistant">
    <div class="assistant-notice">
      <p v-if="error" class="assistant-error">{{ error }}</p>
    </div>
    <div ref="timeline" class="assistant-timeline" aria-live="polite">
      <p v-if="loading" class="assistant-empty">Loading...</p>
      <p v-else-if="!messages.length" class="assistant-empty"> </p>
      <article
        v-for="message in messages"
        :key="message.id"
        class="assistant-message"
        :class="message.role"
      >
        <div
          v-if="message.role === 'assistant'"
          class="assistant-bubble assistant-markdown"
          v-html="renderAssistantMarkdown(message.content)"
        ></div>
        <div v-else class="assistant-bubble">{{ message.content }}</div>
        <div v-if="message.evidence?.length" class="assistant-evidence">
          <span v-for="item in message.evidence" :key="`${item.kind}-${item.label}`">{{ item.label }}</span>
        </div>
      </article>
    </div>
    <div class="assistant-status-slot">
      <p v-if="status" class="assistant-status">{{ status }}</p>
    </div>
    <form class="assistant-composer" @submit.prevent="send">
      <textarea
        v-model="draft"
        aria-label="Assistant message"
        rows="2"
        :disabled="sending"
        @keydown="onKeydown"
      ></textarea>
      <button
        class="icon-button assistant-send"
        type="submit"
        title="Send message"
        aria-label="Send message"
        :disabled="!canSend"
      >
        <ArrowUp :size="17" aria-hidden="true" />
      </button>
    </form>
  </section>
</template>
