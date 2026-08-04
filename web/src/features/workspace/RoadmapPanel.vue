<script setup lang="ts">
import type { ChallengeRoadmap } from "../../api/types";

const props = defineProps<{ roadmap: ChallengeRoadmap; challengeId: string }>();

function neighborTitle(sourceId: string, sourceTitle: string, targetTitle: string) {
	return sourceId === props.challengeId || sourceId === props.roadmap.topic.id ? targetTitle : sourceTitle;
}
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
    <div v-if="roadmap.topic_neighbors.length" class="roadmap-group roadmap-neighbors">
      <p class="eyebrow">Topic graph</p>
      <ul class="roadmap-edge-list">
        <li v-for="edge in roadmap.topic_neighbors" :key="`${edge.source.id}-${edge.target.id}-${edge.relation}`">
          <strong>{{ neighborTitle(edge.source.id, edge.source.title, edge.target.title) }}</strong>
          <span>{{ edge.relation }}</span>
          <small>{{ edge.reason }}</small>
        </li>
      </ul>
    </div>
    <div v-if="roadmap.challenge_neighbors.length" class="roadmap-group roadmap-neighbors">
      <p class="eyebrow">Challenge graph</p>
      <ul class="roadmap-edge-list">
        <li v-for="edge in roadmap.challenge_neighbors" :key="`${edge.source.id}-${edge.target.id}-${edge.relation}`">
          <strong>{{ neighborTitle(edge.source.id, edge.source.title, edge.target.title) }}</strong>
          <span>{{ edge.relation }}</span>
          <small>{{ edge.reason }}</small>
        </li>
      </ul>
    </div>
  </section>
</template>
