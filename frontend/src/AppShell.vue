<script setup lang="ts">
import { ref } from "vue";
import AuthDialog from "./features/auth/AuthDialog.vue";
import AuthoringWorkspace from "./features/authoring/AuthoringWorkspace.vue";
import ChallengeCatalog from "./features/catalog/ChallengeCatalog.vue";
import ChallengeWorkspace from "./features/workspace/ChallengeWorkspace.vue";
import { useChallengeSession } from "./features/workspace/useChallengeSession";

const authOpen = ref(false);
const authMode = ref<"login" | "register">("login");
const authoringOpen = ref(false);
const notice = ref<{ text: string; kind: "error" | "info" } | null>(null);
let noticeTimer: number | undefined;

function notify(text: string, kind: "error" | "info" = "info") {
  notice.value = { text, kind };
  if (noticeTimer !== undefined) window.clearTimeout(noticeTimer);
  noticeTimer = window.setTimeout(() => {
    notice.value = null;
  }, 5000);
}

const {
  loggedIn,
  loading,
  challenges,
  selectedId,
  workspace,
  selected,
  loadChallenges,
  authenticated,
  logout,
  openWorkspace,
  closeWorkspace,
  selectChallenge,
} = useChallengeSession(notify);

function openAuth(mode: "login" | "register") {
  authMode.value = mode;
  authOpen.value = true;
}
async function startWorkspace() {
  await openWorkspace(() => openAuth("login"));
  if (workspace.value) notice.value = null;
}
function onPublished(id: string) {
  notify("Challenge published.");
  authoringOpen.value = false;
  void loadChallenges().then(() => {
    selectChallenge(id);
  });
}

function openAuthoring() {
  notice.value = null;
  authoringOpen.value = true;
}
</script>

<template>
  <div class="app-root">
    <div
      v-if="notice"
      class="toast"
      :class="[notice.kind, { 'workspace-toast': workspace }]"
    >
      {{ notice.text }}
    </div>
    <ChallengeWorkspace
      v-if="workspace"
      :challenge="workspace"
      @exit="closeWorkspace"
      @changed="loadChallenges(true)"
      @notice="notify"
    />
    <AuthoringWorkspace
      v-else-if="authoringOpen"
      @exit="authoringOpen = false"
      @published="onPublished"
    />
    <div v-else class="catalog-layout">
      <ChallengeCatalog
        :challenges="challenges"
        :selected-id="selectedId"
        :loading="loading"
        :logged-in="loggedIn"
        @select="selectChallenge"
        @login="openAuth('login')"
        @register="openAuth('register')"
        @logout="logout"
        @generate="openAuthoring"
      />
      <main class="catalog-detail">
        <template v-if="selected">
          <p class="eyebrow">{{ selected.runtime }} environment</p>
          <h1>{{ selected.title }}</h1>
          <p class="catalog-detail-description">{{ selected.description }}</p>
          <div class="catalog-detail-meta">
            <span :class="['difficulty', selected.difficulty]">{{
              selected.difficulty
            }}</span
            ><span v-for="tag in selected.tags" :key="tag">{{ tag }}</span>
          </div>
          <div class="catalog-detail-actions">
            <button
              class="primary-button"
              @click="startWorkspace"
            >
              {{
                loggedIn
                  ? selected.active
                    ? "Resume challenge"
                    : "Start challenge"
                  : "Sign in to start"
              }}
            </button>
            <p>
              {{
                loggedIn
                  ? selected.active
                    ? "Your existing environment will be reconnected."
                    : "A dedicated workspace will be prepared for this challenge."
                  : "Browse the task now, then sign in to create your own workspace."
              }}
            </p>
          </div>
        </template>
        <template v-else
          ><div class="catalog-empty">
            <p class="eyebrow">Challenge catalog</p>
            <h1>No challenges available</h1>
            <p>
              Generate a challenge from a concrete operational scenario.
            </p>
          </div></template
        >
      </main>
    </div>
    <AuthDialog
      :open="authOpen"
      :initial-mode="authMode"
      @close="authOpen = false"
      @authenticated="authenticated"
    />
  </div>
</template>
