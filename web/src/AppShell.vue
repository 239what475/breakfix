<script setup lang="ts">
import { computed, defineAsyncComponent, onMounted, onUnmounted, ref, watch } from "vue";
import AppTopbar from "./AppTopbar.vue";
import AuthDialog from "./features/auth/AuthDialog.vue";
import AuthoringWorkspace from "./features/authoring/AuthoringWorkspace.vue";
import ScenarioCatalogPage from "./features/catalog/ScenarioCatalogPage.vue";
import MySpacePage from "./features/my-space/MySpacePage.vue";
import { documentationSource } from "./features/documentation/documentation";
import AdminPage from "./features/admin/AdminPage.vue";
import { isLoggedIn, tokenUserRole } from "./api/client";
import ScenarioWorkspace from "./features/workspace/ScenarioWorkspace.vue";
import { useScenarioSession } from "./features/workspace/useScenarioSession";

// The documentation reader (markdown-it + Shiki) loads as its own chunk so
// the primary bundle stays lean.
const DocumentationPage = defineAsyncComponent(() => import("./features/documentation/DocumentationPage.vue"));

const authOpen = ref(false);
const authMode = ref<"login" | "register">("login");
const authoringOpen = ref(false);
const authoringSessionId = ref<string>();
type AdminSection = "workflows" | "users" | "audit";

function adminSectionFromPath(): AdminSection {
	const match = window.location.pathname.match(/^\/admin\/(workflows|users|audit)$/);
	return (match?.[1] as AdminSection) ?? "workflows";
}

const page = ref<"operations" | "my-space" | "documentation" | "admin">(window.location.pathname.startsWith("/admin/") ? "admin" : window.location.pathname === "/documentation" ? "documentation" : "operations");
const adminSection = ref<AdminSection>(adminSectionFromPath());
// Display-layer guard: the admin surface only renders for the admin role.
// The backend independently enforces requireAdmin on every admin endpoint.
const isAdmin = ref(isLoggedIn() && tokenUserRole() === "admin");

watch(
	isAdmin,
	(admin) => {
		if (!admin && page.value === "admin") {
			page.value = "operations";
			window.history.pushState({}, "", "/");
		}
	},
	// Immediate covers the initial page load: a non-admin opening /admin/*
	// directly is redirected instead of rendering the console skeleton.
	{ immediate: true },
);
const mySpaceRefreshRequest = ref(0);
// Incremented on every successful login so mounted pages can resume an
// auth-gated flow (the reader's pending practice start).
const authSignal = ref(0);
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
	if (page.value === "documentation") window.history.pushState({}, "", "/");
	catalogFocusId.value = scenarioId;
	page.value = "operations";
}

function openMySpace() {
	if (page.value === "documentation") window.history.pushState({}, "", "/?page=my-space");
	page.value = "my-space";
}

function openAdmin(section: AdminSection = "workflows") {
	if (!isAdmin.value) return;
	closeAuthoring();
	closeWorkspace();
	adminSection.value = section;
	if (page.value !== "admin") window.history.pushState({}, "", `/admin/${section}`);
	page.value = "admin";
}

function openDocumentation() {
	closeAuthoring();
	closeWorkspace();
	if (page.value !== "documentation") {
		const params = new URLSearchParams({ source: documentationSource.source, version: documentationSource.version, path: documentationSource.entryPath });
		window.history.pushState({}, "", `/documentation?${params.toString()}`);
	}
	page.value = "documentation";
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
	isAdmin.value = false;
	logout();
}

function handleAuthenticated(name: string) {
	isAdmin.value = isLoggedIn() && tokenUserRole() === "admin";
	authSignal.value += 1;
	authenticated(name);
}

const topbarActive = computed<"operations" | "my-space" | "documentation" | "admin" | "none">(() => {
  if (authoringOpen.value) return "none";
  if (workspace.value) return "none";
	return page.value;
});

function handlePopState() {
  if (window.location.pathname === "/documentation") {
    page.value = "documentation";
    return;
  }
  if (window.location.pathname.startsWith("/admin/")) {
    if (!isAdmin.value) {
      page.value = "operations";
      return;
    }
    adminSection.value = adminSectionFromPath();
    page.value = "admin";
    return;
  }
  page.value = new URLSearchParams(window.location.search).get("page") === "my-space" ? "my-space" : "operations";
}

onMounted(() => window.addEventListener("popstate", handlePopState));
onUnmounted(() => window.removeEventListener("popstate", handlePopState));
</script>

<template>
  <div class="app-root">
    <AppTopbar :active="topbarActive" :show-navigation="true" :logged-in="loggedIn" :is-admin="isAdmin" :account-name="accountName" @operations="navigateOperations" @my-space="navigateMySpace" @documentation="openDocumentation" @admin="openAdmin()" @login="openAuth('login')" @register="openAuth('register')" @logout="signOut" />
    <div
      v-if="notice"
      class="toast"
      :class="[notice.kind, { 'workspace-toast': workspace, 'my-space-toast': page === 'my-space', 'catalog-toast': !workspace && !authoringOpen && page === 'operations' }]"
    >
      {{ notice.text }}
    </div>
		<main class="app-main">
      <DocumentationPage
        v-if="!workspace && !authoringOpen && page === 'documentation'"
        :auth-signal="authSignal"
        @request-auth="openAuth('login')"
        @notice="notify"
      />
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
      <AdminPage
        v-if="!workspace && !authoringOpen && page === 'admin'"
        :active="!workspace && !authoringOpen && page === 'admin'"
        :logged-in="loggedIn"
        :section="adminSection"
        @navigate="openAdmin($event)"
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
      @authenticated="handleAuthenticated"
    />
  </div>
</template>
