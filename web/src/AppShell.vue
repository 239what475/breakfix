<script setup lang="ts">
import { computed, ref } from "vue";
import AppTopbar from "./AppTopbar.vue";
import AuthDialog from "./features/auth/AuthDialog.vue";
import AuthoringWorkspace from "./features/authoring/AuthoringWorkspace.vue";
import ScenarioCatalogPage from "./features/catalog/ScenarioCatalogPage.vue";
import MySpacePage from "./features/my-space/MySpacePage.vue";
import ScenarioWorkspace from "./features/workspace/ScenarioWorkspace.vue";
import { useScenarioSession } from "./features/workspace/useScenarioSession";

const authOpen = ref(false);
const authMode = ref<"login" | "register">("login");
const authoringOpen = ref(false);
const authoringSessionId = ref<string>();
const page = ref<"operations" | "my-space">("operations");
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
	scenarios,
	workspace,
	startingId,
	loadScenarios,
	authenticated,
	logout,
	startScenario,
	closeWorkspace,
} = useScenarioSession(notify);

function openAuth(mode: "login" | "register") {
  authMode.value = mode;
  authOpen.value = true;
}
async function startWorkspace(id: string) {
	if (await startScenario(id, () => openAuth("login"))) notice.value = null;
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

function openOperations(scenarioId?: string) {
	catalogFocusId.value = scenarioId;
	page.value = "operations";
}

function openMySpace() {
	page.value = "my-space";
}

function navigateOperations() {
  closeAuthoring();
  closeWorkspace();
  openOperations();
}

function navigateMySpace() {
  const alreadyVisible = !workspace.value && !authoringOpen.value && page.value === "my-space";
  closeAuthoring();
  closeWorkspace();
  openMySpace();
  if (alreadyVisible) mySpaceRefreshRequest.value += 1;
}

function handleWorkspaceStopped() {
  closeWorkspace();
  notify("Environment stopped.", "info");
}

function signOut() {
	page.value = "operations";
	logout();
}

const topbarActive = computed<"operations" | "my-space" | "none">(() => {
  if (authoringOpen.value) return "none";
  if (workspace.value) return "none";
  return page.value;
});
</script>

<template>
  <div class="app-root">
    <AppTopbar :active="topbarActive" :show-navigation="loggedIn" :logged-in="loggedIn" :account-name="accountName" @operations="navigateOperations" @my-space="navigateMySpace" @login="openAuth('login')" @register="openAuth('register')" @logout="signOut" />
    <div
      v-if="notice"
      class="toast"
      :class="[notice.kind, { 'workspace-toast': workspace, 'my-space-toast': page === 'my-space', 'catalog-toast': !workspace && !authoringOpen && page === 'operations' }]"
    >
      {{ notice.text }}
    </div>
		<main class="app-main">
      <ScenarioCatalogPage
		v-show="!workspace && !authoringOpen && page === 'operations'"
      :scenarios="scenarios"
      :loading="loading"
      :logged-in="loggedIn"
      :starting-id="startingId"
			:focus-scenario-id="catalogFocusId"
      @start="startWorkspace"
		@studio="openAuthoring"
      @focused="catalogFocusId = undefined"
			/>
      <MySpacePage
		v-show="!workspace && !authoringOpen && page === 'my-space'"
		:active="!workspace && !authoringOpen && page === 'my-space'"
		:logged-in="loggedIn"
		:refresh-request="mySpaceRefreshRequest"
			@catalog="openOperations($event)"
		@start="startWorkspace"
		@studio="openAuthoring($event)"
			/>
      <ScenarioWorkspace v-if="workspace" :scenario="workspace" @changed="loadScenarios(true)" @stopped="handleWorkspaceStopped" @notice="notify" />
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
