<script setup lang="ts">
import { ChevronDown, ChevronRight, X } from "lucide-vue-next";
import { computed, ref } from "vue";
import type { DocumentationPracticeDetail } from "../../api/generated";
import TerminalPane from "../workspace/TerminalPane.vue";
import { practiceTerminalChannel } from "../workspace/useTerminalSession";

const props = defineProps<{
  practiceId: string;
  detail: DocumentationPracticeDetail | null;
  loading: boolean;
  failed: boolean;
  /** The environment session for this practice. */
  sessionActive: boolean;
  starting: boolean;
  stopping: boolean;
  resetting: boolean;
  ready: boolean;
  phase: string;
  runtime: "node" | "k8s";
  nodes: { name: string; title: string }[];
}>();

const emit = defineEmits<{ close: []; start: []; stop: []; reset: [] }>();

const stepsOpen = ref(true);
const observationsOpen = ref(true);

const channel = computed(() => practiceTerminalChannel(props.practiceId));

function phaseLabel() {
  if (!props.starting) return "";
  if (props.phase === "Provisioning") return "Provisioning the environment...";
  if (props.phase === "Ready") return "Environment is almost ready...";
  return "Preparing the environment...";
}
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

      <!-- The temporary environment lives below the guidance: start it on
           demand, watch it prepare, and work in the terminal once Ready. -->
      <div class="practice-panel-session">
        <div v-if="starting" class="practice-panel-state" role="status">{{ phaseLabel() }}</div>
        <template v-if="ready">
          <div class="practice-panel-session-actions">
            <button class="compact-button" type="button" :disabled="resetting" @click="emit('reset')">
              {{ resetting ? "Resetting..." : "Reset" }}
            </button>
            <button class="compact-button danger-button" type="button" :disabled="stopping" @click="emit('stop')">
              {{ stopping ? "Stopping..." : "Stop" }}
            </button>
          </div>
          <TerminalPane
            :terminal-id="practiceId"
            :runtime="runtime"
            :nodes="nodes"
            :visible="true"
            :channel="channel"
            class="practice-panel-terminal"
          />
        </template>
        <button
          v-else-if="!sessionActive && !starting"
          class="primary-button practice-panel-start"
          type="button"
          @click="emit('start')"
        >
          Start practice
        </button>
      </div>
    </template>
  </aside>
</template>
