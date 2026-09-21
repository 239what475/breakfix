<script setup lang="ts">
import { Boxes, RotateCw, SquarePlus } from "lucide-vue-next";
import { computed, nextTick, onUnmounted, ref, watch } from "vue";
import TerminalPane from "../workspace/TerminalPane.vue";
import { playgroundTerminalChannel, type TerminalChannel } from "../workspace/useTerminalSession";
import { usePlayground } from "./usePlayground";
import "./playground.css";

const props = defineProps<{ authSignal?: number }>();
const emit = defineEmits<{ notice: [text: string, kind?: "error" | "info"] }>();

const notify = (text: string, kind?: "error" | "info") => emit("notice", text, kind);
const playground = usePlayground(notify);

const open = ref(false);
const fab = ref<HTMLButtonElement>();
const panel = ref<HTMLElement>();

// The dock mounts only for signed-in users; every signal is a login landing,
// so each one reconciles whatever session the new identity already owns.
watch(
  () => props.authSignal,
  () => void playground.refresh(),
);
void playground.refresh();

const channel = computed<TerminalChannel>(() => playgroundTerminalChannel());
const nodes = computed(() => [{ name: "terminal", title: "terminal" }]);

const stateLine = computed(() => {
  if (playground.state.value === "ready") return "Ready — the cluster is yours.";
  if (playground.state.value === "failed") return "The playground failed to start.";
  if (playground.resetting.value) return "Resetting the playground...";
  return "Preparing the playground...";
});

async function openPanel() {
  open.value = true;
  await nextTick();
  panel.value?.focus();
}

async function closePanel() {
  open.value = false;
  await nextTick();
  fab.value?.focus();
}

const focusables = "button, [href], input, select, textarea, [tabindex]:not([tabindex='-1'])";

// The dialog traps Tab while open: focusables inside the panel cycle, Esc
// hands the focus back to the ball.
function onPanelKeydown(event: KeyboardEvent) {
  if (event.key === "Escape") {
    event.preventDefault();
    void closePanel();
    return;
  }
  if (event.key !== "Tab" || !panel.value) return;
  const items = Array.from(panel.value.querySelectorAll<HTMLElement>(focusables)).filter(
    (element) => !element.hasAttribute("disabled"),
  );
  if (items.length === 0) return;
  const first = items[0]!;
  const last = items[items.length - 1]!;
  const active = panel.value.contains(document.activeElement) ? document.activeElement : null;
  if (!event.shiftKey && (active === last || active === panel.value)) {
    event.preventDefault();
    first.focus();
  } else if (event.shiftKey && (active === first || active === panel.value || active === null)) {
    event.preventDefault();
    last.focus();
  }
}

function onFabKeydown(event: KeyboardEvent) {
  if (event.key === "Escape" && open.value) {
    event.preventDefault();
    open.value = false;
  }
}

onUnmounted(() => window.removeEventListener("keydown", onGlobalKeydown));
function onGlobalKeydown(event: KeyboardEvent) {
  if (event.key === "Escape" && open.value) {
    event.preventDefault();
    open.value = false;
    fab.value?.focus();
  }
}
window.addEventListener("keydown", onGlobalKeydown);
</script>

<template>
  <button
    ref="fab"
    class="playground-fab"
    :data-state="playground.state.value"
    type="button"
    aria-label="Playground"
    :aria-expanded="open"
    @click="open ? (open = false) : void openPanel()"
    @keydown="onFabKeydown"
  >
    <component :is="playground.state.value === 'failed' ? SquarePlus : Boxes" :size="20" aria-hidden="true" />
    <span v-if="playground.state.value === 'creating'" class="playground-fab-spinner" aria-hidden="true"></span>
    <span v-else-if="playground.state.value === 'ready'" class="playground-fab-dot" aria-hidden="true"></span>
    <span class="visually-hidden">{{ playground.state.value }}</span>
  </button>

  <div
    v-if="open"
    ref="panel"
    class="playground-panel"
    role="dialog"
    aria-label="Playground"
    tabindex="-1"
    @keydown="onPanelKeydown"
  >
    <div class="playground-panel-head">
      <div>
        <span class="playground-panel-kicker">Practice ground</span>
        <h2 class="playground-panel-title">Playground</h2>
      </div>
      <button class="compact-button" type="button" aria-label="Close playground" @click="void closePanel()">
        <span aria-hidden="true">×</span>
      </button>
    </div>
    <p class="playground-panel-hint">A fresh Kubernetes cluster to try things out.</p>

    <div
      v-if="playground.state.value !== 'none'"
      class="playground-panel-state"
      :class="{ 'playground-panel-state-error': playground.state.value === 'failed' }"
      role="status"
    >
      {{ stateLine }}
      <button v-if="playground.state.value === 'failed'" class="compact-button" type="button" @click="void playground.create()">Retry</button>
    </div>

    <div v-if="playground.state.value === 'none'" class="playground-panel-actions">
      <button
        class="compact-button"
        type="button"
        :disabled="playground.starting.value"
        @click="void playground.create()"
      >
        {{ playground.starting.value ? "Creating..." : "Create" }}
      </button>
    </div>

    <!-- Close belongs to every existing session, creating ones included: it is
         how a stuck preparation is abandoned. -->
    <div v-if="playground.state.value !== 'none'" class="playground-panel-actions">
      <button
        v-if="playground.state.value === 'ready'"
        class="compact-button"
        type="button"
        :disabled="playground.resetting.value"
        @click="void playground.reset()"
      >
        <RotateCw :size="13" aria-hidden="true" :class="{ spinning: playground.resetting.value }" />
        {{ playground.resetting.value ? "Resetting..." : "Reset" }}
      </button>
      <button class="compact-button danger-button" type="button" :disabled="playground.stopping.value" @click="void playground.close()">
        {{ playground.stopping.value ? "Closing..." : "Close" }}
      </button>
    </div>

    <TerminalPane
      v-if="playground.state.value === 'ready'"
      terminal-id="playground"
      :runtime="playground.runtime.value"
      :nodes="nodes"
      :visible="true"
      :channel="channel"
      class="playground-panel-terminal"
    />
  </div>
</template>
