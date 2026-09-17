<script setup lang="ts">
import { ref } from "vue";
import { RefreshCw, Search } from "lucide-vue-next";
import { useAdminAudit } from "./admin";
import { toActiveRef, toLoggedInRef } from "./refs";
import "./admin.css";

const props = defineProps<{ active: boolean; loggedIn: boolean }>();
const { actions, nextCursor, loading, loadingMore, error, actionFilter, userFilter, refresh, loadMore } = useAdminAudit(
	toActiveRef(props),
	toLoggedInRef(props),
);

const actionInput = ref("");
const userInput = ref("");

function applyFilters() {
	actionFilter.value = actionInput.value.trim();
	userFilter.value = userInput.value.trim();
	void refresh();
}

const clock = (value: string) => new Date(value).toLocaleString();
const detail = (value: unknown) => JSON.stringify(value);
</script>

<template>
	<section class="admin-section" aria-labelledby="admin-audit-title">
		<div class="space-section-heading">
			<div><p class="eyebrow">Human action ledger</p><h2 id="admin-audit-title">操作审计</h2></div>
			<button class="icon-button" type="button" title="Refresh" aria-label="Refresh" @click="refresh()"><RefreshCw :size="15" aria-hidden="true" /></button>
		</div>
		<form class="admin-audit-filters" @submit.prevent="applyFilters">
			<label><span>action</span><input v-model="actionInput" type="search" placeholder="documentation.workflow.force_fail" /></label>
			<label><span>user_id</span><input v-model="userInput" type="search" placeholder="u-..." /></label>
			<button class="compact-button" type="submit"><Search :size="13" aria-hidden="true" />过滤</button>
		</form>
		<p v-if="error" class="admin-error">{{ error }}</p>
		<p v-if="loading" class="space-empty">Loading audit ledger...</p>
		<p v-else-if="!actions.length" class="space-empty">暂无审计记录。管理动词执行后会在此出现。</p>
		<div v-else class="admin-workflow-list">
			<article v-for="action in actions" :key="action.id" class="admin-workflow-row">
				<div class="admin-workflow-main">
					<div class="admin-workflow-title">
						<h3><code>{{ action.action }}</code></h3>
						<span class="workflow-state state-planning">{{ action.target_type }}</span>
					</div>
					<p>actor {{ action.user_id }} → {{ action.target_id }} · {{ clock(action.created_at) }}</p>
					<p class="admin-audit-detail"><code>{{ detail(action.detail) }}</code></p>
				</div>
			</article>
		</div>
		<button v-if="nextCursor" class="text-button history-more" type="button" :disabled="loadingMore" @click="loadMore">{{ loadingMore ? "Loading..." : "加载更多" }}</button>
	</section>
</template>
