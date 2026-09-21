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
		window.history.pushState({}, "", "/");
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

// A JWT-shaped token whose payload carries the role claim; the shell reads
// the claim locally to gate the admin surface display.
function tokenWithRole(role: string) {
	const payload = btoa(JSON.stringify({ role })).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
	return `header.${payload}.signature`;
}

describe("AppShell admin gate", () => {
	afterEach(() => {
		localStorage.removeItem("token");
		window.history.pushState({}, "", "/");
		vi.clearAllMocks();
	});

	it("never renders the console for an anonymous or non-admin visitor", async () => {
		window.history.pushState({}, "", "/admin/environments");
		const anonymous = mountShell();
		await flushPromises();
		expect(anonymous.findComponent({ name: "AdminPage" }).exists()).toBe(false);

		localStorage.setItem("token", tokenWithRole("user"));
		const member = mountShell();
		await flushPromises();
		expect(member.findComponent({ name: "AdminPage" }).exists()).toBe(false);
	});

	it("renders the console for an admin on any admin section path", async () => {
		vi.mocked(api.getPlayground).mockResolvedValue({ state: "none" });
		localStorage.setItem("token", tokenWithRole("admin"));
		window.history.pushState({}, "", "/admin/environments");
		const wrapper = mountShell();
		await flushPromises();

		const console = wrapper.findComponent({ name: "AdminPage" });
		expect(console.exists()).toBe(true);
		expect(console.props("section")).toBe("environments");
	});
});
