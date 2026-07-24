<script setup lang="ts">
import { Clock3, FileCheck2, Flag, Trophy } from "lucide-vue-next";
import type { MySpaceSummary } from "../../api/types";

defineProps<{ summary: MySpaceSummary }>();

function duration(seconds: number) {
  const hours = Math.floor(seconds / 3600);
  const minutes = Math.floor((seconds % 3600) / 60);
  if (hours) return `${hours}h ${minutes}m`;
  return `${minutes}m`;
}
</script>

<template>
  <dl class="space-summary" aria-label="Learning summary">
    <div>
      <dt><Trophy :size="14" aria-hidden="true" />Completed</dt>
      <dd>{{ summary.completed_count }}</dd>
    </div>
    <div>
      <dt><Flag :size="14" aria-hidden="true" />Attempted</dt>
      <dd>{{ summary.attempted_count }}</dd>
    </div>
    <div>
      <dt><Clock3 :size="14" aria-hidden="true" />Terminal time</dt>
      <dd>{{ duration(summary.terminal_learning_seconds) }}</dd>
    </div>
    <div>
      <dt><FileCheck2 :size="14" aria-hidden="true" />Published</dt>
      <dd>{{ summary.published_count }}</dd>
    </div>
  </dl>
</template>
