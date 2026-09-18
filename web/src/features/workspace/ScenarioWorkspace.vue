<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from "vue";
import { api } from "../../api/client";
import type { AssistantTerminalContext, Scenario, ScenarioContent } from "../../api/types";
import MarkdownDocument from "./MarkdownDocument.vue";
import AssistantChat from "./AssistantChat.vue";
import ScenarioOverview from "./ScenarioOverview.vue";
import TerminalPane from "./TerminalPane.vue";
import WorkspaceHeader from "./WorkspaceHeader.vue";
import WorkspaceSidebar from "./WorkspaceSidebar.vue";
import { scenarioTerminalChannel } from "./useTerminalSession";
import { useScenarioProgress } from "./useScenarioProgress";
import "./workspace.css";
import "./assistant.css";

const props = defineProps<{ scenario: Scenario }>();
const emit = defineEmits<{
  changed: [];
  stopped: [];
  notice: [message: string, kind?: "error" | "info"];
}>();
const content = ref<ScenarioContent>();
const loadingContent = ref(false);
const contentError = ref("");
const view = ref<"overview" | "problem" | "solution" | "assistant">("overview");
const sidebarCollapsed = ref(false);
const isNarrow = ref(false);
const activeHint = ref<string | null>(null);
const terminalConnected = ref(false);
const currentTerminalNode = ref("");
const currentTerminalWindow = ref("shell-1");
const terminalContexts = ref<AssistantTerminalContext[]>([{ windows: ["shell-1"] }]);
const resetting = ref(false);
const stopping = ref(false);
const sessionStartedAt = ref(Date.now());
const elapsedSeconds = ref(0);
let elapsedTimer: number | undefined;
const scenarioId = computed(() => props.scenario.id);
const terminalChannel = computed(() => scenarioTerminalChannel(props.scenario.id));
const progressEnabled = computed(
  () => !document.hidden,
);
const {
  checks,
  loading: progressLoading,
  error: progressError,
  refresh,
} = useScenarioProgress(scenarioId, progressEnabled);
const completed = computed(
  () => checks.value.filter((check) => check.passed).length,
);
const documentSource = computed(() =>
  view.value === "problem"
    ? (content.value?.problem ?? "")
    : (content.value?.solution ?? ""),
);
const terminalVisible = computed(
  () => !isNarrow.value,
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
    content.value = await api.getScenarioContent(props.scenario.id);
    view.value = "overview";
  } catch (err) {
    contentError.value =
      err instanceof Error ? err.message : "Unable to load scenario content";
  } finally {
    loadingContent.value = false;
  }
}

function showHint(id: string) {
  activeHint.value = activeHint.value === id ? null : id;
}

function changeView(next: "overview" | "problem" | "solution" | "assistant") {
  view.value = next;
  if (next === "assistant") activeHint.value = null;
}

async function reset() {
  resetting.value = true;
  try {
    await api.resetScenario(props.scenario.id);
    checks.value = [];
    emit("notice", "Scenario reset.", "info");
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

async function stop() {
  if (!window.confirm("Stop this environment? Any unsaved changes in it will be destroyed, while the history record will remain.")) return;
  stopping.value = true;
  try {
    await api.stopScenario(props.scenario.id);
    emit("stopped");
  } catch (err) {
    emit(
      "notice",
      err instanceof Error ? err.message : "Stop failed",
      "error",
    );
  } finally {
    stopping.value = false;
  }
}

watch(
  () => props.scenario.id,
  () => {
    content.value = undefined;
    checks.value = [];
    activeHint.value = null;
    currentTerminalNode.value = "";
    currentTerminalWindow.value = "shell-1";
    terminalContexts.value = [{ windows: ["shell-1"] }];
    view.value = "overview";
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
  <main class="scenario-workspace">
    <WorkspaceHeader
      :scenario="scenario"
      :complete="completed"
      :total="content?.checkpoints.length ?? 0"
      :has-checkpoints="(content?.checkpoints.length ?? 0) > 0"
      :connected="terminalConnected"
      :elapsed="elapsed"
      :resetting="resetting"
      :stopping="stopping"
      @reset="reset"
      @stop="stop"
    />
    <div class="workspace-body">
      <WorkspaceSidebar
        :view="view"
        :has-problem="!!content?.problem"
        :has-solution="!!content?.solution"
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
          Loading scenario content...
        </div>
        <div v-else-if="contentError" class="document-empty">
          <p>{{ contentError }}</p>
          <button class="compact-button" @click="loadContent">Retry</button>
        </div>
        <template v-else>
          <div class="document-toolbar">
            <span>{{ view === "overview" ? "Overview" : view === "problem" ? "Problem" : view === "solution" ? "Solution" : "Assistant" }}</span
            ><span v-if="progressLoading" class="muted-copy"
              >Checking environment...</span
            >
          </div>
          <ScenarioOverview v-if="view === 'overview' && content" :content="content" />
          <AssistantChat
            v-if="view === 'assistant'"
            :scenario-id="scenario.id"
            :current-node="currentTerminalNode"
            :current-window="currentTerminalWindow"
            :terminals="terminalContexts"
          />
          <template v-else-if="view !== 'overview'">
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
        :terminal-id="scenario.id"
        :runtime="content?.runtime ?? scenario.runtime"
        :nodes="content?.nodes ?? []"
        :visible="terminalVisible"
        :channel="terminalChannel"
        :close-window="async (window, node) => { await api.closeTerminalWindow(scenario.id, window, node); }"
        @connected="terminalConnected = $event"
        @context="(node, current, terminals) => { currentTerminalNode = node; currentTerminalWindow = current; terminalContexts = terminals; }"
      />
    </div>
  </main>
</template>
