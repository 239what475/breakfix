import { flushPromises, mount } from "@vue/test-utils";
import { defineComponent, nextTick, ref } from "vue";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { useTerminalSession, type TerminalChannel } from "./useTerminalSession";

// xterm brings a real DOM renderer; this spec verifies session state and
// ticket plumbing, so the terminal surface is a recording stub.
	vi.mock("@xterm/xterm", () => ({
	Terminal: class {
		cols = 80;
		rows = 24;
		loadAddon() {}
		open() {}
		write() {}
		dispose() {}
		focus() {}
		onData() {
			return { dispose() {} };
		}
	},
}));
vi.mock("@xterm/addon-fit", () => ({
	FitAddon: class {
		fit() {}
	},
}));

type SocketEvent = { data?: string };

class FakeWebSocket {
	static CONNECTING = 0;
	static OPEN = 1;
	static instances: FakeWebSocket[] = [];
	readyState = FakeWebSocket.OPEN;
	url: string;
	sent: string[] = [];
	onopen: (() => void) | null = null;
	onmessage: ((event: SocketEvent) => void) | null = null;
	onclose: (() => void) | null = null;
	onerror: (() => void) | null = null;

	constructor(url: string) {
		this.url = url;
		FakeWebSocket.instances.push(this);
	}

	send(data: string) {
		this.sent.push(data);
	}

	close() {
		this.readyState = FakeWebSocket.CONNECTING;
	}
}

function lastSocket() {
	return FakeWebSocket.instances[FakeWebSocket.instances.length - 1];
}

describe("useTerminalSession", () => {
	const ticket = vi.fn();

	// Every test gets its own refs: a shared host ref would look like a change
	// and fire an extra connect on the next mount.
	function mountSession() {
		const host = ref<HTMLDivElement>();
		const channel = ref<TerminalChannel | null>({
			ticket: (window) => ticket(window),
			socket: (window, token) => `ws://terminal/${window}?ticket=${token}`,
		});
		const nodeName = ref<string | null>(null);
		const windowName = ref<string | null>("shell-1");
		const session = useTerminalSession(host, channel, nodeName, windowName);
		const harness = defineComponent({
			setup: () => ({ host }),
			template: '<div ref="host"></div>',
		});
		mount(harness);
		return { session, channel };
	}

	beforeEach(() => {
		vi.stubGlobal("ResizeObserver", class {
			observe() {}
			disconnect() {}
		});
		vi.stubGlobal("WebSocket", FakeWebSocket);
		FakeWebSocket.instances = [];
		ticket.mockReset();
		ticket.mockResolvedValue("ticket-1");
	});

	afterEach(() => {
		vi.unstubAllGlobals();
	});

	it("reports connected only after the socket's ready message", async () => {
		const { session } = mountSession();
		await flushPromises();

		expect(ticket).toHaveBeenCalledWith("shell-1");
		expect(FakeWebSocket.instances).toHaveLength(1);
		expect(session.state.value).toBe("connecting");

		lastSocket().onmessage?.({ data: JSON.stringify({ type: "ready" }) });
		expect(session.state.value).toBe("connected");

		// Terminal data keeps flowing over the same open socket.
		lastSocket().onmessage?.({ data: JSON.stringify({ type: "data", data: "$ " }) });
		expect(session.state.value).toBe("connected");
	});

	it("turns disconnected immediately when the socket closes and reconnects on a fresh ticket", async () => {
		const { session } = mountSession();
		await flushPromises();
		lastSocket().onmessage?.({ data: JSON.stringify({ type: "ready" }) });
		expect(session.state.value).toBe("connected");

		// The server closing the socket is visible on the same tick: no user
		// action is needed to unmask a dead session.
		lastSocket().onclose?.();
		expect(session.state.value).toBe("disconnected");
		expect(session.stateMessage.value).toContain("disconnected");

		ticket.mockResolvedValue("ticket-2");
		void session.connect();
		await flushPromises();
		expect(ticket).toHaveBeenCalledTimes(2);
		expect(FakeWebSocket.instances).toHaveLength(2);
		expect(lastSocket().url).toContain("ticket=ticket-2");
		lastSocket().onmessage?.({ data: JSON.stringify({ type: "ready" }) });
		expect(session.state.value).toBe("connected");
	});

	it("never leaves a stale connected behind a locally disposed socket", async () => {
		const { session, channel } = mountSession();
		await flushPromises();
		lastSocket().onmessage?.({ data: JSON.stringify({ type: "ready" }) });
		expect(session.state.value).toBe("connected");

		// Hiding the pane drops the channel: the session disposes its socket
		// and must stop claiming Connected, even before any reconnect attempt.
		channel.value = null;
		await nextTick();
		await nextTick();
		expect(session.state.value).not.toBe("connected");
		expect(session.stateMessage.value).toContain("disconnected");
	});
});
