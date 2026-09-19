import { ref, watch, type Ref } from "vue";
import { api, isLoggedIn, tokenUserRole } from "../../api/client";
import type {
	AdminDocumentationCorpusPage,
	AdminDocumentationWorkflow,
	AdminDocumentationWorkflowDetail,
	AdminDocumentBatch,
	AdminDocumentBatchItemsPage,
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
			await refreshDetail();
		} catch (cause) {
			if (ticket !== request) return;
			error.value = cause instanceof Error ? cause.message : "Unable to load documentation workflows";
		} finally {
			if (ticket === request) loading.value = false;
		}
	}

	// An expanded row must keep showing fresh evidence across list refreshes.
	async function refreshDetail() {
		if (!detail.value) return;
		try {
			detail.value = await api.getAdminWorkflow(detail.value.id);
		} catch {
			// A stale detail beats failing the whole list refresh.
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
			await refresh();
			await refreshDetail();
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

// The corpus section owns its fetches: the lazy tree, the server-side corpus
// projection, and the batch views all refresh on demand.
export type CorpusTreeSection = {
	path: string;
	title: string;
	hasChildren: boolean;
	children?: CorpusTreeSection[];
	loaded?: boolean;
};

export type CorpusWorkflowRow = {
	workflow: AdminDocumentationWorkflow;
	expanded: boolean;
	loadingDetail: boolean;
	detail?: AdminDocumentationWorkflowDetail;
};

export function useAdminCorpus(active: Ref<boolean>, runWorkflowAction: (kind: "force-fail" | "restart", id: string, reason: string) => Promise<boolean>) {
	const corpusRows = ref<AdminDocumentationCorpusPage["items"]>([]);
	const corpusCursor = ref<string>();
	const corpusLoading = ref(false);
	const corpusLoadingMore = ref(false);
	const corpusError = ref("");
	const sectionFilter = ref("");
	const searchInput = ref("");
	const searchFilter = ref("");
	const failuresOnly = ref(false);

	const batches = ref<AdminDocumentBatch[]>([]);
	const batchesLoading = ref(false);
	const batchesError = ref("");

	const selectedBatchId = ref("");
	const selectedBatch = ref<AdminDocumentBatch>();
	const batchItems = ref<AdminDocumentBatchItemsPage["items"]>([]);
	const batchItemsCursor = ref<string>();
	const batchItemsState = ref("");
	const batchLoading = ref(false);

	const creating = ref(false);
	const busy = ref(false);
	const createError = ref("");

	// Tree state: lazy sections loaded per path via the public tree endpoint.
	const treeRoots = ref<CorpusTreeSection[]>([]);
	const treeLoaded = ref(false);
	const checkedSections = ref<string[]>([]);

	let corpusRequest = 0;

	async function loadRootTree() {
		const tree = await api.getDocumentationTree();
		treeRoots.value = (tree?.nodes ?? []).map((child) => ({
			path: child.path,
			title: child.title,
			hasChildren: child.has_children,
		}));
		treeLoaded.value = true;
	}

	async function refreshCorpus(reset = true) {
		const ticket = ++corpusRequest;
		if (reset) {
			corpusCursor.value = undefined;
		}
		corpusLoading.value = true;
		corpusError.value = "";
		try {
			const page = await api.listAdminCorpus({
				section: sectionFilter.value || undefined,
				search: searchFilter.value || undefined,
				failures: failuresOnly.value || undefined,
				cursor: reset ? undefined : corpusCursor.value,
			});
			if (ticket !== corpusRequest) return;
			if (reset) corpusRows.value = page.items;
			else corpusRows.value = [...corpusRows.value, ...page.items];
			corpusCursor.value = page.next_cursor;
		} catch (cause) {
			if (ticket !== corpusRequest) return;
			corpusError.value = cause instanceof Error ? cause.message : "Unable to load the corpus";
		} finally {
			if (ticket === corpusRequest) corpusLoading.value = false;
		}
	}

	function loadMoreCorpus() {
		if (!corpusCursor.value) return;
		void refreshCorpus(false).then(() => {
			corpusLoadingMore.value = false;
		});
		corpusLoadingMore.value = true;
	}

	function applyCorpusFilters() {
		searchFilter.value = searchInput.value.trim();
		void refreshCorpus();
	}

	async function loadTreeSection(section: CorpusTreeSection) {
		if (!section.hasChildren || section.loaded) return;
		const tree = await api.getDocumentationTree(section.path);
		section.children = (tree?.nodes ?? []).map((child) => ({
			path: child.path,
			title: child.title,
			hasChildren: child.has_children,
		}));
		section.loaded = true;
	}

	function toggleSectionChecked(section: CorpusTreeSection) {
		const has = checkedSections.value.includes(section.path);
		if (has) {
			checkedSections.value = checkedSections.value.filter((path) => path !== section.path);
			return;
		}
		checkedSections.value = [...checkedSections.value, section.path];
		// Checking a node implies its subtree in the declared scope.
		void loadTreeSection(section).then(() => {
			const collect = (nodes: CorpusTreeSection[] | undefined): string[] =>
				(nodes ?? []).flatMap((node) => [node.path, ...collect(node.children)]);
			for (const path of collect(section.children)) {
				if (!checkedSections.value.includes(path)) {
					checkedSections.value = [...checkedSections.value, path];
				}
			}
		});
	}

	async function refreshBatches() {
		batchesLoading.value = true;
		batchesError.value = "";
		try {
			const page = await api.listAdminBatches();
			batches.value = page.batches;
			if (!selectedBatchId.value && page.batches.length) {
				await openBatch(page.batches[0].id);
			}
		} catch (cause) {
			batchesError.value = cause instanceof Error ? cause.message : "Unable to load batches";
		} finally {
			batchesLoading.value = false;
		}
	}

	async function openBatch(id: string) {
		selectedBatchId.value = id;
		selectedBatch.value = undefined;
		batchItems.value = [];
		batchItemsCursor.value = undefined;
		batchLoading.value = true;
		try {
			const [batch, items] = await Promise.all([
				api.getAdminBatch(id),
				api.listAdminBatchItems(id, { state: batchItemsState.value || undefined }),
			]);
			selectedBatch.value = batch;
			batchItems.value = items.items;
			batchItemsCursor.value = items.next_cursor;
		} catch (cause) {
			batchesError.value = cause instanceof Error ? cause.message : "Unable to load the batch";
		} finally {
			batchLoading.value = false;
		}
	}

	function filterBatchItems(state: string) {
		batchItemsState.value = batchItemsState.value === state ? "" : state;
		void openBatch(selectedBatchId.value);
	}

	async function loadMoreBatchItems() {
		if (!selectedBatchId.value || !batchItemsCursor.value) return;
		const page = await api.listAdminBatchItems(selectedBatchId.value, {
			state: batchItemsState.value || undefined,
			cursor: batchItemsCursor.value,
		});
		batchItems.value = [...batchItems.value, ...page.items];
		batchItemsCursor.value = page.next_cursor;
	}

	async function createBatch(scope: { kind: "full" | "sections" | "pages"; sections?: string[] }) {
		creating.value = true;
		createError.value = "";
		try {
			const batch = await api.createAdminBatch(scope);
			checkedSections.value = [];
			await refreshBatches();
			await openBatch(batch.id);
			return true;
		} catch (cause) {
			createError.value = cause instanceof Error ? cause.message : "Unable to create the batch";
			return false;
		} finally {
			creating.value = false;
		}
	}

	async function runBatchAction(kind: "pause" | "resume" | "cancel" | "retry-failed", id: string, reason: string): Promise<boolean> {
		busy.value = true;
		try {
			if (kind === "pause") await api.pauseAdminBatch(id, reason);
			else if (kind === "resume") await api.resumeAdminBatch(id, reason);
			else if (kind === "cancel") await api.cancelAdminBatch(id, reason);
			else await api.retryFailedAdminBatch(id, reason);
			await refreshBatches();
			if (selectedBatchId.value === id) await openBatch(id);
			return true;
		} catch (cause) {
			batchesError.value = cause instanceof Error ? cause.message : "Unable to run the batch action";
			return false;
		} finally {
			busy.value = false;
		}
	}

	// Per-page drill-down: the workflows bound to one corpus page row.
	const pageRows = ref<Record<string, CorpusWorkflowRow[]>>({});
	const pageRowLoading = ref<Record<string, boolean>>({});

	async function togglePageRow(pagePath: string) {
		if (pageRows.value[pagePath]) {
			const next = { ...pageRows.value };
			delete next[pagePath];
			pageRows.value = next;
			return;
		}
		pageRowLoading.value = { ...pageRowLoading.value, [pagePath]: true };
		try {
			const list = await api.listAdminWorkflows({ page_path: pagePath });
			pageRows.value = {
				...pageRows.value,
				[pagePath]: list.workflows.map((workflow) => ({ workflow, expanded: false, loadingDetail: false })),
			};
		} finally {
			pageRowLoading.value = { ...pageRowLoading.value, [pagePath]: false };
		}
	}

	async function expandWorkflow(workflow: CorpusWorkflowRow) {
		workflow.expanded = !workflow.expanded;
		if (!workflow.expanded || workflow.detail) return;
		workflow.loadingDetail = true;
		try {
			workflow.detail = await api.getAdminWorkflow(workflow.workflow.id);
		} finally {
			workflow.loadingDetail = false;
		}
	}

	async function refresh() {
		const jobs: Promise<void>[] = [refreshCorpus()];
		if (!treeLoaded.value) jobs.push(loadRootTree().catch(() => {}));
		jobs.push(refreshBatches());
		await Promise.allSettled(jobs);
	}

	watch(active, (isActive) => {
		if (isActive && !corpusRows.value.length && !corpusLoading.value) void refresh();
	});

	return {
		corpusRows, corpusCursor, corpusLoading, corpusLoadingMore, corpusError,
		sectionFilter, searchInput, searchFilter, failuresOnly,
		refreshCorpus, loadMoreCorpus, applyCorpusFilters,
		treeRoots, treeLoaded, checkedSections, loadTreeSection, toggleSectionChecked,
		batches, batchesLoading, batchesError, refreshBatches,
		selectedBatchId, selectedBatch, batchItems, batchItemsState, batchLoading, batchItemsCursor,
		openBatch, filterBatchItems, loadMoreBatchItems,
		creating, busy, createError, createBatch, runBatchAction,
		pageRows, pageRowLoading, togglePageRow, expandWorkflow,
		runWorkflowAction,
		refresh,
	};
}
