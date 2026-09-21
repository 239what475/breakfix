<script setup lang="ts">
import { ref, watch } from "vue";
import {
	CircleDot,
	DoorOpen,
	KeyRound,
	Search,
	type LucideIcon,
} from "lucide-vue-next";
import { useAdminAudit } from "./admin";
import { relative } from "./format";
import { toActiveRef, toLoggedInRef } from "./refs";
import type { AdminHumanAction } from "../../api/generated";
import "./admin.css";

const props = defineProps<{ active: boolean; loggedIn: boolean; refreshRequest: number }>();
const { actions, nextCursor, loading, loadingMore, error, actionFilter, userFilter, refresh, loadMore } = useAdminAudit(
	toActiveRef(props),
	toLoggedInRef(props),
);

watch(
	() => props.refreshRequest,
	(request, previous) => {
		if (request !== previous && props.active && props.loggedIn) void refresh();
	},
);

const ACTION_ICONS: Record<string, { icon: LucideIcon; tone?: "red" | "amber" }> = {
	"user.totp.reset": { icon: KeyRound },
	"environment.release": { icon: DoorOpen },
};

const actionIcon = (action: string) => ACTION_ICONS[action] ?? { icon: CircleDot };

const actionInput = ref("");
const userInput = ref("");
const expandedId = ref<string>();

const detailOf = (action: AdminHumanAction) => JSON.stringify(action.detail, null, 2);
const hasDetail = (action: AdminHumanAction) => !!action.detail && Object.keys(action.detail).length > 0;

function applyFilters() {
	actionFilter.value = actionInput.value.trim();
	userFilter.value = userInput.value.trim();
	expandedId.value = undefined;
	void refresh();
}
</script>

<template>
	<section class="admin-section" aria-labelledby="admin-audit-title">
		<h2 id="admin-audit-title" class="admin-section-title">操作审计</h2>
		<form class="admin-audit-filters" @submit.prevent="applyFilters">
			<label><span>action</span><input v-model="actionInput" type="search" placeholder="environment.release" /></label>
			<label><span>user_id</span><input v-model="userInput" type="search" placeholder="u-..." /></label>
			<button class="compact-button" type="submit"><Search :size="13" aria-hidden="true" />过滤</button>
		</form>
		<p v-if="error" class="admin-error">{{ error }}</p>
		<p v-if="loading" class="admin-empty">Loading audit ledger...</p>
		<p v-else-if="!actions.length" class="admin-empty">暂无审计记录。管理动词执行后会在此出现。</p>
		<ul v-else class="admin-audit-list">
			<li v-for="action in actions" :key="action.id" class="admin-audit-item">
				<button
					class="admin-audit-toggle"
					type="button"
					:aria-expanded="expandedId === action.id"
					:disabled="!hasDetail(action)"
					@click="expandedId = expandedId === action.id ? undefined : action.id"
				>
					<span class="admin-history-icon" :class="actionIcon(action.action).tone"><component :is="actionIcon(action.action).icon" :size="18" aria-hidden="true" /></span>
					<span class="admin-audit-main">
						<strong class="admin-audit-action">{{ action.action }}</strong>
						<span class="admin-audit-target">{{ action.user_id }} → {{ action.target_id }}</span>
					</span>
					<span class="admin-history-time">{{ relative(action.created_at) }}</span>
				</button>
				<pre v-if="expandedId === action.id" class="admin-audit-detail">{{ detailOf(action) }}</pre>
			</li>
		</ul>
		<button v-if="nextCursor" class="text-button admin-more" type="button" :disabled="loadingMore" @click="loadMore">{{ loadingMore ? "Loading..." : "加载更多" }}</button>
	</section>
</template>
