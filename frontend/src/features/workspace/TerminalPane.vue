<script setup lang="ts">
import { computed, nextTick, ref, watch } from "vue";
import { Plus, X } from "lucide-vue-next";
import { api } from "../../api/client";
import { useTerminalSession } from "./useTerminalSession";

const props = defineProps<{
  challengeId: string;
  visible: boolean;
}>();
const emit = defineEmits<{
  connected: [connected: boolean];
  context: [currentWindow: string, openWindows: string[]];
}>();
const host = ref<HTMLDivElement>();
const tabs = ref(["shell-1"]);
const active = ref<string | null>("shell-1");
const challenge = computed(() => props.challengeId);
const { state, stateMessage, connect, focus, refreshLayout } =
  useTerminalSession(host, challenge, active);
const connected = computed(() => state.value === "connected");
watch(connected, (value) => emit("connected", value), { immediate: true });
watch(
  [active, tabs],
  () => emit("context", active.value ?? "shell-1", [...tabs.value]),
  { deep: true, immediate: true },
);
watch(
  () => props.challengeId,
  () => {
    tabs.value = ["shell-1"];
    active.value = "shell-1";
  },
);
watch(
  () => props.visible,
  async (visible) => {
    if (!visible) return;
    await nextTick();
    refreshLayout();
  },
);
function addTab() {
  let number = 1;
  while (tabs.value.includes(`shell-${number}`)) number += 1;
  const name = `shell-${number}`;
  tabs.value.push(name);
  active.value = name;
  void nextTick(connect);
}

async function closeTab(name: string) {
  if (name === active.value) active.value = null;
  tabs.value = tabs.value.filter((tab) => tab !== name);
  try {
    await api.closeTerminalWindow(props.challengeId, name);
  } catch {
    /* the local tab is already gone */
  }
  if (!active.value && tabs.value.length)
    active.value = tabs.value[tabs.value.length - 1];
}
</script>

<template>
  <section class="terminal-pane" @pointerdown="focus">
    <div class="terminal-tabs">
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
