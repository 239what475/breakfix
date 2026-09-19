import { flushPromises, mount } from "@vue/test-utils";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "../../api/client";
import { automockApi } from "../../test/client-mock";
import DocumentationPage from "./DocumentationPage.vue";
import { podLifecyclePage, podLifecyclePath, practiceIndex, practiceProjection, treeAt } from "./fixtures";

vi.mock("../../api/client", async (importOriginal) => {
	const { automockApi } = await import("../../test/client-mock");
	const actual = await importOriginal<typeof import("../../api/client")>();
	return { ...actual, api: automockApi(actual.api) };
});

const source = "kubernetes";
const version = "snapshot-ce98a43";
const entryUrl = `/documentation?source=${source}&version=${version}&path=%2Fdocs%2F`;
const podLifecycleUrl = `/documentation?source=${source}&version=${version}&path=%2Fdocs%2Fconcepts%2Fworkloads%2Fpods%2Fpod-lifecycle%2F`;

// happy-dom's history does not replay entries or fire popstate on back();
// a real browser back delivers exactly a restored URL plus the event.
function browserNavigation(targetUrl: string) {
	window.history.replaceState({ documentation: true }, "", targetUrl);
	window.dispatchEvent(new PopStateEvent("popstate"));
}

// The reader syncs the URL hash from an IntersectionObserver; happy-dom
// never computes intersections, so the spec drives the callback by hand.
const observerInstances: Array<{ callback: IntersectionObserverCallback; observed: Element[] }> = [];

class IntersectionObserverStub {
	constructor(callback: IntersectionObserverCallback) {
		observerInstances.push({ callback, observed: [] });
	}
	observe(target: Element) {
		observerInstances[observerInstances.length - 1].observed.push(target);
	}
	unobserve() {}
	disconnect() {}
	takeRecords() {
		return [];
	}
	root = null;
	rootMargin = "0px";
	thresholds = [0];
}

function mountReader() {
	return mount(DocumentationPage, {});
}

// Markdown rendering waits on shiki's WASM highlighter, a real async task
// that flushPromises alone cannot observe.
async function waitForArticle(page: ReturnType<typeof mountReader>) {
	await vi.waitFor(() => {
		if (!page.find(".documentation-article h2#pod-lifetime").exists()) throw new Error("article not rendered yet");
	}, { timeout: 15_000 });
	return page.get(".documentation-article h2#pod-lifetime");
}

async function openEntryByText(page: ReturnType<typeof mountReader>, title: string) {
	const link = page.findAll(".documentation-toc-link").find((button) => button.text() === title);
	if (!link) throw new Error(`outline entry not found: ${title}`);
	await link.trigger("click");
	await flushPromises();
}

async function openPodLifecycle(page: ReturnType<typeof mountReader>) {
	await page.get('button[aria-label="Toggle Concepts section"]').trigger("click");
	await flushPromises();
	await page.get('button[aria-label="Toggle Workloads section"]').trigger("click");
	await flushPromises();
	await page.get('button[aria-label="Toggle Pods section"]').trigger("click");
	await flushPromises();
	await openEntryByText(page, "Pod Lifecycle");
}

describe("DocumentationPage", () => {
	let wrapper: ReturnType<typeof mountReader> | undefined;

	beforeEach(() => {
		vi.clearAllMocks();
		window.history.replaceState({}, "", entryUrl);
		vi.stubGlobal("IntersectionObserver", IntersectionObserverStub);
		vi.mocked(api.getDocumentationTree).mockImplementation(async (path?: string) => treeAt(path));
		vi.mocked(api.getDocumentationPage).mockResolvedValue(podLifecyclePage);
		vi.mocked(api.getDocumentationPractices).mockResolvedValue({ digest: "sha256:none", practices: [] });
		vi.mocked(api.getDocumentationPractice).mockResolvedValue(practiceProjection);
	});

	afterEach(() => {
		wrapper?.unmount();
		vi.unstubAllGlobals();
	});

	it("shows the outline on the entry path and does not fetch a page", async () => {
		wrapper = mountReader();
		await flushPromises();

		expect(wrapper.get('nav[aria-label="Documentation outline"]').text()).toContain("Concepts");
		expect(wrapper.text()).toContain("Choose a page from the outline to start reading.");
		expect(api.getDocumentationPage).not.toHaveBeenCalled();
	});

	it("lazily walks the outline, pushes the URL, and restores on back/forward", async () => {
		wrapper = mountReader();
		await flushPromises();

		// Children load per section on first toggle only.
		await wrapper.get('button[aria-label="Toggle Concepts section"]').trigger("click");
		await flushPromises();
		expect(api.getDocumentationTree).toHaveBeenCalledWith("/docs/concepts");

		await openPodLifecycle(wrapper);
		expect(api.getDocumentationPage).toHaveBeenCalledWith(podLifecyclePath);
		expect((await waitForArticle(wrapper)).text()).toContain("Pod Lifetime");
		expect(window.location.search).toContain("path=%2Fdocs%2Fconcepts%2Fworkloads%2Fpods%2Fpod-lifecycle%2F");

		browserNavigation(entryUrl);
		await flushPromises();
		expect(wrapper.text()).toContain("Choose a page from the outline to start reading.");

		browserNavigation(podLifecycleUrl);
		expect((await waitForArticle(wrapper)).text()).toContain("Pod Lifetime");
	});

	it("reports load failures and retries", async () => {
		window.history.replaceState({}, "", podLifecycleUrl);
		vi.mocked(api.getDocumentationPage).mockRejectedValueOnce(new Error("boom"));
		wrapper = mountReader();
		await flushPromises();

		expect(wrapper.text()).toContain("Documentation is unavailable.");
		expect(wrapper.find(".documentation-article").exists()).toBe(false);

		await wrapper.get(".documentation-state-error button").trigger("click");
		expect((await waitForArticle(wrapper)).text()).toContain("Pod Lifetime");
	});

	it("falls back from a non-document path", async () => {
		window.history.replaceState({}, "", `/documentation?source=${source}&version=${version}&path=%2Fblog%2F`);
		wrapper = mountReader();
		await flushPromises();

		expect(window.location.search).toContain("path=%2Fdocs%2F");
		expect(wrapper.text()).toContain("Choose a page from the outline to start reading.");
		expect(api.getDocumentationPage).not.toHaveBeenCalled();
	});

	it("syncs the URL hash from the visible heading", async () => {
		window.history.replaceState({}, "", podLifecycleUrl);
		wrapper = mountReader();
		const heading = await waitForArticle(wrapper);

		const observer = observerInstances.at(-1)!;
		expect(observer.observed).toContain(heading.element);

		observer.callback(
			[{ isIntersecting: true, target: heading.element, boundingClientRect: { top: 10 } as DOMRect } as IntersectionObserverEntry],
			{} as IntersectionObserver,
		);
		expect(window.location.search).toContain("hash=pod-lifetime");
	});

	it("anchors exactly one practice entry and swaps the reader layout", async () => {
		window.history.replaceState({}, "", podLifecycleUrl);
		vi.mocked(api.getDocumentationPractices).mockResolvedValue(practiceIndex);
		wrapper = mountReader();
		await waitForArticle(wrapper);

		const article = wrapper.get(".documentation-article");
		// The anchor buttons are decorated after the practice index lands.
		await vi.waitFor(() => {
			if (article.findAll(".practice-anchor-button").length !== 1) throw new Error("practice anchor not decorated yet");
		}, { timeout: 5_000 });
		expect(article.get("h2#pod-lifetime .practice-anchor-button").attributes("aria-label")).toBe("Start practice: Observe Pod lifetime");

		const reader = wrapper.get(".documentation-reader");
		expect(reader.classes()).not.toContain("practice-open");

		(article.element.querySelector("#pod-lifetime .practice-anchor-button") as HTMLElement).click();
		await flushPromises();

		// Anonymous entry asks for a login while the frozen projection loads.
		expect(wrapper.emitted("request-auth")).toHaveLength(1);
		expect(reader.classes()).toContain("practice-open");
		expect(reader.classes()).toContain("toc-collapsed");
		const panel = wrapper.get(".practice-panel");
		expect(panel.text()).toContain("Observe Pod lifetime");
		expect(panel.text()).toContain("Observe a Pod reach Running");
		expect(panel.text()).toContain("One Pod in the fixed Kubernetes environment");

		await panel.get('button[aria-label="Close practice panel"]').trigger("click");
		await flushPromises();
		expect(wrapper.find(".practice-panel").exists()).toBe(false);
		expect(reader.classes()).not.toContain("practice-open");
		expect(reader.classes()).not.toContain("toc-collapsed");
	});
});
