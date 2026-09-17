<script setup lang="ts">
import { computed, ref, watch } from "vue";
import AdminAuditPage from "./AdminAuditPage.vue";
import AdminUsersPage from "./AdminUsersPage.vue";
import AdminWorkflowsPage from "./AdminWorkflowsPage.vue";

const props = defineProps<{ active: boolean; loggedIn: boolean; section: "workflows" | "users" | "audit" }>();
const emit = defineEmits<{ navigate: [section: "workflows" | "users" | "audit"] }>();

const tab = ref<"workflows" | "users" | "audit">(props.section);

watch(
	() => props.section,
	(value) => {
		tab.value = value;
	},
);

const heading = computed(
	() => ({ workflows: "工作流观测与解卡", users: "账号管理", audit: "人操作审计" })[tab.value],
);
</script>

<template>
	<div class="admin-head">
		<p class="eyebrow">Admin console</p>
		<h1>管理控制面</h1>
		<p class="admin-head-sub">{{ heading }}。权限由后端 requireAdmin 保证,这里只做展示层。</p>
	</div>
	<nav class="admin-tabs" aria-label="Admin sections">
		<button :class="{ active: tab === 'workflows' }" type="button" @click="emit('navigate', 'workflows')">工作流</button>
		<button :class="{ active: tab === 'users' }" type="button" @click="emit('navigate', 'users')">用户</button>
		<button :class="{ active: tab === 'audit' }" type="button" @click="emit('navigate', 'audit')">审计</button>
	</nav>
	<AdminWorkflowsPage v-show="tab === 'workflows'" :active="props.active && tab === 'workflows'" />
	<AdminUsersPage v-show="tab === 'users'" :active="props.active && tab === 'users'" :logged-in="props.loggedIn" />
	<AdminAuditPage v-show="tab === 'audit'" :active="props.active && tab === 'audit'" :logged-in="props.loggedIn" />
</template>
