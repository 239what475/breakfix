<script setup lang="ts">
import { ref } from "vue";
import AuthDialog from "./features/auth/AuthDialog.vue";
import AuthoringWorkspace from "./features/authoring/AuthoringWorkspace.vue";
import ChallengeCatalogPage from "./features/catalog/ChallengeCatalogPage.vue";
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
	workspace,
	startingId,
	loadChallenges,
	authenticated,
	logout,
	startChallenge,
	closeWorkspace,
} = useChallengeSession(notify);

function openAuth(mode: "login" | "register") {
  authMode.value = mode;
  authOpen.value = true;
}
async function startWorkspace(id: string) {
	if (await startChallenge(id, () => openAuth("login"))) notice.value = null;
}
function onPublished() {
	notify("Challenge published.");
	authoringOpen.value = false;
	void loadChallenges();
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
    <ChallengeCatalogPage
      v-show="!workspace && !authoringOpen"
      :challenges="challenges"
      :loading="loading"
      :logged-in="loggedIn"
      :starting-id="startingId"
      @start="startWorkspace"
      @login="openAuth('login')"
      @register="openAuth('register')"
      @logout="logout"
      @generate="openAuthoring"
    />
    <ChallengeWorkspace v-if="workspace" :challenge="workspace" @exit="closeWorkspace" @changed="loadChallenges(true)" @notice="notify" />
    <AuthoringWorkspace
      v-else-if="authoringOpen"
      @exit="authoringOpen = false"
      @published="onPublished"
    />
    <AuthDialog
      :open="authOpen"
      :initial-mode="authMode"
      @close="authOpen = false"
      @authenticated="authenticated"
    />
  </div>
</template>
