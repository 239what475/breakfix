<script setup lang="ts">
import { ChevronDown, ChevronRight, X } from "lucide-vue-next";
import { ref } from "vue";
import type { DocumentationPracticeDetail } from "../../api/generated";

defineProps<{
  detail: DocumentationPracticeDetail | null;
  loading: boolean;
  failed: boolean;
}>();

const emit = defineEmits<{ close: [] }>();

const stepsOpen = ref(true);
const observationsOpen = ref(true);
</script>

<template>
  <aside class="practice-panel" aria-label="Practice">
    <div class="practice-panel-head">
      <span class="practice-panel-kicker">Practice</span>
      <button class="compact-button" type="button" aria-label="Close practice panel" @click="emit('close')">
        <X :size="14" aria-hidden="true" />
      </button>
    </div>
    <div v-if="loading" class="practice-panel-state">Loading practice...</div>
    <div v-else-if="failed || !detail" class="practice-panel-state practice-panel-state-error">
      The practice is unavailable.
    </div>
    <template v-else>
      <h2 class="practice-panel-title">{{ detail.title }}</h2>
      <section class="practice-panel-section">
        <h3>Objective</h3>
        <p>{{ detail.objective }}</p>
      </section>
      <section class="practice-panel-section">
        <h3>Boundary</h3>
        <p>{{ detail.boundary }}</p>
      </section>
      <section v-if="detail.steps.length > 0" class="practice-panel-section">
        <button
          class="practice-panel-toggle"
          type="button"
          :aria-expanded="stepsOpen"
          @click="stepsOpen = !stepsOpen"
        >
          <component :is="stepsOpen ? ChevronDown : ChevronRight" :size="13" aria-hidden="true" />
          Steps
        </button>
        <ol v-show="stepsOpen" class="practice-panel-steps">
          <li v-for="(step, index) in detail.steps" :key="index">{{ step }}</li>
        </ol>
      </section>
      <section class="practice-panel-section">
        <button
          class="practice-panel-toggle"
          type="button"
          :aria-expanded="observationsOpen"
          @click="observationsOpen = !observationsOpen"
        >
          <component :is="observationsOpen ? ChevronDown : ChevronRight" :size="13" aria-hidden="true" />
          Observations
        </button>
        <ul v-show="observationsOpen" class="practice-panel-observations">
          <li v-for="(observation, index) in detail.observations" :key="index">{{ observation }}</li>
        </ul>
      </section>
    </template>
  </aside>
</template>
