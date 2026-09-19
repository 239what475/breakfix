import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { effectScope, ref, type Ref } from "vue";
import { api } from "../../api/client";
import type {
	AdminDocumentBatch,
	AdminDocumentBatchItem,
	AdminDocumentBatchItemsPage,
	AdminDocumentBatchList,
	AdminDocumentationCorpusPage,
	DocumentationTreeResponse,
} from "../../api/generated";
import { automockApi } from "../../test/client-mock";
import AdminCorpusPage from "./AdminCorpusPage.vue";
import { useAdminCorpus } from "./admin";

vi.mock("../../api/client", async (importOriginal) => {
	const { automockApi } = await import("../../test/client-mock");
	const actual = await importOriginal<typeof import("../../api/client")>();
	return { ...actual, api: automockApi(actual.api) };
});

const rootTree: DocumentationTreeResponse = {
	nodes: [
		{ title: "Concepts", path: "/docs/concepts", has_children: true },
		{ title: "Tasks", path: "/docs/tasks", has_children: false },
	],
};

const sectionTree: DocumentationTreeResponse = {
	nodes: [{ title: "Workloads", path: "/docs/concepts/workloads", has_children: true }],
};

const corpusPage = (over: Partial<AdminDocumentationCorpusPage> = {}): AdminDocumentationCorpusPage => ({
	items: [
		{
			page_path: "docs/concepts/workloads/pods/pod-lifecycle",
			title: "Pod Lifecycle",
			total: 3,
			published: 2,
			failed: 1,
			no_practice: 0,
			in_progress: 0,
			stuck: 0,
		},
	],
	...over,
});

const batch = (over: Partial<AdminDocumentBatch> = {}): AdminDocumentBatch => ({
	id: "document-batch-rolled-out",
	state: "Completed",
	scope: { kind: "pages", pages: ["docs/concepts/workloads/pods/pod-lifecycle"] },
	concurrency: 2,
	resolution: { resolved_pages: 2 },
	total_items: 2,
	counts: { Published: 1, Skipped: 1 },
	created_by: "admin",
	created_at: "2026-09-19T08:00:00Z",
	updated_at: "2026-09-19T08:05:00Z",
	...over,
});

const batchItem = (state: AdminDocumentBatchItem["state"]): AdminDocumentBatchItem => ({
	id: `document-batch-item-${state.toLowerCase()}`,
	batch_id: "document-batch-rolled-out",
	ordinal: 1,
	page_path: "docs/concepts/workloads/pods/pod-lifecycle",
	anchor: "pod-lifetime",
	title: "Pod Lifecycle",
	workflow_id: "document-workflow-item",
	state,
	created_at: "2026-09-19T08:00:00Z",
	updated_at: "2026-09-19T08:05:00Z",
});

describe("AdminCorpusPage", () => {
	let scope: ReturnType<typeof effectScope> | undefined;
	let wrapper: VueWrapper | undefined;

	function mountCorpus(active: Ref<boolean>) {
		const runWorkflowAction = vi.fn().mockResolvedValue(true);
		const corpus = scope!.run(() => useAdminCorpus(active, runWorkflowAction))!;
		wrapper = mount(AdminCorpusPage, {
			props: { active: active.value, refreshRequest: 0, corpus, runWorkflowAction },
		});
		return corpus;
	}

	beforeEach(() => {
		vi.clearAllMocks();
		vi.mocked(api.getDocumentationTree).mockImplementation(async (path?: string) =>
			path ? sectionTree : rootTree,
		);
		vi.mocked(api.listAdminCorpus).mockResolvedValue(corpusPage());
		vi.mocked(api.listAdminBatches).mockResolvedValue({ batches: [] } satisfies AdminDocumentBatchList);
	});

	afterEach(() => {
		wrapper?.unmount();
		scope?.stop();
	});

	it("loads the corpus as soon as the section mounts already active", async () => {
		// Regression guard: the corpus watch must fire immediately, not only on
		// the next activation flip - a route that renders the section directly
		// used to show an empty tree until the admin switched tabs.
		scope = effectScope();
		mountCorpus(ref(true));
		await flushPromises();

		expect(api.listAdminCorpus).toHaveBeenCalledTimes(1);
		expect(api.getDocumentationTree).toHaveBeenCalledTimes(1);
		expect(api.listAdminBatches).toHaveBeenCalledTimes(1);
		expect(wrapper!.text()).toContain("Pod Lifecycle");
		expect(wrapper!.text()).toContain("已发布 2/3");
		expect(wrapper!.text()).toContain("失败 1");
	});

	it("lazily loads section children on the first toggle only", async () => {
		scope = effectScope();
		mountCorpus(ref(true));
		await flushPromises();
		vi.mocked(api.getDocumentationTree).mockClear();

		await wrapper!.get(".admin-corpus-section-toggle").trigger("click");
		expect(api.getDocumentationTree).toHaveBeenCalledWith("/docs/concepts");
		await flushPromises();
		expect(wrapper!.text()).toContain("Workloads");

		vi.mocked(api.getDocumentationTree).mockClear();
		await wrapper!.get(".admin-corpus-section-toggle").trigger("click");
		expect(api.getDocumentationTree).not.toHaveBeenCalled();
	});

	it("auto-opens the newest batch detail with per-item terminal states", async () => {
		vi.mocked(api.listAdminBatches).mockResolvedValue({ batches: [batch()] } satisfies AdminDocumentBatchList);
		vi.mocked(api.getAdminBatch).mockResolvedValue(batch());
		vi.mocked(api.listAdminBatchItems).mockResolvedValue({
			items: [batchItem("Skipped"), batchItem("Published")],
		} satisfies AdminDocumentBatchItemsPage);
		scope = effectScope();
		mountCorpus(ref(true));
		await flushPromises();

		// The composable auto-opens the newest batch; the batch pane shows the
		// detail with its per-item terminal states and the count rollup.
		const detail = wrapper!.get(".admin-batch-detail");
		expect(detail.text()).toContain("Completed");
		expect(detail.text()).toContain("Skipped");
		expect(detail.text()).toContain("Published");
		expect(detail.text()).toContain("已发布 1");
		expect(detail.text()).toContain("跳过 1");
	});

	it("applies the failures-only filter on form submit, not on toggle", async () => {
		scope = effectScope();
		mountCorpus(ref(true));
		await flushPromises();
		vi.mocked(api.listAdminCorpus).mockClear();

		await wrapper!.get(".admin-corpus-filters input[type=checkbox]").setValue(true);
		expect(api.listAdminCorpus).not.toHaveBeenCalled();

		await wrapper!.get('[aria-label="Search page titles"]').setValue("Pod Lifecycle");
		await wrapper!.get(".admin-corpus-filters").trigger("submit");
		expect(api.listAdminCorpus).toHaveBeenCalledWith({ search: "Pod Lifecycle", failures: true });
	});
});
