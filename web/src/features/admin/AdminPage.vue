<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { AlertTriangle, BookOpen, RefreshCw, ScrollText, ShieldCheck, Users, Workflow } from "lucide-vue-next";
import AdminAuditPage from "./AdminAuditPage.vue";
import AdminCorpusPage from "./AdminCorpusPage.vue";
import AdminUsersPage from "./AdminUsersPage.vue";
import AdminWorkflowsPage from "./AdminWorkflowsPage.vue";
import { queueFlagCounts, useAdminCorpus, useAdminWorkflows } from "./admin";
import { toActiveRef } from "./refs";
import "./admin.css";

const props = defineProps<{ active: boolean; loggedIn: boolean; section: "workflows" | "users" | "audit" | "corpus" }>();
const emit = defineEmits<{ navigate: [section: "workflows" | "users" | "audit" | "corpus"] }>();

const tab = ref<"workflows" | "users" | "audit" | "corpus">(props.section);

watch(
	() => props.section,
	(value) => {
		tab.value = value;
	},
);

const heading = computed(() => ({ workflows: "工作流观测与解卡", users: "账号管理", audit: "人操作审计", corpus: "文档语料与批次" })[tab.value]);

// One shared fetch drives the sidebar queue cells, the stuck banner, and the
// workflows tab, so every section renders the same queue state.
const { workflows, detail, queue, loading, error, busy, refresh, openDetail, runAction } = useAdminWorkflows(toActiveRef(props));
const corpus = useAdminCorpus(toActiveRef(props), runAction);

const flagCounts = computed(() => (queue.value ? queueFlagCounts(queue.value.items) : {}));
const stuckWorkflows = computed(() => workflows.value.filter((workflow) => workflow.stuck.flag));

// Users and audit own their fetches; the header refresh nudges them through
// the same counter pattern AppShell uses for My space.
const refreshRequest = ref(0);

function refreshConsole() {
	if (tab.value === "workflows") void refresh();
	else if (tab.value === "corpus") void corpus.refresh();
	else refreshRequest.value += 1;
}

// The stuck banner hands the workflows tab a row to expand; the nonce keeps
// repeat jumps to the same workflow distinct.
const expandRequest = ref<{ id: string; nonce: number }>();
let expandNonce = 0;

function focusStuck(id: string) {
	expandRequest.value = { id, nonce: ++expandNonce };
	if (tab.value !== "workflows") emit("navigate", "workflows");
}

function closeDetail() {
	detail.value = undefined;
}
</script>

<template>
	<section class="admin-page">
		<div class="admin-layout">
			<aside class="admin-sidebar">
				<div class="admin-identity">
					<span class="admin-avatar"><ShieldCheck :size="17" aria-hidden="true" /></span>
					<div><strong>管理控制面</strong><small>role: admin</small></div>
				</div>
				<dl v-if="queue" class="admin-summary" aria-label="Runnable action queue">
					<div><dt>queued</dt><dd>{{ queue.summary.by_state["queued"] ?? 0 }}</dd></div>
					<div><dt>running</dt><dd>{{ queue.summary.by_state["running"] ?? 0 }}</dd></div>
					<div><dt>completed</dt><dd>{{ queue.summary.by_state["completed"] ?? 0 }}</dd></div>
					<div><dt>failed</dt><dd>{{ queue.summary.by_state["failed"] ?? 0 }}</dd></div>
				</dl>
				<dl v-if="Object.keys(flagCounts).length" class="admin-summary admin-summary-flags" aria-label="Queue flags">
					<div v-for="(count, flag) in flagCounts" :key="flag" :class="flag">
						<dt>{{ flag }}</dt>
						<dd>{{ count }}</dd>
					</div>
				</dl>
				<nav class="admin-tabs desktop-tabs" aria-label="Admin sections">
					<button :class="{ active: tab === 'workflows' }" type="button" @click="emit('navigate', 'workflows')"><Workflow :size="16" aria-hidden="true" />工作流</button>
					<button :class="{ active: tab === 'corpus' }" type="button" @click="emit('navigate', 'corpus')"><BookOpen :size="16" aria-hidden="true" />文档</button>
					<button :class="{ active: tab === 'users' }" type="button" @click="emit('navigate', 'users')"><Users :size="16" aria-hidden="true" />用户</button>
					<button :class="{ active: tab === 'audit' }" type="button" @click="emit('navigate', 'audit')"><ScrollText :size="16" aria-hidden="true" />审计</button>
				</nav>
			</aside>
			<main class="admin-content">
				<nav class="admin-tabs mobile-tabs" aria-label="Admin sections">
					<button :class="{ active: tab === 'workflows' }" type="button" @click="emit('navigate', 'workflows')">工作流</button>
					<button :class="{ active: tab === 'corpus' }" type="button" @click="emit('navigate', 'corpus')">文档</button>
					<button :class="{ active: tab === 'users' }" type="button" @click="emit('navigate', 'users')">用户</button>
					<button :class="{ active: tab === 'audit' }" type="button" @click="emit('navigate', 'audit')">审计</button>
				</nav>
				<header class="admin-content-header">
					<div><p class="eyebrow">Admin console</p><h1>{{ heading }}</h1></div>
					<div class="admin-content-actions">
						<button class="icon-button" type="button" :disabled="loading && tab === 'workflows'" title="Refresh" aria-label="Refresh" @click="refreshConsole"><RefreshCw :size="15" aria-hidden="true" /></button>
					</div>
				</header>
				<p v-if="error && tab === 'workflows'" class="admin-error">{{ error }}</p>
				<div v-for="stuck in stuckWorkflows" :key="stuck.id" class="admin-alert">
					<AlertTriangle :size="17" aria-hidden="true" />
					<span>工作流卡在 {{ stuck.state }} — {{ stuck.stuck.reason }}<template v-if="stuck.stuck.failure_code">({{ stuck.stuck.failure_class }}/{{ stuck.stuck.failure_code }})</template>,dwell {{ Math.round(stuck.dwell_seconds / 60) }}m</span>
					<button class="compact-button" type="button" @click="focusStuck(stuck.id)">查看</button>
				</div>
				<AdminWorkflowsPage
					v-show="tab === 'workflows'"
					:workflows="workflows"
					:detail="detail"
					:loading="loading"
					:busy="busy"
					:expand-request="expandRequest"
					:open-detail="openDetail"
					:close-detail="closeDetail"
					:run-action="runAction"
				/>
				<AdminCorpusPage v-show="tab === 'corpus'" :active="props.active && tab === 'corpus'" :refresh-request="refreshRequest" :corpus="corpus" :run-workflow-action="runAction" />
				<AdminUsersPage v-show="tab === 'users'" :active="props.active && tab === 'users'" :logged-in="props.loggedIn" :refresh-request="refreshRequest" />
				<AdminAuditPage v-show="tab === 'audit'" :active="props.active && tab === 'audit'" :logged-in="props.loggedIn" :refresh-request="refreshRequest" />
			</main>
		</div>
	</section>
</template>
