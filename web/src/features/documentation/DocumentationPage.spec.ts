import { flushPromises, mount } from "@vue/test-utils";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "../../api/client";
import { automockApi } from "../../test/client-mock";
import type { DocumentationLink } from "../../api/generated";
import DocumentationPage from "./DocumentationPage.vue";

vi.mock("../../api/client", async (importOriginal) => {
	const { automockApi } = await import("../../test/client-mock");
	const actual = await importOriginal<typeof import("../../api/client")>();
	return { ...actual, api: automockApi(actual.api) };
});

function linkFixture(key: string, overrides: Partial<DocumentationLink> = {}): DocumentationLink {
	return { key, title: `Doc ${key}`, url: `https://${key}.example/docs`, embed: true, ...overrides };
}

const sampleLinks = [
	linkFixture("doc-alpha"),
	linkFixture("doc-beta"),
	linkFixture("doc-gamma", { embed: false }),
];

function mountPage(props: { isAdmin?: boolean } = {}) {
	return mount(DocumentationPage, { props });
}

async function selectByTitle(wrapper: ReturnType<typeof mountPage>, title: string) {
	const button = wrapper.findAll(".documentation-list-link").find((candidate) => candidate.text() === title);
	if (!button) throw new Error(`list entry not found: ${title}`);
	await button.trigger("click");
	await flushPromises();
}

function renderedFrames(wrapper: ReturnType<typeof mountPage>) {
	return wrapper.findAll("iframe.documentation-frame");
}

describe("DocumentationPage list", () => {
	let wrapper: ReturnType<typeof mountPage> | undefined;

	beforeEach(() => {
		vi.clearAllMocks();
		window.history.replaceState({}, "", "/documentation");
		vi.mocked(api.listDocumentationLinks).mockResolvedValue({ links: sampleLinks });
	});

	afterEach(() => {
		wrapper?.unmount();
	});

	it("renders the shared list and starts on the empty state", async () => {
		wrapper = mountPage();
		await flushPromises();

		const entries = wrapper.findAll(".documentation-list-item");
		expect(entries).toHaveLength(3);
		expect(wrapper.text()).toContain("Choose a document from the list to start reading.");
		expect(renderedFrames(wrapper)).toHaveLength(0);
	});

	it("hides admin controls from ordinary visitors and shows them to admins", async () => {
		wrapper = mountPage();
		await flushPromises();
		expect(wrapper.find(".documentation-item-settings").exists()).toBe(false);
		expect(wrapper.text()).not.toContain("Add documentation link");

		wrapper.unmount();
		wrapper = mountPage({ isAdmin: true });
		await flushPromises();
		expect(wrapper.findAll(".documentation-item-settings")).toHaveLength(3);
		expect(wrapper.text()).toContain("Add documentation link");
	});

	it("shows the load failure state and retries", async () => {
		vi.mocked(api.listDocumentationLinks).mockRejectedValueOnce(new Error("boom"));
		wrapper = mountPage();
		await flushPromises();
		expect(wrapper.text()).toContain("The documentation list is unavailable.");

		await wrapper.get(".documentation-empty-error button").trigger("click");
		await flushPromises();
		expect(api.listDocumentationLinks).toHaveBeenCalledTimes(2);
		expect(wrapper.text()).toContain("Choose a document from the list to start reading.");
	});

	it("shows the empty-list state when nothing has been added", async () => {
		vi.mocked(api.listDocumentationLinks).mockResolvedValue({ links: [] });
		wrapper = mountPage();
		await flushPromises();
		expect(wrapper.text()).toContain("No documentation has been added yet.");
	});
});

describe("DocumentationPage selection", () => {
	let wrapper: ReturnType<typeof mountPage> | undefined;

	beforeEach(() => {
		vi.clearAllMocks();
		window.history.replaceState({}, "", "/documentation");
		vi.mocked(api.listDocumentationLinks).mockResolvedValue({ links: sampleLinks });
	});

	afterEach(() => {
		wrapper?.unmount();
	});

	it("shows the selected document in an iframe and mirrors the key into the URL", async () => {
		wrapper = mountPage();
		await flushPromises();
		await selectByTitle(wrapper, "Doc doc-alpha");

		const frames = renderedFrames(wrapper);
		expect(frames).toHaveLength(1);
		expect(frames[0].attributes("src")).toBe("https://doc-alpha.example/docs");
		expect(frames[0].attributes("referrerpolicy")).toBe("no-referrer");
		expect(window.location.search).toBe("?doc=doc-alpha");
		expect(wrapper.get(".documentation-toolbar-title").text()).toBe("Doc doc-alpha");
	});

	it("keeps visited iframes mounted under v-show so switching never reloads", async () => {
		wrapper = mountPage();
		await flushPromises();
		await selectByTitle(wrapper, "Doc doc-alpha");
		await selectByTitle(wrapper, "Doc doc-beta");

		const frames = renderedFrames(wrapper);
		expect(frames).toHaveLength(2);
		expect(frames[0].attributes("src")).toBe("https://doc-alpha.example/docs");
		expect(frames[0].element.style.display).toBe("none");
		expect(frames[1].attributes("src")).toBe("https://doc-beta.example/docs");
		expect(frames[1].element.style.display).not.toBe("none");

		// Going back to the first document reuses the same mounted node; the
		// pool order follows recency, so beta hides and alpha shows again.
		await selectByTitle(wrapper, "Doc doc-alpha");
		const reordered = renderedFrames(wrapper);
		expect(reordered.map((frame) => frame.attributes("src"))).toEqual([
			"https://doc-beta.example/docs",
			"https://doc-alpha.example/docs",
		]);
		expect(reordered[0].element.style.display).toBe("none");
		expect(reordered[1].element.style.display).not.toBe("none");
	});

	it("evicts the least recently used iframe beyond the pool limit", async () => {
		const sixLinks = ["a", "b", "c", "d", "e", "f"].map((suffix) => linkFixture(`doc-${suffix}`));
		vi.mocked(api.listDocumentationLinks).mockResolvedValue({ links: sixLinks });
		wrapper = mountPage();
		await flushPromises();

		for (const suffix of ["a", "b", "c", "d", "e"]) await selectByTitle(wrapper, `Doc doc-${suffix}`);
		// Re-touching doc-a makes doc-b the least recently used entry.
		await selectByTitle(wrapper, "Doc doc-a");
		await selectByTitle(wrapper, "Doc doc-f");

		const sources = renderedFrames(wrapper).map((frame) => frame.attributes("src"));
		expect(sources).toHaveLength(5);
		expect(sources).not.toContain("https://doc-b.example/docs");
		expect(sources).toContain("https://doc-a.example/docs");
	});

	it("keeps the iframe pool mounted while a URL-card entry is showing", async () => {
		wrapper = mountPage();
		await flushPromises();
		await selectByTitle(wrapper, "Doc doc-alpha");
		const frameBefore = renderedFrames(wrapper)[0].element;

		// The pool container hides for the URL card but never unmounts, and a
		// non-embeddable entry never claims a pool slot.
		await selectByTitle(wrapper, "Doc doc-gamma");
		expect(wrapper.get(".documentation-external-card").attributes("href")).toBe("https://doc-gamma.example/docs");
		expect(wrapper.get(".documentation-frames").element.style.display).toBe("none");
		expect(renderedFrames(wrapper)).toHaveLength(1);

		// Back on the embeddable entry: the same iframe node, never reloaded.
		await selectByTitle(wrapper, "Doc doc-alpha");
		const frames = renderedFrames(wrapper);
		expect(frames).toHaveLength(1);
		expect(frames[0].element).toBe(frameBefore);
		expect(wrapper.get(".documentation-frames").element.style.display).not.toBe("none");
	});

	it("restores the selection from ?doc=<key> on load and on popstate", async () => {
		window.history.replaceState({}, "", "/documentation?doc=doc-beta");
		wrapper = mountPage();
		await flushPromises();

		expect(renderedFrames(wrapper).map((frame) => frame.attributes("src"))).toEqual(["https://doc-beta.example/docs"]);
		expect(wrapper.get(".documentation-toolbar-title").text()).toBe("Doc doc-beta");

		// Back to a state without a doc key: the empty state returns.
		window.history.replaceState({}, "", "/documentation");
		window.dispatchEvent(new PopStateEvent("popstate"));
		await flushPromises();
		expect(wrapper.text()).toContain("Choose a document from the list to start reading.");

		// Forward again restores the document without another list request.
		window.history.replaceState({}, "", "/documentation?doc=doc-beta");
		window.dispatchEvent(new PopStateEvent("popstate"));
		await flushPromises();
		expect(wrapper.get(".documentation-toolbar-title").text()).toBe("Doc doc-beta");
		expect(api.listDocumentationLinks).toHaveBeenCalledTimes(1);
	});

	it("falls back to the empty state when ?doc points at a removed key", async () => {
		window.history.replaceState({}, "", "/documentation?doc=doc-gone");
		wrapper = mountPage();
		await flushPromises();

		expect(renderedFrames(wrapper)).toHaveLength(0);
		expect(wrapper.text()).toContain("Choose a document from the list to start reading.");
	});

	it("renders an external card with the URL for entries that refuse embedding", async () => {
		wrapper = mountPage();
		await flushPromises();
		await selectByTitle(wrapper, "Doc doc-gamma");

		expect(renderedFrames(wrapper)).toHaveLength(0);
		const card = wrapper.get(".documentation-external-card");
		expect(card.attributes("href")).toBe("https://doc-gamma.example/docs");
		expect(card.attributes("target")).toBe("_blank");
		expect(card.attributes("rel")).toContain("noopener");
		expect(card.attributes("rel")).toContain("noreferrer");
	});

	it("always offers the entry URL in a new window with noopener", async () => {
		wrapper = mountPage();
		await flushPromises();
		await selectByTitle(wrapper, "Doc doc-alpha");

		const external = wrapper.get(".documentation-open-external");
		expect(external.attributes("href")).toBe("https://doc-alpha.example/docs");
		expect(external.attributes("target")).toBe("_blank");
		expect(external.attributes("rel")).toBe("noopener noreferrer");

		// The toolbar entry follows the embed verdict too: the fallback belongs
		// next to the card as well.
		await selectByTitle(wrapper, "Doc doc-gamma");
		expect(wrapper.get(".documentation-open-external").attributes("href")).toBe("https://doc-gamma.example/docs");
	});
});

describe("DocumentationPage admin surface", () => {
	let wrapper: ReturnType<typeof mountPage> | undefined;

	beforeEach(() => {
		vi.clearAllMocks();
		window.history.replaceState({}, "", "/documentation");
		vi.mocked(api.listDocumentationLinks).mockResolvedValue({ links: sampleLinks });
	});

	afterEach(() => {
		wrapper?.unmount();
	});

	function mountAdmin() {
		return mount(DocumentationPage, { props: { isAdmin: true } });
	}

	it("creates a link from the dialog, selects it, and appends it to the list", async () => {
		vi.mocked(api.createDocumentationLink).mockResolvedValue(linkFixture("doc-new", { title: "Fresh docs", url: "https://fresh.example" }));
		wrapper = mountAdmin();
		await flushPromises();

		await wrapper.get(".documentation-list-add .documentation-list-link").trigger("click");
		const dialog = wrapper.get(".dialog");
		expect(wrapper.text()).toContain("Add documentation link");

		await dialog.get('input[name="title"]').setValue("Fresh docs");
		await dialog.get('input[name="url"]').setValue("https://fresh.example");
		await dialog.get("form").trigger("submit");
		await flushPromises();

		expect(api.createDocumentationLink).toHaveBeenCalledWith({ title: "Fresh docs", url: "https://fresh.example", embed: true });
		expect(wrapper.findAll(".documentation-list-item")).toHaveLength(5);
		expect(wrapper.get(".documentation-toolbar-title").text()).toBe("Fresh docs");
		expect(window.location.search).toBe("?doc=doc-new");
		expect(wrapper.find(".dialog").exists()).toBe(false);
	});

	it("refuses to submit a blank name or a non-http URL", async () => {
		wrapper = mountAdmin();
		await flushPromises();
		await wrapper.get(".documentation-list-add .documentation-list-link").trigger("click");
		const dialog = wrapper.get(".dialog");

		await dialog.get("form").trigger("submit");
		expect(dialog.text()).toContain("Name is required.");
		expect(api.createDocumentationLink).not.toHaveBeenCalled();

		await dialog.get('input[name="title"]').setValue("Local mirror");
		await dialog.get('input[name="url"]').setValue("ftp://mirror.internal/docs");
		await dialog.get("form").trigger("submit");
		expect(dialog.text()).toContain("URL must be an absolute http or https address.");
		expect(api.createDocumentationLink).not.toHaveBeenCalled();
	});

	it("warns about a duplicate title but still allows saving", async () => {
		wrapper = mountAdmin();
		await flushPromises();
		await wrapper.get(".documentation-list-add .documentation-list-link").trigger("click");
		const dialog = wrapper.get(".dialog");

		await dialog.get('input[name="title"]').setValue("DOC DOC-ALPHA");
		expect(dialog.text()).toContain("Another link already uses this name.");

		vi.mocked(api.createDocumentationLink).mockResolvedValue(linkFixture("doc-dup", { title: "DOC DOC-ALPHA", url: "https://another.example" }));
		await dialog.get('input[name="url"]').setValue("https://another.example");
		await dialog.get("form").trigger("submit");
		await flushPromises();
		expect(api.createDocumentationLink).toHaveBeenCalled();
	});

	it("edits an entry through its settings button and follows the rename", async () => {
		vi.mocked(api.updateDocumentationLink).mockResolvedValue(linkFixture("doc-alpha", { title: "Renamed docs", embed: false }));
		wrapper = mountAdmin();
		await flushPromises();
		await selectByTitle(wrapper, "Doc doc-alpha");

		await wrapper.get('.documentation-list-item.active .documentation-item-settings').trigger("click");
		const dialog = wrapper.get(".dialog");
		expect(dialog.get('input[name="title"]').element as HTMLInputElement).toBeTruthy();
		expect((dialog.get('input[name="title"]').element as HTMLInputElement).value).toBe("Doc doc-alpha");

		await dialog.get('input[name="title"]').setValue("Renamed docs");
		await dialog.get('input[name="embed"]').setValue(false);
		await dialog.get("form").trigger("submit");
		await flushPromises();

		expect(api.updateDocumentationLink).toHaveBeenCalledWith("doc-alpha", { title: "Renamed docs", url: "https://doc-alpha.example/docs", embed: false });
		expect(wrapper.get(".documentation-toolbar-title").text()).toBe("Renamed docs");
		// embed=false swaps the iframe for the external card without dropping
		// the selection.
		expect(renderedFrames(wrapper)).toHaveLength(0);
		expect(wrapper.find(".documentation-external-card").exists()).toBe(true);
		expect(window.location.search).toBe("?doc=doc-alpha");
	});

	it("deletes the selected entry and returns the right pane to the empty state", async () => {
		wrapper = mountAdmin();
		await flushPromises();
		await selectByTitle(wrapper, "Doc doc-alpha");

		await wrapper.get('.documentation-list-item.active .documentation-item-settings').trigger("click");
		await wrapper.get(".dialog .danger-text-button").trigger("click");
		await flushPromises();

		expect(api.deleteDocumentationLink).toHaveBeenCalledWith("doc-alpha");
		expect(wrapper.findAll(".documentation-list-item")).toHaveLength(3);
		expect(renderedFrames(wrapper)).toHaveLength(0);
		expect(wrapper.text()).toContain("Choose a document from the list to start reading.");
		expect(window.location.search).toBe("");
	});

	it("surfaces a failed save inside the dialog and keeps the values", async () => {
		vi.mocked(api.createDocumentationLink).mockRejectedValueOnce(new Error("boom"));
		wrapper = mountAdmin();
		await flushPromises();
		await wrapper.get(".documentation-list-add .documentation-list-link").trigger("click");
		const dialog = wrapper.get(".dialog");

		await dialog.get('input[name="title"]').setValue("Fresh docs");
		await dialog.get('input[name="url"]').setValue("https://fresh.example");
		await dialog.get("form").trigger("submit");
		await flushPromises();

		expect(wrapper.find(".dialog").exists()).toBe(true);
		expect(dialog.text()).toContain("Saving the link failed. Try again.");
		expect((dialog.get('input[name="title"]').element as HTMLInputElement).value).toBe("Fresh docs");
	});

	it("surfaces a failed delete inside the dialog and keeps the entry", async () => {
		vi.mocked(api.deleteDocumentationLink).mockRejectedValueOnce(new Error("boom"));
		wrapper = mountAdmin();
		await flushPromises();
		await selectByTitle(wrapper, "Doc doc-alpha");

		await wrapper.get('.documentation-list-item.active .documentation-item-settings').trigger("click");
		await wrapper.get(".dialog .danger-text-button").trigger("click");
		await flushPromises();

		expect(wrapper.find(".dialog").exists()).toBe(true);
		expect(wrapper.get(".dialog").text()).toContain("Deleting the link failed. Try again.");
		// Three links plus the admin add row all survive the failed delete.
		expect(wrapper.findAll(".documentation-list-item")).toHaveLength(4);
		expect(window.location.search).toBe("?doc=doc-alpha");
	});
});

describe("DocumentationPage responsive chrome", () => {
	beforeEach(() => {
		vi.clearAllMocks();
		window.history.replaceState({}, "", "/documentation");
		vi.mocked(api.listDocumentationLinks).mockResolvedValue({ links: sampleLinks });
	});

	afterEach(() => {
		wrapper?.unmount();
	});

	let wrapper: ReturnType<typeof mountPage> | undefined;

	it("collapses the list on desktop and opens the drawer for the mobile contents", async () => {
		wrapper = mountPage();
		await flushPromises();

		await wrapper.get('button[aria-label="Collapse documentation list"]').trigger("click");
		expect(wrapper.get(".documentation-shell").classes()).toContain("list-collapsed");
		await wrapper.get('button[aria-label="Expand documentation list"]').trigger("click");
		expect(wrapper.get(".documentation-shell").classes()).not.toContain("list-collapsed");

		await wrapper.get(".documentation-list-mobile").trigger("click");
		expect(wrapper.get(".documentation-list").classes()).toContain("open");
		expect(wrapper.get(".documentation-list-backdrop").exists()).toBe(true);

		await selectByTitle(wrapper, "Doc doc-beta");
		expect(wrapper.get(".documentation-list").classes()).not.toContain("open");
	});
});
