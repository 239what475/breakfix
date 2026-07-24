<script setup lang="ts">
import { Clock3, RotateCcw } from "lucide-vue-next";
import type { Challenge } from "../../api/types";

defineProps<{
  challenge: Challenge;
  complete: number;
  total: number;
  connected: boolean;
  elapsed: string;
  resetting: boolean;
  mobileView: "document" | "terminal";
}>();
const emit = defineEmits<{
  reset: [];
  updateMobileView: [view: "document" | "terminal"];
}>();
</script>

<template>
  <header class="workspace-header">
    <div class="workspace-title"><div><p class="eyebrow">{{ challenge.runtime }} lab</p><h1>{{ challenge.title }}</h1><div class="workspace-tags"><span :class="['difficulty', challenge.difficulty]">{{ challenge.difficulty }}</span><span v-for="tag in challenge.tags" :key="tag">{{ tag }}</span></div></div></div>
    <div class="workspace-header-actions">
      <div class="mobile-view-toggle"><button :class="{ active: mobileView === 'document' }" @click="emit('updateMobileView', 'document')">Docs</button><button :class="{ active: mobileView === 'terminal' }" @click="emit('updateMobileView', 'terminal')">Terminal</button></div>
      <span class="progress-count">{{ complete }} / {{ total }} complete</span>
      <span v-if="total > 0 && complete === total" class="completion-state">All checkpoints complete</span>
      <span class="elapsed-time"><Clock3 :size="13" aria-hidden="true" />{{ elapsed }}</span>
      <span class="connection-state"><i :class="{ online: connected }"></i>{{ connected ? "terminal connected" : "terminal offline" }}</span>
      <button class="compact-button" :disabled="resetting" @click="emit('reset')"><RotateCcw :size="14" aria-hidden="true" />{{ resetting ? "Resetting..." : "Reset" }}</button>
    </div>
  </header>
</template>
