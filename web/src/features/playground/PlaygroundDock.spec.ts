import { flushPromises, mount } from "@vue/test-utils";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "../../api/client";
import { automockApi } from "../../test/client-mock";
import type { PlaygroundEnvironment } from "../../api/generated";
import PlaygroundDock from "./PlaygroundDock.vue";

vi.mock("../../api/client", async (importOriginal) => {
	const { automockApi } = await import("../../test/client-mock");
	const actual = await importOriginal<typeof import("../../api/client")>();
	return { ...actual, api: automockApi(actual.api) };
});

// TerminalPane pulls xterm and the ticket plumbing; the dock spec verifies
// the binding contract, not the terminal itself.
vi.mock("../workspace/TerminalPane.vue", () => ({
	default: { name: "TerminalPane", props: ["terminalId", "runtime", "nodes", "visible", "channel", "closeWindow"], template: '<div class="terminal-stub"></div>' },
}));

function environmentAt(state: PlaygroundEnvironment["state"], generation = 0): PlaygroundEnvironment {
  return { state, environment_id: "environment-1", runtime: "k8s", generation };
}

async function dockAt(state: PlaygroundEnvironment["state"]) {
	vi.mocked(api.getPlayground).mockResolvedValue(environmentAt(state));
	const wrapper = mount(PlaygroundDock, {});
	await flushPromises();
	await wrapper.get("button.playground-fab").trigger("click");
	await flushPromises();
	return wrapper;
}

function verb(wrapper: ReturnType<typeof mount>, label: string) {
	return wrapper
		.get(".playground-panel")
		.findAll("button")
		.find((button) => ["Create", "Creating...", "Retry", "Reset", "Resetting...", "Close", "Closing..."].includes(label) && button.text().trim() === label);
}

describe("PlaygroundDock", () => {
	beforeEach(() => {
		vi.clearAllMocks();
		localStorage.setItem("token", "reader-token");
	});

	afterEach(() => {
		localStorage.removeItem("token");
	});

	it("reconciles the session on mount and reflects it in the ball", async () => {
		vi.mocked(api.getPlayground).mockResolvedValue(environmentAt("ready"));
		const wrapper = mount(PlaygroundDock, {});
		await flushPromises();

		expect(api.getPlayground).toHaveBeenCalledTimes(1);
		const ball = wrapper.get("button.playground-fab");
		expect(ball.attributes("data-state")).toBe("ready");
		expect(ball.attributes("aria-label")).toBe("Playground");
	});

	it("keeps polling while the session is preparing", async () => {
		vi.useFakeTimers();
		try {
			vi.mocked(api.getPlayground).mockResolvedValue(environmentAt("creating"));
			mount(PlaygroundDock, {});
			await flushPromises();
			vi.mocked(api.getPlayground).mockClear();

			await vi.advanceTimersByTimeAsync(2_100);
			expect(api.getPlayground).toHaveBeenCalled();
		} finally {
			vi.useRealTimers();
		}
	});

	it("does not trust a ready that still reports the pre-wipe generation", async () => {
		vi.useFakeTimers();
		try {
			vi.mocked(api.getPlayground).mockResolvedValue(environmentAt("ready", 0));
			const wrapper = mount(PlaygroundDock, {});
			await flushPromises();
			await wrapper.get("button.playground-fab").trigger("click");

			// The reset POST answers the optimistic projection: creating, but
			// still carrying the pre-wipe generation.
			vi.mocked(api.resetPlayground).mockResolvedValue(environmentAt("creating", 0));
			await verb(wrapper, "Reset")!.trigger("click");
			await flushPromises();

			// The controller has not adopted the wipe yet, so the GET can still
			// read the old session as ready — same generation. That ready is
			// the pre-wipe terminal and must not settle the loop.
			vi.mocked(api.getPlayground).mockResolvedValue(environmentAt("ready", 0));
			await vi.advanceTimersByTimeAsync(2_100);
			await flushPromises();
			expect(wrapper.get("button.playground-fab").attributes("data-state")).toBe("creating");
			vi.mocked(api.getPlayground).mockClear();

			await vi.advanceTimersByTimeAsync(2_100);
			expect(api.getPlayground).toHaveBeenCalled();

			// The adoption moves the generation; the ready that follows belongs
			// to the wiped environment and settles the loop.
			vi.mocked(api.getPlayground).mockResolvedValue(environmentAt("creating", 1));
			await vi.advanceTimersByTimeAsync(2_100);
			vi.mocked(api.getPlayground).mockResolvedValue(environmentAt("ready", 1));
			await vi.advanceTimersByTimeAsync(2_100);
			await flushPromises();
			const settled = vi.mocked(api.getPlayground).mock.calls.length;
			await vi.advanceTimersByTimeAsync(6_500);
			expect(vi.mocked(api.getPlayground).mock.calls.length).toBe(settled);
			expect(wrapper.get("button.playground-fab").attributes("data-state")).toBe("ready");
		} finally {
			vi.useRealTimers();
		}
	});

	it("enables create from none without reset or close", async () => {
		const wrapper = await dockAt("none");
		expect(verb(wrapper, "Create")?.attributes("disabled")).toBeUndefined();
		expect(verb(wrapper, "Reset")).toBeUndefined();
		expect(verb(wrapper, "Close")).toBeUndefined();
	});

	it("attaches the terminal and offers reset and close from ready", async () => {
		const wrapper = await dockAt("ready");
		const terminal = wrapper.findComponent({ name: "TerminalPane" });
		expect(terminal.exists()).toBe(true);
		expect(terminal.props("terminalId")).toBe("playground");
		expect(terminal.props("runtime")).toBe("k8s");
		expect(terminal.props("visible")).toBe(true);
		expect(terminal.props("channel")).toBeTruthy();
		expect(verb(wrapper, "Reset")?.attributes("disabled")).toBeUndefined();
		expect(verb(wrapper, "Close")?.attributes("disabled")).toBeUndefined();
	});

	it("offers retry from failed and shows preparing while creating", async () => {
		const failed = await dockAt("failed");
		expect(verb(failed, "Retry")).toBeTruthy();
		expect(verb(failed, "Close")?.attributes("disabled")).toBeUndefined();

		const creating = await dockAt("creating");
		expect(creating.get(".playground-panel-state").text()).toContain("Preparing");
		expect(verb(creating, "Reset")).toBeUndefined();
	});

	it("drives create, reset, and close through the API", async () => {
		vi.mocked(api.getPlayground).mockResolvedValue(environmentAt("none"));
		vi.mocked(api.createPlayground).mockResolvedValue(environmentAt("creating"));
		vi.mocked(api.resetPlayground).mockResolvedValue(environmentAt("creating"));
		vi.mocked(api.closePlayground).mockResolvedValue({ closed: true });
		const wrapper = await dockAt("none");

		await verb(wrapper, "Create")!.trigger("click");
		await flushPromises();
		expect(api.createPlayground).toHaveBeenCalledTimes(1);

		// A landing login signal reconciles the session; the verbs follow.
		vi.mocked(api.getPlayground).mockResolvedValue(environmentAt("ready"));
		await wrapper.setProps({ authSignal: 1 });
		await flushPromises();
		await verb(wrapper, "Reset")!.trigger("click");
		await flushPromises();
		expect(api.resetPlayground).toHaveBeenCalledTimes(1);

		await verb(wrapper, "Close")!.trigger("click");
		await flushPromises();
		expect(api.closePlayground).toHaveBeenCalledTimes(1);
	});

	it("closes the dialog on Escape", async () => {
		const wrapper = await dockAt("ready");
		const panel = wrapper.get(".playground-panel");
		expect(panel.attributes("role")).toBe("dialog");

		await panel.trigger("keydown", { key: "Escape" });
		expect(wrapper.find(".playground-panel").exists()).toBe(false);
	});
});
