<script setup lang="ts">
import type { ChallengeTaxonomy } from "../../api/types";

defineProps<{ taxonomy: ChallengeTaxonomy }>();
</script>

<template>
  <section class="taxonomy-panel" aria-label="Learning context">
    <div class="taxonomy-group">
      <p class="eyebrow">Practice</p>
      <strong>{{ taxonomy.primary_outcome.title }}</strong>
      <ul v-if="taxonomy.outcomes.length > 1" class="taxonomy-outcomes">
        <li v-for="outcome in taxonomy.outcomes.filter((outcome) => !outcome.primary)" :key="outcome.id">{{ outcome.title }}</li>
      </ul>
    </div>
    <div v-if="taxonomy.entry_skills.length" class="taxonomy-group">
      <p class="eyebrow">Recommended background</p>
      <ul class="taxonomy-entry-skills">
        <li v-for="skill in taxonomy.entry_skills" :key="skill.id">
          <strong>{{ skill.title }}</strong>
          <small v-if="skill.requires.length">Builds on: {{ skill.requires.map((requirement) => requirement.title).join(", ") }}</small>
        </li>
      </ul>
    </div>
  </section>
</template>
