<script setup lang="ts">
import { computed } from "vue";
import TerminalPane from "../workspace/TerminalPane.vue";
import { blankScenarioTerminalChannel } from "../workspace/useTerminalSession";
import type { BlankScenarioState } from "./useBlankScenario";

const props = defineProps<{
  state: BlankScenarioState;
  starting: boolean;
  stopping: boolean;
  resetting: boolean;
  runtime: "node" | "k8s";
}>();

const emit = defineEmits<{ create: []; reset: []; close: [] }>();

// The blank panel carries no steps or observations: the documentation page is
// the guidance and the terminal is the whole session.
const channel = computed(() => blankScenarioTerminalChannel());
const nodes = computed(() => [{ name: "terminal", title: "terminal" }]);

function statusLine() {
  if (props.state === "failed") return "The practice environment failed to start.";
  if (props.resetting) return "Resetting the environment...";
  return "Preparing the environment...";
}
</script>

<template>
  <aside class="scenario-panel" aria-label="Practice ground">
    <div class="scenario-panel-head">
      <div>
        <span class="scenario-panel-kicker">Practice ground</span>
        <h2 class="scenario-panel-title">Blank scenario</h2>
      </div>
      <button class="compact-button" type="button" aria-label="Close practice panel" @click="emit('close')">
        <span aria-hidden="true">×</span>
      </button>
    </div>
    <p class="scenario-panel-hint">A fresh Kubernetes cluster to try what you are reading.</p>

    <div v-if="state === 'creating' || state === 'failed'" class="scenario-panel-state" :class="{ 'scenario-panel-state-error': state === 'failed' }" role="status">
      {{ statusLine() }}
      <button v-if="state === 'failed'" class="compact-button" type="button" @click="emit('create')">Retry</button>
    </div>

    <template v-if="state === 'ready'">
      <div class="scenario-panel-actions">
        <button class="compact-button" type="button" :disabled="resetting" @click="emit('reset')">
          {{ resetting ? "Resetting..." : "Reset" }}
        </button>
        <button class="compact-button danger-button" type="button" :disabled="stopping" @click="emit('close')">
          {{ stopping ? "Closing..." : "Close" }}
        </button>
      </div>
      <TerminalPane
        terminal-id="documentation-scenario"
        :runtime="runtime"
        :nodes="nodes"
        :visible="true"
        :channel="channel"
        class="scenario-panel-terminal"
      />
    </template>
  </aside>
</template>
