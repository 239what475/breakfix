import { flushPromises, mount } from "@vue/test-utils";
import { afterEach, describe, expect, it, vi } from "vitest";
import { api } from "./api/client";
import { automockApi } from "./test/client-mock";
import AppShell from "./AppShell.vue";

vi.mock("./api/client", async (importOriginal) => {
	const { automockApi } = await import("./test/client-mock");
	const actual = await importOriginal<typeof import("./api/client")>();
	return { ...actual, api: automockApi(actual.api) };
});

// The shell's own wiring is the gate under test: every page child is stubbed
// so the mount stays about the playground dock and nothing else.
const stubs = {
	AppTopbar: true,
	ScenarioCatalogPage: true,
	MySpacePage: true,
	AdminPage: true,
	AuthDialog: true,
	DocumentationPage: true,
	ScenarioWorkspace: true,
	AuthoringWorkspace: true,
};

function mountShell() {
	return mount(AppShell, { global: { stubs } });
}

describe("AppShell playground gate", () => {
	afterEach(() => {
		localStorage.removeItem("token");
		vi.clearAllMocks();
	});

	it("renders no ball and fires no request for an anonymous visitor", async () => {
		const wrapper = mountShell();
		await flushPromises();

		expect(wrapper.find(".playground-fab").exists()).toBe(false);
		expect(api.getPlayground).not.toHaveBeenCalled();
	});

	it("mounts the ball for a signed-in user on every page", async () => {
		vi.mocked(api.getPlayground).mockResolvedValue({ state: "none" });
		localStorage.setItem("token", "reader-token");
		const wrapper = mountShell();
		await flushPromises();

		expect(wrapper.find(".playground-fab").exists()).toBe(true);
		expect(api.getPlayground).toHaveBeenCalledTimes(1);
	});
});
