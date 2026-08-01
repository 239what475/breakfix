<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from "vue";
import { api } from "../../api/client";
import type { AssistantTerminalContext, Challenge, ChallengeContent } from "../../api/types";
import MarkdownDocument from "./MarkdownDocument.vue";
import AssistantChat from "./AssistantChat.vue";
import TerminalPane from "./TerminalPane.vue";
import TaxonomyPanel from "./TaxonomyPanel.vue";
import WorkspaceHeader from "./WorkspaceHeader.vue";
import WorkspaceSidebar from "./WorkspaceSidebar.vue";
import { useChallengeProgress } from "./useChallengeProgress";
import "./workspace.css";
import "./assistant.css";

const props = defineProps<{ challenge: Challenge }>();
const emit = defineEmits<{
  changed: [];
  notice: [message: string, kind?: "error" | "info"];
}>();
const content = ref<ChallengeContent>();
const loadingContent = ref(false);
const contentError = ref("");
const view = ref<"problem" | "solution" | "assistant">("problem");
const sidebarCollapsed = ref(false);
const mobileView = ref<"document" | "terminal">("document");
const isNarrow = ref(false);
const activeHint = ref<string | null>(null);
const terminalConnected = ref(false);
const currentTerminalNode = ref("");
const currentTerminalWindow = ref("shell-1");
const terminalContexts = ref<AssistantTerminalContext[]>([{ windows: ["shell-1"] }]);
const resetting = ref(false);
const sessionStartedAt = ref(Date.now());
const elapsedSeconds = ref(0);
let elapsedTimer: number | undefined;
const challengeId = computed(() => props.challenge.id);
const progressEnabled = computed(
  () => terminalConnected.value && !document.hidden,
);
const {
  checks,
  loading: progressLoading,
  error: progressError,
  refresh,
} = useChallengeProgress(challengeId, progressEnabled);
const completed = computed(
  () => checks.value.filter((check) => check.passed).length,
);
const documentSource = computed(() =>
  view.value === "problem"
    ? (content.value?.problem ?? "")
    : (content.value?.solution ?? ""),
);
const terminalVisible = computed(
  () => !isNarrow.value || mobileView.value === "terminal",
);
const elapsed = computed(() => {
  const minutes = Math.floor(elapsedSeconds.value / 60);
  const seconds = elapsedSeconds.value % 60;
  return `${String(minutes).padStart(2, "0")}:${String(seconds).padStart(2, "0")}`;
});

function resetElapsed() {
  sessionStartedAt.value = Date.now();
  elapsedSeconds.value = 0;
}

async function loadContent() {
  loadingContent.value = true;
  contentError.value = "";
  try {
    content.value = await api.getChallengeContent(props.challenge.id);
  } catch (err) {
    contentError.value =
      err instanceof Error ? err.message : "Unable to load challenge content";
  } finally {
    loadingContent.value = false;
  }
}

function showHint(id: string) {
  activeHint.value = activeHint.value === id ? null : id;
}

function changeView(next: "problem" | "solution" | "assistant") {
  view.value = next;
  if (next === "assistant") activeHint.value = null;
}

async function reset() {
  resetting.value = true;
  try {
    await api.resetChallenge(props.challenge.id);
    checks.value = [];
    emit("notice", "Challenge reset.", "info");
    emit("changed");
  } catch (err) {
    emit(
      "notice",
      err instanceof Error ? err.message : "Reset failed",
      "error",
    );
  } finally {
    resetting.value = false;
  }
}

watch(
  () => props.challenge.id,
  () => {
    content.value = undefined;
    checks.value = [];
    activeHint.value = null;
    currentTerminalNode.value = "";
    currentTerminalWindow.value = "shell-1";
    terminalContexts.value = [{ windows: ["shell-1"] }];
    view.value = "problem";
    resetElapsed();
    void loadContent();
  },
  { immediate: true },
);
const narrowViewport = window.matchMedia("(max-width: 950px)");
function syncNarrowLayout() {
  isNarrow.value = narrowViewport.matches;
  sidebarCollapsed.value = narrowViewport.matches;
}
onMounted(() => {
  syncNarrowLayout();
  narrowViewport.addEventListener("change", syncNarrowLayout);
  elapsedTimer = window.setInterval(() => {
    elapsedSeconds.value = Math.floor(
      (Date.now() - sessionStartedAt.value) / 1000,
    );
  }, 1000);
});
onUnmounted(() => {
  narrowViewport.removeEventListener("change", syncNarrowLayout);
  if (elapsedTimer !== undefined) window.clearInterval(elapsedTimer);
});
</script>

<template>
  <main class="challenge-workspace">
    <WorkspaceHeader
      :challenge="challenge"
      :complete="completed"
      :total="content?.checkpoints.length ?? 0"
      :connected="terminalConnected"
      :elapsed="elapsed"
      :resetting="resetting"
      :mobile-view="mobileView"
      @reset="reset"
      @update-mobile-view="mobileView = $event"
    />
    <div class="workspace-body" :class="`mobile-${mobileView}`">
      <WorkspaceSidebar
        :view="view"
        :checkpoints="content?.checkpoints ?? []"
        :results="checks"
        :collapsed="sidebarCollapsed"
        :error="progressError"
        @update-view="changeView"
        @toggle="sidebarCollapsed = !sidebarCollapsed"
        @refresh="refresh"
        @hint="showHint"
      />
      <section class="document-pane" :class="{ 'assistant-view': view === 'assistant' }">
        <div v-if="loadingContent" class="document-empty">
          Loading challenge content...
        </div>
        <div v-else-if="contentError" class="document-empty">
          <p>{{ contentError }}</p>
          <button class="compact-button" @click="loadContent">Retry</button>
        </div>
        <template v-else>
          <div class="document-toolbar">
            <span>{{ view === "problem" ? "Problem" : view === "solution" ? "Solution" : "Assistant" }}</span
            ><span v-if="progressLoading" class="muted-copy"
              >Checking environment...</span
            >
          </div>
          <AssistantChat
            v-if="view === 'assistant'"
            :challenge-id="challenge.id"
            :current-node="currentTerminalNode"
            :current-window="currentTerminalWindow"
            :terminals="terminalContexts"
          />
          <template v-else>
            <TaxonomyPanel v-if="view === 'problem' && content" :taxonomy="content.taxonomy" />
            <MarkdownDocument :source="documentSource" />
            <aside
              v-if="activeHint && content?.hints[activeHint]"
              class="hint-panel"
            >
              <div>
                <p class="eyebrow">Hint</p>
                <MarkdownDocument :source="content.hints[activeHint]" />
              </div>
              <button
                class="icon-button"
                title="Close hint"
                @click="activeHint = null"
              >
                x
              </button>
            </aside>
          </template>
        </template>
      </section>
      <TerminalPane
        :challenge-id="challenge.id"
        :runtime="content?.runtime ?? challenge.runtime"
        :nodes="content?.nodes ?? []"
        :visible="terminalVisible"
        @connected="terminalConnected = $event"
        @context="(node, current, terminals) => { currentTerminalNode = node; currentTerminalWindow = current; terminalContexts = terminals; }"
      />
    </div>
  </main>
</template>
