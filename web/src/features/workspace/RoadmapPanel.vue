<script setup lang="ts">
import type { ChallengeRoadmap } from "../../api/types";

defineProps<{ roadmap: ChallengeRoadmap }>();
</script>

<template>
  <section class="roadmap-panel" aria-label="Learning context">
    <div class="roadmap-group">
      <p class="eyebrow">Topic</p>
      <strong>{{ roadmap.topic.title }}</strong>
      <small>{{ roadmap.domain.title }}</small>
    </div>
    <div v-if="roadmap.tags.length" class="roadmap-group">
      <p class="eyebrow">Tags</p>
      <ul class="roadmap-tags">
        <li v-for="tag in roadmap.tags" :key="tag.id">{{ tag.title }}</li>
      </ul>
    </div>
  </section>
</template>
