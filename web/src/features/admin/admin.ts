import { ref, watch, type Ref } from "vue";
import { api, isLoggedIn, tokenUserRole } from "../../api/client";
import type {
	AdminDocumentationWorkflow,
	AdminDocumentationWorkflowDetail,
	AdminHumanAction,
	AdminRunnableActionItem,
	AdminRunnableActionPage,
	AdminUser,
} from "../../api/generated";

// useAdminAuthorization guards the admin surface at the display layer only.
// The backend independently rejects non-admin calls with 403.
export function useAdminAuthorization(active: Ref<boolean>, loggedIn: Ref<boolean>) {
	const isAdministrator = ref(false);

	function refresh() {
		isAdministrator.value = isLoggedIn() && tokenUserRole() === "admin";
	}

	watch([active, loggedIn], ([activeNow, loggedInNow]) => {
		if (!activeNow || !loggedInNow) return;
		refresh();
	}, { immediate: true });

	refresh();
	return { isAdministrator, refresh };
}

export function useAdminWorkflows(active: Ref<boolean>) {
	const workflows = ref<AdminDocumentationWorkflow[]>([]);
	const detail = ref<AdminDocumentationWorkflowDetail>();
	const queue = ref<AdminRunnableActionPage>();
	const loading = ref(false);
	const error = ref("");
	const busy = ref(false);
	let request = 0;

	async function refresh() {
		const ticket = ++request;
		loading.value = true;
		error.value = "";
		try {
			const [list, queuePage] = await Promise.all([
				api.listAdminWorkflows(),
				api.listAdminRunnableActions(),
			]);
			if (ticket !== request) return;
			workflows.value = list.workflows;
			queue.value = queuePage;
		} catch (cause) {
			if (ticket !== request) return;
			error.value = cause instanceof Error ? cause.message : "Unable to load documentation workflows";
		} finally {
			if (ticket === request) loading.value = false;
		}
	}

	async function openDetail(id: string) {
		error.value = "";
		try {
			detail.value = await api.getAdminWorkflow(id);
		} catch (cause) {
			error.value = cause instanceof Error ? cause.message : "Unable to load the workflow detail";
		}
	}

	async function runAction(kind: "force-fail" | "restart", id: string, reason: string): Promise<boolean> {
		busy.value = true;
		error.value = "";
		try {
			if (kind === "force-fail") await api.forceFailAdminWorkflow(id, reason);
			else await api.restartAdminWorkflow(id, reason);
			detail.value = undefined;
			await refresh();
			return true;
		} catch (cause) {
			error.value = cause instanceof Error ? cause.message : "The workflow action failed";
			return false;
		} finally {
			busy.value = false;
		}
	}

	watch(active, (activeNow) => {
		if (activeNow) void refresh();
	}, { immediate: true });

	return { workflows, detail, queue, loading, error, busy, refresh, openDetail, runAction };
}

export function useAdminUsers(active: Ref<boolean>, loggedIn: Ref<boolean>) {
	const users = ref<AdminUser[]>([]);
	const loading = ref(false);
	const error = ref("");
	let request = 0;

	async function refresh() {
		const ticket = ++request;
		loading.value = true;
		error.value = "";
		try {
			const page = await api.listAdminUsers();
			if (ticket !== request) return;
			users.value = page.users;
		} catch (cause) {
			if (ticket !== request) return;
			error.value = cause instanceof Error ? cause.message : "Unable to load user accounts";
		} finally {
			if (ticket === request) loading.value = false;
		}
	}

	watch([active, loggedIn], ([activeNow, loggedInNow]) => {
		if (activeNow && loggedInNow) void refresh();
	}, { immediate: true });

	return { users, loading, error, refresh };
}

export function useAdminAudit(active: Ref<boolean>, loggedIn: Ref<boolean>) {
	const actions = ref<AdminHumanAction[]>([]);
	const nextCursor = ref<string>();
	const loading = ref(false);
	const loadingMore = ref(false);
	const error = ref("");
	const actionFilter = ref("");
	const userFilter = ref("");
	let request = 0;

	async function loadMore() {
		const ticket = ++request;
		if (!nextCursor.value) return;
		loadingMore.value = true;
		error.value = "";
		try {
			const page = await api.listAdminAudit({
				cursor: nextCursor.value,
				action: actionFilter.value || undefined,
				user_id: userFilter.value || undefined,
			});
			if (ticket !== request) return;
			actions.value = [...actions.value, ...page.items];
			nextCursor.value = page.next_cursor;
		} catch (cause) {
			if (ticket !== request) return;
			error.value = cause instanceof Error ? cause.message : "Unable to load the audit ledger";
		} finally {
			if (ticket === request) loadingMore.value = false;
		}
	}

	async function refresh() {
		const ticket = ++request;
		loading.value = true;
		error.value = "";
		try {
			const page = await api.listAdminAudit({
				action: actionFilter.value || undefined,
				user_id: userFilter.value || undefined,
			});
			if (ticket !== request) return;
			actions.value = page.items;
			nextCursor.value = page.next_cursor;
		} catch (cause) {
			if (ticket !== request) return;
			error.value = cause instanceof Error ? cause.message : "Unable to load the audit ledger";
		} finally {
			if (ticket === request) loading.value = false;
		}
	}

	watch([active, loggedIn], ([activeNow, loggedInNow]) => {
		if (activeNow && loggedInNow) void refresh();
	}, { immediate: true });

	return { actions, nextCursor, loading, loadingMore, error, actionFilter, userFilter, refresh, loadMore };
}

export function queueFlagCounts(items: AdminRunnableActionItem[]): Record<string, number> {
	const counts: Record<string, number> = {};
	for (const item of items) {
		if (!item.flag) continue;
		counts[item.flag] = (counts[item.flag] ?? 0) + 1;
	}
	return counts;
}
