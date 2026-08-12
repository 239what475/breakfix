<script setup lang="ts">
import { computed, ref } from "vue";
import AppTopbar from "./AppTopbar.vue";
import AuthDialog from "./features/auth/AuthDialog.vue";
import AuthoringWorkspace from "./features/authoring/AuthoringWorkspace.vue";
import ChallengeCatalogPage from "./features/catalog/ChallengeCatalogPage.vue";
import MySpacePage from "./features/my-space/MySpacePage.vue";
import ChallengeWorkspace from "./features/workspace/ChallengeWorkspace.vue";
import { useChallengeSession } from "./features/workspace/useChallengeSession";

const authOpen = ref(false);
const authMode = ref<"login" | "register">("login");
const authoringOpen = ref(false);
const authoringSessionId = ref<string>();
const page = ref<"catalog" | "my-space">("catalog");
const mySpaceRefreshRequest = ref(0);
const catalogFocusId = ref<string>();
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
	accountName,
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
function openAuthoring(sessionId?: string) {
  notice.value = null;
  authoringSessionId.value = sessionId;
  authoringOpen.value = true;
}

function closeAuthoring() {
  authoringOpen.value = false;
  authoringSessionId.value = undefined;
}

function openCatalog(challengeId?: string) {
	catalogFocusId.value = challengeId;
	page.value = "catalog";
}

function openMySpace() {
	page.value = "my-space";
}

function navigateCatalog() {
  closeAuthoring();
  closeWorkspace();
  openCatalog();
}

function navigateMySpace() {
  const alreadyVisible = !workspace.value && !authoringOpen.value && page.value === "my-space";
  closeAuthoring();
  closeWorkspace();
  openMySpace();
  if (alreadyVisible) mySpaceRefreshRequest.value += 1;
}

function navigateStudio() {
  if (authoringOpen.value) return;
  closeWorkspace();
  openAuthoring();
}

function signOut() {
	page.value = "catalog";
	logout();
}

const topbarActive = computed<"catalog" | "my-space" | "studio" | "none">(() => {
  if (authoringOpen.value) return "studio";
  if (workspace.value) return "none";
  return page.value;
});
</script>

<template>
  <div class="app-root">
    <AppTopbar :active="topbarActive" :show-navigation="loggedIn" :logged-in="loggedIn" :account-name="accountName" @catalog="navigateCatalog" @my-space="navigateMySpace" @studio="navigateStudio" @login="openAuth('login')" @register="openAuth('register')" @logout="signOut" />
    <div
      v-if="notice"
      class="toast"
      :class="[notice.kind, { 'workspace-toast': workspace, 'my-space-toast': page === 'my-space', 'catalog-toast': !workspace && !authoringOpen && page === 'catalog' }]"
    >
      {{ notice.text }}
    </div>
		<main class="app-main">
      <ChallengeCatalogPage
		v-show="!workspace && !authoringOpen && page === 'catalog'"
      :challenges="challenges"
      :loading="loading"
      :logged-in="loggedIn"
      :starting-id="startingId"
			:focus-challenge-id="catalogFocusId"
      @start="startWorkspace"
		@studio="openAuthoring"
      @focused="catalogFocusId = undefined"
			/>
      <MySpacePage
		v-show="!workspace && !authoringOpen && page === 'my-space'"
		:active="!workspace && !authoringOpen && page === 'my-space'"
		:logged-in="loggedIn"
		:refresh-request="mySpaceRefreshRequest"
			@catalog="openCatalog($event)"
		@start="startWorkspace"
		@studio="openAuthoring($event)"
			/>
      <ChallengeWorkspace v-if="workspace" :challenge="workspace" @changed="loadChallenges(true)" @notice="notify" />
      <AuthoringWorkspace
      v-else-if="authoringOpen"
      :initial-session-id="authoringSessionId"
      />
		</main>
    <AuthDialog
      :open="authOpen"
      :initial-mode="authMode"
      @close="authOpen = false"
      @authenticated="authenticated"
    />
  </div>
</template>
