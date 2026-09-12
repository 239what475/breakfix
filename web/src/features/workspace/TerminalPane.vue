<script setup lang="ts">
import { computed, nextTick, reactive, ref, watch } from "vue";
import { Monitor, Plus, X } from "lucide-vue-next";
import { api } from "../../api/client";
import type { AssistantTerminalContext, ScenarioNode } from "../../api/types";
import { useTerminalSession } from "./useTerminalSession";

const props = defineProps<{
  scenarioId: string;
  runtime: "node" | "k8s";
  nodes: ScenarioNode[];
  visible: boolean;
}>();
const emit = defineEmits<{
  connected: [connected: boolean];
  context: [currentNode: string, currentWindow: string, terminals: AssistantTerminalContext[]];
}>();
const host = ref<HTMLDivElement>();
const selectedNode = ref("");
const tabsByNode = reactive<Record<string, string[]>>({});
const activeByNode = reactive<Record<string, string | null>>({});
const scenario = computed(() => props.scenarioId);
const node = computed(() => props.runtime === "node" ? selectedNode.value || null : null);
const terminalKey = computed(() => node.value || "management");
const tabs = computed(() => tabsByNode[terminalKey.value] ?? []);
const active = computed<string | null>({
  get: () => activeByNode[terminalKey.value] ?? null,
  set: (value) => { activeByNode[terminalKey.value] = value; },
});
const { state, stateMessage, connect, focus, refreshLayout } =
  useTerminalSession(host, scenario, node, active);
const connected = computed(() => state.value === "connected");
watch(connected, (value) => emit("connected", value), { immediate: true });

function contexts(): AssistantTerminalContext[] {
  return Object.entries(tabsByNode)
    .filter(([, windows]) => windows.length > 0)
    .map(([key, windows]) => ({
      ...(props.runtime === "node" ? { node: key } : {}),
      windows: [...windows],
    }));
}

function emitContext() {
  emit("context", node.value ?? "", active.value ?? "shell-1", contexts());
}

watch(
  [node, active, () => contexts()],
  emitContext,
  { deep: true, immediate: true },
);

function ensureNode(key: string) {
  if (tabsByNode[key]) return;
  tabsByNode[key] = ["shell-1"];
  activeByNode[key] = "shell-1";
}

function resetTerminals() {
  for (const key of Object.keys(tabsByNode)) delete tabsByNode[key];
  for (const key of Object.keys(activeByNode)) delete activeByNode[key];
  selectedNode.value = props.runtime === "node" ? (props.nodes[0]?.name ?? "") : "";
  if (props.runtime === "k8s") ensureNode("management");
  else if (selectedNode.value) ensureNode(selectedNode.value);
  emitContext();
}

watch(
  [() => props.scenarioId, () => props.runtime, () => props.nodes.map((item) => item.name).join("\0")],
  resetTerminals,
  { immediate: true },
);

watch(selectedNode, (value) => {
  if (props.runtime === "node" && value) ensureNode(value);
  emitContext();
});
watch(
  () => props.visible,
  async (visible) => {
    if (!visible) return;
    await nextTick();
    refreshLayout();
  },
);
function addTab() {
  const key = terminalKey.value;
  ensureNode(key);
  let number = 1;
  while (tabs.value.includes(`shell-${number}`)) number += 1;
  const name = `shell-${number}`;
  tabsByNode[key].push(name);
  active.value = name;
  void nextTick(connect);
}

async function closeTab(name: string) {
  const key = terminalKey.value;
  const targetNode = node.value;
  if (name === active.value) active.value = null;
  tabsByNode[key] = tabs.value.filter((tab) => tab !== name);
  try {
    await api.closeTerminalWindow(props.scenarioId, name, targetNode || undefined);
  } catch {
    /* the local tab is already gone */
  }
  if (!active.value && tabsByNode[key].length)
    active.value = tabsByNode[key][tabsByNode[key].length - 1];
  emitContext();
}
</script>

<template>
  <section class="terminal-pane" @pointerdown="focus">
    <div class="terminal-tabs">
      <label v-if="runtime === 'node'" class="terminal-node-selector">
        <Monitor :size="14" aria-hidden="true" />
        <span class="sr-only">Terminal node</span>
        <select v-model="selectedNode" aria-label="Terminal node">
          <option v-for="item in nodes" :key="item.name" :value="item.name">
            {{ item.title }}
          </option>
        </select>
      </label>
      <div class="terminal-tab-list">
        <div
          v-for="tab in tabs"
          :key="tab"
          class="terminal-tab-group"
          :class="{ active: active === tab }"
        >
          <button
            class="terminal-tab"
            :class="{ active: active === tab }"
            @click="active = tab"
          >
            {{ tab }}
          </button>
          <button
            v-if="tabs.length > 1"
            class="terminal-tab-close"
            :aria-label="`Close ${tab}`"
            :title="`Close ${tab}`"
            @click="closeTab(tab)"
          >
            <X :size="13" aria-hidden="true" />
          </button>
        </div>
      </div>
      <button
        class="icon-button terminal-add"
        title="New terminal"
        aria-label="New terminal"
        @click="addTab"
      >
        <Plus :size="16" aria-hidden="true" />
      </button>
    </div>
    <div class="terminal-status">
      <span :class="['connection-dot', state]"></span
      ><span>{{
        state === "connected"
          ? "Connected"
          : state === "connecting"
            ? "Connecting"
            : state === "idle"
              ? "No terminal"
              : "Disconnected"
      }}</span
      ><button
        v-if="state === 'disconnected'"
        class="text-button"
        @click="connect"
      >
        Reconnect
      </button>
    </div>
    <div class="terminal-body">
      <div v-if="active" ref="host" class="terminal-host"></div>
      <div v-else class="terminal-empty">
        <p>All terminal tabs are closed.</p>
        <button class="compact-button" @click="addTab">Open terminal</button>
      </div>
      <div
        v-if="state === 'disconnected' && active"
        class="terminal-disconnected"
      >
        <p>{{ stateMessage }}</p>
        <button class="compact-button" @click="connect">Reconnect</button>
      </div>
    </div>
  </section>
</template>
