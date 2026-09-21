import { ref, watch, type Ref } from "vue";
import { api, isLoggedIn, tokenUserRole } from "../../api/client";
import type { AdminHumanAction, AdminUser } from "../../api/generated";

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
