<script setup lang="ts">
import {
	BookOpen,
	Check,
	ChevronLeft,
	ChevronRight,
	CircleDashed,
	FileText,
	Info,
	MessageCircle,
	RefreshCw,
} from "lucide-vue-next";
import type { Checkpoint, CheckpointResult } from "../../api/types";

defineProps<{
  view: "overview" | "problem" | "solution" | "assistant";
  hasProblem: boolean;
  hasSolution: boolean;
  checkpoints: Checkpoint[];
  results: CheckpointResult[];
  collapsed: boolean;
  error: string;
}>();
const emit = defineEmits<{
  updateView: [view: "overview" | "problem" | "solution" | "assistant"];
  toggle: [];
  refresh: [];
  hint: [id: string];
}>();

function firstPassedLabel(value?: string | null) {
  if (!value) return "";
  return "First passed " + new Intl.DateTimeFormat(undefined, { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" }).format(new Date(value));
}
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
          :class="{ active: view === 'overview' }"
          @click="emit('updateView', 'overview')"
        >
          Overview
        </button>
        <button
          v-if="hasProblem"
          :class="{ active: view === 'problem' }"
          @click="emit('updateView', 'problem')"
        >
          Problem
        </button>
        <button
          v-if="hasSolution"
          :class="{ active: view === 'solution' }"
          @click="emit('updateView', 'solution')"
        >
          Solution
        </button>
        <button
          :class="{ active: view === 'assistant' }"
          @click="emit('updateView', 'assistant')"
        >
          Assistant
        </button>
      </div>
      <template v-if="checkpoints.length">
        <div class="checkpoint-heading">
          <strong>Checkpoints</strong>
          <button
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
              <span>
                <strong>{{ checkpoint.title }}</strong>
                <small>{{
                  results.find((result) => result.id === checkpoint.id)
                    ?.summary || checkpoint.description
                }}</small>
                <time
                  v-if="results.find((result) => result.id === checkpoint.id)?.first_passed_at"
                  class="checkpoint-first-passed"
                  :datetime="results.find((result) => result.id === checkpoint.id)?.first_passed_at ?? undefined"
                >{{ firstPassedLabel(results.find((result) => result.id === checkpoint.id)?.first_passed_at) }}</time>
              </span>
            </button>
          </li>
        </ol>
      </template>
    </template>
    <nav v-else class="sidebar-rail" aria-label="Workspace navigation">
      <button
        class="icon-button"
        :class="{ active: view === 'overview' }"
        aria-label="Show overview"
        title="Show overview"
        @click="emit('updateView', 'overview')"
      >
        <Info :size="16" aria-hidden="true" />
      </button>
      <button
        v-if="hasProblem"
        class="icon-button"
        :class="{ active: view === 'problem' }"
        aria-label="Show Problem"
        title="Show Problem"
        @click="emit('updateView', 'problem')"
      >
        <FileText :size="16" aria-hidden="true" />
      </button>
      <button
        v-if="hasSolution"
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
        :class="{ active: view === 'assistant' }"
        aria-label="Show Assistant"
        title="Show Assistant"
        @click="emit('updateView', 'assistant')"
      >
        <MessageCircle :size="16" aria-hidden="true" />
      </button>
    </nav>
  </aside>
</template>
