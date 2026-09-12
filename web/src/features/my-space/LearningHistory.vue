<script setup lang="ts">
import { CheckCircle2, CircleDashed, Clock3 } from "lucide-vue-next";
import type { MySpaceLearningHistory } from "../../api/types";
import type { LearningRuntimeFilter, LearningStateFilter } from "./useMySpace";

withDefaults(defineProps<{
  items: MySpaceLearningHistory[];
  loading: boolean;
  loadingMore: boolean;
  hasMore: boolean;
  showFilters?: boolean;
  stateFilter?: LearningStateFilter;
  runtimeFilter?: LearningRuntimeFilter;
}>(), {
  showFilters: false,
  stateFilter: "all",
  runtimeFilter: "all",
});
const emit = defineEmits<{ more: []; "update:stateFilter": [value: LearningStateFilter]; "update:runtimeFilter": [value: LearningRuntimeFilter] }>();

function duration(seconds: number) {
  const hours = Math.floor(seconds / 3600);
  const minutes = Math.floor((seconds % 3600) / 60);
  if (hours) return `${hours}h ${minutes}m`;
  return `${minutes}m`;
}
function date(value: string) {
  return new Intl.DateTimeFormat(undefined, { month: "short", day: "numeric", year: "numeric" }).format(new Date(value));
}
function stateLabel(item: MySpaceLearningHistory) {
  if (item.state === "completed") return "Completed";
  return item.state === "active" ? "In progress" : "Attempt ended";
}
function checkpointFirstPassLabel(item: MySpaceLearningHistory) {
  const count = item.checkpoint_first_passes.length;
  if (!count) return "";
  return `${count} checkpoint${count === 1 ? "" : "s"} first passed`;
}
function updateState(event: Event) {
  emit("update:stateFilter", (event.target as HTMLSelectElement).value as LearningStateFilter);
}
function updateRuntime(event: Event) {
  emit("update:runtimeFilter", (event.target as HTMLSelectElement).value as LearningRuntimeFilter);
}
</script>

<template>
  <section class="space-section learning-history" aria-labelledby="learning-history-title">
    <div class="space-section-heading history-heading">
      <div><p class="eyebrow">Learning record</p><h2 id="learning-history-title">Recent activity</h2></div>
      <div v-if="showFilters" class="history-filters">
        <label><span>State</span><select :value="stateFilter" aria-label="Filter learning state" @change="updateState"><option value="all">All activity</option><option value="active">In progress</option><option value="completed">Completed</option><option value="ended">Ended attempts</option></select></label>
        <label><span>Runtime</span><select :value="runtimeFilter" aria-label="Filter learning runtime" @change="updateRuntime"><option value="all">All runtimes</option><option value="node">Linux nodes</option><option value="k8s">Kubernetes</option></select></label>
      </div>
    </div>
    <p v-if="loading" class="space-empty">Loading learning history...</p>
    <div v-else-if="items.length" class="space-row-list history-list">
      <article v-for="item in items" :key="`${item.scenario.id}-${item.ready_at}`" class="space-row history-row">
        <div class="history-state" :class="{ complete: !!item.completed_at }">
          <CheckCircle2 v-if="item.completed_at" :size="18" aria-hidden="true" />
          <CircleDashed v-else :size="18" aria-hidden="true" />
        </div>
        <div class="space-row-main"><div class="space-row-title"><h3>{{ item.scenario.title }}</h3></div><p>{{ stateLabel(item) }} · {{ date(item.state === "completed" ? item.completed_at ?? item.ready_at : item.ready_at) }}<span v-if="checkpointFirstPassLabel(item)"> · {{ checkpointFirstPassLabel(item) }}</span></p></div>
        <span class="history-duration"><Clock3 :size="13" aria-hidden="true" />{{ duration(item.learning_seconds) }}</span>
      </article>
    </div>
    <p v-else class="space-empty">Your completed and recovered attempts will appear here.</p>
    <button v-if="hasMore" class="text-button history-more" type="button" :disabled="loadingMore" @click="emit('more')">{{ loadingMore ? "Loading..." : "Load more" }}</button>
  </section>
</template>
