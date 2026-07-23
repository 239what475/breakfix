<script setup lang="ts">
import {
	BookOpen,
	Check,
	ChevronLeft,
	ChevronRight,
	CircleDashed,
	FileText,
	ListChecks,
	RefreshCw,
} from "lucide-vue-next";
import type { Checkpoint, CheckpointResult } from "../../api/types";

defineProps<{
  view: "problem" | "solution";
  checkpoints: Checkpoint[];
  results: CheckpointResult[];
  collapsed: boolean;
  error: string;
}>();
const emit = defineEmits<{
  updateView: [view: "problem" | "solution"];
  toggle: [];
  refresh: [];
  hint: [id: string];
}>();
</script>

<template>
  <aside class="workspace-sidebar" :class="{ collapsed }">
    <button
      class="icon-button sidebar-toggle"
      :title="collapsed ? 'Expand sidebar' : 'Collapse sidebar'"
      @click="emit('toggle')"
    >
      <ChevronRight v-if="collapsed" :size="16" aria-hidden="true" />
      <ChevronLeft v-else :size="16" aria-hidden="true" />
    </button>
    <template v-if="!collapsed">
      <div class="document-tabs">
        <button
          :class="{ active: view === 'problem' }"
          @click="emit('updateView', 'problem')"
        >
          Problem</button
        ><button
          :class="{ active: view === 'solution' }"
          @click="emit('updateView', 'solution')"
        >
          Solution
        </button>
      </div>
      <div class="checkpoint-heading">
        <strong>Checkpoints</strong
        ><button
          class="icon-button"
          title="Refresh checkpoints"
          @click="emit('refresh')"
        >
          <RefreshCw :size="14" aria-hidden="true" />
        </button>
      </div>
      <p v-if="error" class="checkpoint-error">{{ error }}</p>
      <ol class="checkpoint-list">
        <li
          v-for="checkpoint in checkpoints"
          :key="checkpoint.id"
          :class="{
            passed: results.find((result) => result.id === checkpoint.id)
              ?.passed,
          }"
        >
          <button
            class="checkpoint-button"
            @click="checkpoint.hint && emit('hint', checkpoint.id)"
          >
            <span class="checkpoint-mark">
              <Check
                v-if="results.find((result) => result.id === checkpoint.id)?.passed"
                :size="12"
                aria-hidden="true"
              />
              <CircleDashed v-else :size="12" aria-hidden="true" />
            </span>
            <span
              ><strong>{{ checkpoint.title }}</strong
              ><small>{{
                results.find((result) => result.id === checkpoint.id)
                  ?.summary || checkpoint.description
              }}</small></span
            >
          </button>
        </li>
      </ol>
    </template>
    <nav v-else class="sidebar-rail" aria-label="Workspace navigation">
      <button
        class="icon-button"
        :class="{ active: view === 'problem' }"
        aria-label="Show Problem"
        title="Show Problem"
        @click="emit('updateView', 'problem')"
      >
        <FileText :size="16" aria-hidden="true" />
      </button>
      <button
        class="icon-button"
        :class="{ active: view === 'solution' }"
        aria-label="Show Solution"
        title="Show Solution"
        @click="emit('updateView', 'solution')"
      >
        <BookOpen :size="16" aria-hidden="true" />
      </button>
      <button
        class="icon-button"
        aria-label="Expand checkpoints"
        title="Expand checkpoints"
        @click="emit('toggle')"
      >
        <ListChecks :size="16" aria-hidden="true" />
      </button>
    </nav>
  </aside>
</template>
