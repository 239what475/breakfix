<script setup lang="ts">
import { Play, Square, TimerReset } from "lucide-vue-next";
import type { MySpaceActiveEnvironment } from "../../api/types";

const props = defineProps<{ environments: MySpaceActiveEnvironment[]; stoppingId?: string | null }>();
const emit = defineEmits<{ start: [id: string]; stop: [id: string] }>();

function checkpointLabel(environment: MySpaceActiveEnvironment) {
  if (!environment.checkpoint_progress || environment.checkpoint_progress.total === 0) return "No checkpoints";
  return `${environment.checkpoint_progress.passed}/${environment.checkpoint_progress.total} checkpoints`;
}

function sourceLabel(source: string | undefined) {
  return source === "documentation" ? "Documentation" : "Operations";
}

const phaseLabels: Record<string, string> = { Pending: "Preparing", Provisioning: "Preparing", Ready: "Ready", Draining: "Draining" };

function phaseLabel(environment: MySpaceActiveEnvironment) {
  // A resetting playground keeps its stale Ready phase until the rebuilt
  // terminal reports Ready; the row must say so instead of ready.
  if (environment.operation === "Resetting") return "Resetting";
  return phaseLabels[environment.phase] ?? environment.phase;
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
          <!-- The playground binds the user alone: no scenario identity, no
               checkpoint progress, and its lifecycle verbs live on the
               floating ball instead of this row. -->
          <template v-if="environment.kind === 'playground'">
            <div class="space-row-title"><h3>Playground</h3><span class="runtime-pill">Playground</span><span class="runtime-pill">{{ environment.runtime }}</span></div>
            <div class="space-row-meta"><span>{{ phaseLabel(environment) }}</span><span><TimerReset :size="13" aria-hidden="true" />{{ expiryLabel(environment.expires_at) }}</span></div>
          </template>
          <template v-else-if="environment.scenario">
            <div class="space-row-title"><h3>{{ environment.scenario.title }}</h3><span class="runtime-pill">{{ sourceLabel(environment.scenario.content_source) }}</span><span class="runtime-pill">{{ environment.runtime }}</span></div>
            <div class="space-row-meta"><span>{{ checkpointLabel(environment) }}</span><span><TimerReset :size="13" aria-hidden="true" />{{ expiryLabel(environment.expires_at) }}</span></div>
          </template>
        </div>
        <div v-if="environment.scenario" class="space-environment-actions">
          <button class="compact-button space-start-button" type="button" @click="emit('start', environment.scenario.id)"><Play :size="14" fill="currentColor" aria-hidden="true" />Start scenario</button>
          <button class="compact-button danger-button space-stop-button" type="button" :disabled="props.stoppingId === environment.scenario.id" @click="emit('stop', environment.scenario.id)"><Square :size="13" fill="currentColor" aria-hidden="true" />{{ props.stoppingId === environment.scenario.id ? "Stopping..." : "Stop" }}</button>
        </div>
      </article>
    </div>
    <p v-else class="space-empty">No active environment. Pick a scenario from the catalog when you are ready.</p>
  </section>
</template>
