<script setup lang="ts">
import { Boxes, RotateCw, SquarePlus, X } from "lucide-vue-next";
import type { BlankScenarioState } from "./useBlankScenario";

const props = defineProps<{
  state: BlankScenarioState;
  starting: boolean;
  stopping: boolean;
  resetting: boolean;
}>();

const emit = defineEmits<{ create: []; reset: []; close: [] }>();

// The availability matrix follows the session state machine: create is the
// entry from none or a retry from failed, reset belongs to a ready session,
// and close releases any session that still exists.
const stateLabel: Record<BlankScenarioState, string> = {
  none: "No scenario",
  creating: "Preparing",
  ready: "Ready",
  failed: "Failed",
};

function canCreate() {
  return (props.state === "none" || props.state === "failed") && !props.starting;
}
function canReset() {
  return props.state === "ready" && !props.resetting;
}
function canClose() {
  return props.state !== "none" && !props.stopping;
}
</script>

<template>
  <aside class="scenario-toolbar" aria-label="Practice ground">
    <span class="scenario-toolbar-badge" :data-state="state" role="status">{{ stateLabel[state] }}</span>
    <div class="scenario-toolbar-buttons">
      <button
        class="scenario-toolbar-button"
        type="button"
        :disabled="!canCreate()"
        aria-label="Create blank scenario"
        title="Create a blank scenario"
        @click="emit('create')"
      >
        <component :is="state === 'failed' ? SquarePlus : Boxes" :size="16" aria-hidden="true" />
      </button>
      <button
        class="scenario-toolbar-button"
        type="button"
        :disabled="!canReset()"
        aria-label="Reset blank scenario"
        title="Reset the scenario to a fresh state"
        @click="emit('reset')"
      >
        <RotateCw :size="16" aria-hidden="true" :class="{ spinning: resetting && state === 'ready' }" />
      </button>
      <button
        class="scenario-toolbar-button"
        type="button"
        :disabled="!canClose()"
        aria-label="Close blank scenario"
        title="Close and release the scenario"
        @click="emit('close')"
      >
        <X :size="16" aria-hidden="true" />
      </button>
    </div>
  </aside>
</template>
