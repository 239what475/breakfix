<script setup lang="ts">
import { Play, TimerReset } from "lucide-vue-next";
import type { MySpaceActiveEnvironment } from "../../api/types";

defineProps<{ environments: MySpaceActiveEnvironment[] }>();
const emit = defineEmits<{ start: [id: string] }>();

function checkpointLabel(environment: MySpaceActiveEnvironment) {
  return `${environment.checkpoint_progress.passed}/${environment.checkpoint_progress.total} checkpoints`;
}

function expiryLabel(expiresAt?: string | null) {
  if (!expiresAt) return "No expiry";
  const remaining = Math.max(0, new Date(expiresAt).getTime() - Date.now());
  const minutes = Math.ceil(remaining / 60000);
  return minutes > 1 ? `${minutes} min left` : "Less than a minute left";
}
</script>

<template>
  <section class="space-section active-environments" aria-labelledby="active-environments-title">
    <div class="space-section-heading">
      <div>
        <p class="eyebrow">Continue learning</p>
        <h2 id="active-environments-title">Active environments</h2>
      </div>
      <span class="section-count">{{ environments.length }}</span>
    </div>
    <div v-if="environments.length" class="space-row-list">
      <article v-for="environment in environments" :key="environment.environment_id" class="space-row active-environment-row">
        <div class="space-row-main">
          <div class="space-row-title"><h3>{{ environment.scenario.title }}</h3><span class="runtime-pill">{{ environment.runtime }}</span></div>
          <div class="space-row-meta"><span>{{ checkpointLabel(environment) }}</span><span><TimerReset :size="13" aria-hidden="true" />{{ expiryLabel(environment.expires_at) }}</span></div>
        </div>
        <button class="compact-button space-start-button" type="button" @click="emit('start', environment.scenario.id)"><Play :size="14" fill="currentColor" aria-hidden="true" />Start scenario</button>
      </article>
    </div>
    <p v-else class="space-empty">No active environment. Pick a scenario from the catalog when you are ready.</p>
  </section>
</template>
