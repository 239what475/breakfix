<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, toRef, watch } from "vue";
import { BookOpen, LayoutDashboard, PenLine, RefreshCw, UserRound } from "lucide-vue-next";
import ActiveEnvironmentList from "./ActiveEnvironmentList.vue";
import AuthoringOverview from "./AuthoringOverview.vue";
import LearningHistory from "./LearningHistory.vue";
import MySpaceSummary from "./MySpaceSummary.vue";
import { useMySpace, type LearningRuntimeFilter, type LearningStateFilter } from "./useMySpace";
import { api } from "../../api/client";
import "./my-space.css";

const props = defineProps<{ active: boolean; loggedIn: boolean; refreshRequest: number }>();
const emit = defineEmits<{ catalog: [id?: string]; start: [id: string]; studio: [sessionId?: string] }>();
const active = toRef(props, "active");
const loggedIn = toRef(props, "loggedIn");
const tab = ref<"overview" | "learning" | "authoring">("overview");
const learningState = ref<LearningStateFilter>("all");
const learningRuntime = ref<LearningRuntimeFilter>("all");
const { space, history, nextCursor, loading, loadingLearning, loadingMore, error, refresh, loadMore } = useMySpace(active, loggedIn, learningState, learningRuntime);
const initials = computed(() => space.value?.profile.name.slice(0, 1).toUpperCase() || "?");
const heading = computed(() => ({ overview: "Your learning space", learning: "Learning history", authoring: "Scenario authoring" })[tab.value]);
const compactViewport = window.matchMedia("(max-width: 760px)");

async function reviseScenario(id: string) {
  try {
    const session = await api.createAuthoringScenarioRevision(id);
    emit("studio", session.id);
  } catch (cause) {
    error.value = cause instanceof Error ? cause.message : "Unable to start the scenario revision";
  }
}

async function deprecateScenario(id: string) {
  if (!window.confirm("Deprecate this scenario? Existing learning history will remain available.")) return;
  try {
    await api.deprecateAuthoringScenario(id);
    await refresh();
  } catch (cause) {
    error.value = cause instanceof Error ? cause.message : "Unable to deprecate the scenario";
  }
}

function syncCompactWorkspace() {
  if (compactViewport.matches && tab.value === "authoring") tab.value = "overview";
}

onMounted(() => {
  syncCompactWorkspace();
  compactViewport.addEventListener("change", syncCompactWorkspace);
});
onUnmounted(() => compactViewport.removeEventListener("change", syncCompactWorkspace));

watch(
  () => props.refreshRequest,
  (request, previous) => {
    if (request !== previous && active.value && loggedIn.value) void refresh();
  },
);
</script>

<template>
  <section class="my-space-page">
    <div class="my-space-layout">
      <aside class="space-sidebar">
        <div class="space-identity"><span class="space-avatar">{{ initials }}</span><div><strong>{{ space?.profile.name ?? "Your space" }}</strong><small v-if="space">Member since {{ new Date(space.profile.created_at).getFullYear() }}</small></div></div>
        <MySpaceSummary v-if="space" :summary="space.summary" />
        <nav class="space-tabs desktop-tabs" aria-label="My space views"><button :class="{ active: tab === 'overview' }" type="button" @click="tab = 'overview'"><LayoutDashboard :size="16" aria-hidden="true" />Overview</button><button :class="{ active: tab === 'learning' }" type="button" @click="tab = 'learning'"><BookOpen :size="16" aria-hidden="true" />Learning</button><button :class="{ active: tab === 'authoring' }" type="button" @click="tab = 'authoring'"><PenLine :size="16" aria-hidden="true" />Authoring</button></nav>
      </aside>
      <main class="space-content">
        <nav class="space-tabs mobile-tabs" aria-label="My space views"><button :class="{ active: tab === 'overview' }" type="button" @click="tab = 'overview'">Overview</button><button :class="{ active: tab === 'learning' }" type="button" @click="tab = 'learning'">Learning</button></nav>
        <header class="space-content-header"><div><p class="eyebrow">Account</p><h1>{{ heading }}</h1></div><div class="space-content-actions"><span v-if="space && tab === 'overview'" class="quota-copy">{{ space.summary.environment_quota.maximum === null ? `${space.summary.environment_quota.occupied} environments in use` : `${space.summary.environment_quota.occupied}/${space.summary.environment_quota.maximum} environments` }}</span><button class="icon-button refresh-space" type="button" :disabled="loading" title="Refresh My space" aria-label="Refresh My space" @click="refresh"><RefreshCw :size="15" aria-hidden="true" /></button></div></header>
        <p v-if="loading && !space" class="space-loading">Loading your learning space...</p>
        <div v-else-if="error && !space" class="space-error"><UserRound :size="19" aria-hidden="true" /><span>{{ error }}</span><button class="compact-button" type="button" @click="refresh">Retry</button></div>
        <template v-else-if="space">
          <p v-if="error" class="space-inline-error">{{ error }}</p>
          <template v-if="tab === 'overview'"><ActiveEnvironmentList :environments="space.active_environments" @start="emit('start', $event)" /><LearningHistory :items="space.recent_learning.slice(0, 5)" :loading="false" :loading-more="false" :has-more="false" /></template>
          <LearningHistory v-else-if="tab === 'learning'" :items="history" :loading="loadingLearning" :loading-more="loadingMore" :has-more="!!nextCursor" show-filters :state-filter="learningState" :runtime-filter="learningRuntime" @update:state-filter="learningState = $event" @update:runtime-filter="learningRuntime = $event" @more="loadMore" />
          <AuthoringOverview v-else :authoring="space.authoring" @authoring="emit('studio', $event)" @catalog="emit('catalog', $event)" @revision="reviseScenario" @deprecate="deprecateScenario" />
        </template>
      </main>
    </div>
  </section>
</template>
