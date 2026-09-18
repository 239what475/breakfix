<script setup lang="ts">
import { Clock3, RotateCcw, Square } from "lucide-vue-next";
import type { Scenario } from "../../api/types";

defineProps<{
  scenario: Scenario;
  complete: number;
  total: number;
  hasCheckpoints: boolean;
  connected: boolean;
  elapsed: string;
  resetting: boolean;
  stopping: boolean;
}>();
const emit = defineEmits<{
  reset: [];
  stop: [];
}>();
</script>

<template>
  <header class="workspace-header">
    <div class="workspace-title"><div><p class="eyebrow">{{ scenario.runtime }} lab</p><h1>{{ scenario.title }}</h1><div v-if="scenario.scenario_tags.length" class="workspace-tags"><span v-for="tag in scenario.scenario_tags" :key="tag">{{ tag }}</span></div></div></div>
    <div class="workspace-header-actions">
      <span v-if="hasCheckpoints" class="progress-count">{{ complete }} / {{ total }} complete</span>
      <span v-if="total > 0 && complete === total" class="completion-state">All checkpoints complete</span>
      <span class="elapsed-time"><Clock3 :size="13" aria-hidden="true" />{{ elapsed }}</span>
      <span class="connection-state"><i :class="{ online: connected }"></i>{{ connected ? "terminal connected" : "terminal offline" }}</span>
      <button class="compact-button" :disabled="resetting || stopping" @click="emit('reset')"><RotateCcw :size="14" aria-hidden="true" />{{ resetting ? "Resetting..." : "Reset" }}</button>
      <button class="compact-button danger-button" :disabled="resetting || stopping" @click="emit('stop')"><Square :size="13" fill="currentColor" aria-hidden="true" />{{ stopping ? "Stopping..." : "Stop" }}</button>
    </div>
  </header>
</template>
