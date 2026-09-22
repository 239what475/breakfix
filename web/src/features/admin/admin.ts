import { ref, watch, type Ref } from "vue";
import { api, isLoggedIn, tokenUserRole } from "../../api/client";
import type {
	AdminEnvironment,
	AdminHumanAction,
	AdminRunnableActionItem,
	AdminRunnableActionSummary,
	AdminRunnableReap,
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

// useAdminEnvironments owns the environment observation page: one environment
// list, the runnable action queue and the reap queue behind it, and the
// configured playground cap the overview card reads from the system status.
// The release verb rides the existing drain path and only nudges the
// controller.
export function useAdminEnvironments(active: Ref<boolean>, loggedIn: Ref<boolean>) {
	const environments = ref<AdminEnvironment[]>([]);
	const reaps = ref<AdminRunnableReap[]>([]);
	const actions = ref<AdminRunnableActionItem[]>([]);
	const actionSummary = ref<AdminRunnableActionSummary>({ by_state: {}, by_attempt: {} });
	const playgroundMaxActive = ref<number | null>(null);
	const loading = ref(false);
	const error = ref("");
	const releasing = ref("");
	let request = 0;

	async function refresh() {
		const ticket = ++request;
		loading.value = true;
		error.value = "";
		try {
			const [list, reapList, actionPage, system] = await Promise.all([
				api.listAdminEnvironments(),
				api.listAdminRunnableReaps(),
				api.listAdminRunnableActions(),
				api.getAdminSystem(),
			]);
			if (ticket !== request) return;
			environments.value = list.environments;
			reaps.value = reapList.reaps;
			actions.value = actionPage.items;
			actionSummary.value = actionPage.summary;
			playgroundMaxActive.value = system.playground_max_active;
		} catch (cause) {
			if (ticket !== request) return;
			error.value = cause instanceof Error ? cause.message : "Unable to load environments";
		} finally {
			if (ticket === request) loading.value = false;
		}
	}

	async function release(name: string) {
		releasing.value = name;
		error.value = "";
		try {
			await api.releaseAdminEnvironment(name);
		} catch (cause) {
			error.value = cause instanceof Error ? cause.message : "Unable to request the release";
			return false;
		} finally {
			releasing.value = "";
		}
		await refresh();
		return true;
	}

	watch([active, loggedIn], ([activeNow, loggedInNow]) => {
		if (activeNow && loggedInNow) void refresh();
	}, { immediate: true });

	return { environments, reaps, actions, actionSummary, playgroundMaxActive, loading, error, releasing, refresh, release };
}

// A Draining or Failed environment whose reference timestamp (failure time,
// else the expired deadline, else creation) is older than ten minutes is
// flagged as stuck. It is a pure UI heuristic: no alerting and no automatic
// action — the operator decides, and disposal still goes through the audited
// release path.
export const stuckThresholdMs = 10 * 60 * 1000;

export function isStuckEnvironment(environment: AdminEnvironment, now = Date.now()) {
	if (environment.phase !== "Draining" && environment.phase !== "Failed") return false;
	let reference: string;
	if (environment.phase === "Failed" && environment.failure) {
		reference = environment.failure.at;
	} else if (environment.expires_at && new Date(environment.expires_at).getTime() <= now) {
		// Draining usually starts at the deadline; before any deadline exists,
		// creation time is the only lower bound (an overestimate).
		reference = environment.expires_at;
	} else {
		reference = environment.created_at;
	}
	return now - new Date(reference).getTime() > stuckThresholdMs;
}
