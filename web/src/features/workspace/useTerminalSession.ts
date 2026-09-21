import { onScopeDispose, ref, watch, type Ref } from "vue";
import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import { api } from "../../api/client";
import "@xterm/xterm/css/xterm.css";

type TerminalState = "idle" | "connecting" | "connected" | "disconnected";

// A terminal channel abstracts the content-specific parts of one terminal:
// how a one-time ticket is minted and where the ticket-authenticated socket
// lives. Operations scenarios and the playground share the socket protocol
// and differ only in their channel.
export interface TerminalChannel {
  ticket: (window: string, node: string | undefined) => Promise<string>;
  socket: (window: string, ticket: string, node: string | undefined) => string;
}

function terminalSocketURL(path: string, window: string, ticket: string, node: string | undefined) {
  const protocol = location.protocol === "https:" ? "wss:" : "ws:";
  const query = new URLSearchParams({ window, ticket });
  if (node) query.set("node", node);
  return `${protocol}//${location.host}${path}?${query.toString()}`;
}

export function scenarioTerminalChannel(scenarioId: string): TerminalChannel {
  return {
    ticket: (window, node) =>
      api
        .createTerminalTicket(scenarioId, window, node)
        .then((response) => response.ticket),
    socket: (window, ticket, node) =>
      terminalSocketURL(`/api/operations/scenarios/${scenarioId}/terminal`, window, ticket, node),
  };
}

// The playground is one session per user: its terminal rides fixed paths
// with no content identifier.
export function playgroundTerminalChannel(): TerminalChannel {
  return {
    ticket: (window, node) =>
      api
        .createPlaygroundTerminalTicket(window, node)
        .then((response) => response.ticket),
    socket: (window, ticket, node) =>
      terminalSocketURL("/api/playground/terminal", window, ticket, node),
  };
}

export function useTerminalSession(
  host: Readonly<Ref<HTMLDivElement | undefined>>,
  channel: Readonly<Ref<TerminalChannel | null>>,
  nodeName: Readonly<Ref<string | null>>,
  windowName: Readonly<Ref<string | null>>,
) {
  const state = ref<TerminalState>("idle");
  const stateMessage = ref("");
  let terminal: Terminal | undefined;
  let fit: FitAddon | undefined;
  let socket: WebSocket | undefined;
  let observer: ResizeObserver | undefined;
  let input: { dispose: () => void } | undefined;
  let epoch = 0;

  function sendResize() {
    if (!terminal || socket?.readyState !== WebSocket.OPEN) return;
    socket.send(
      JSON.stringify({
        type: "resize",
        cols: terminal.cols,
        rows: terminal.rows,
      }),
    );
  }

  function disconnect() {
    epoch += 1;
    observer?.disconnect();
    observer = undefined;
    input?.dispose();
    input = undefined;
    socket?.close();
    socket = undefined;
    terminal?.dispose();
    terminal = undefined;
    fit = undefined;
  }

  async function connect() {
    disconnect();
    const link = channel.value;
    const node = nodeName.value;
    const window = windowName.value;
    if (!host.value || !link || !window) {
      state.value = "idle";
      return;
    }
    const currentEpoch = ++epoch;
    state.value = "connecting";
    stateMessage.value = "";
    terminal = new Terminal({
      cursorBlink: true,
      fontSize: 13,
      fontFamily: '"IBM Plex Mono", "SFMono-Regular", Consolas, monospace',
      lineHeight: 1.25,
      convertEol: true,
      theme: {
        background: "#101719",
        foreground: "#dce7e1",
        cursor: "#e8bd72",
        black: "#101719",
        brightBlack: "#65716e",
        red: "#e48670",
        green: "#9ac0a2",
        yellow: "#e8bd72",
        blue: "#8bb4c8",
        magenta: "#c89ab5",
        cyan: "#85c6bd",
        white: "#edf4ef",
      },
    });
    fit = new FitAddon();
    terminal.loadAddon(fit);
    terminal.open(host.value);
    fit.fit();
    let ticket: string;
    try {
      ticket = await link.ticket(window, node || undefined);
    } catch (error) {
      if (currentEpoch !== epoch) return;
      state.value = "disconnected";
      stateMessage.value = error instanceof Error ? error.message : "Terminal connection failed.";
      return;
    }
    if (currentEpoch !== epoch) return;
    socket = new WebSocket(link.socket(window, ticket, node || undefined));
    socket.onopen = () => {
      if (currentEpoch !== epoch) return;
      stateMessage.value = "";
      sendResize();
      terminal?.focus();
    };
    socket.onmessage = (event) => {
      if (currentEpoch !== epoch) return;
      try {
        const message = JSON.parse(event.data);
        if (message.type === "ready") {
          state.value = "connected";
          return;
        }
        if (message.type === "data") terminal?.write(message.data);
      } catch {
        terminal?.write(event.data);
      }
    };
    socket.onclose = () => {
      if (currentEpoch !== epoch) return;
      state.value = "disconnected";
      stateMessage.value =
        "Terminal disconnected. Reconnect to resume this tmux window.";
    };
    socket.onerror = () => {
      if (currentEpoch !== epoch) return;
      state.value = "disconnected";
      stateMessage.value = "Terminal connection failed.";
    };
    input = terminal.onData((data) => {
      if (socket?.readyState === WebSocket.OPEN)
        socket.send(JSON.stringify({ type: "data", data }));
    });
    observer = new ResizeObserver(() => {
      fit?.fit();
      sendResize();
    });
    observer.observe(host.value);
  }

  watch([host, channel, nodeName, windowName], () => void connect(), { flush: "post" });
  onScopeDispose(disconnect);
  function refreshLayout() {
    requestAnimationFrame(() => {
      fit?.fit();
      sendResize();
      terminal?.focus();
    });
  }

  return {
    state,
    stateMessage,
    connect,
    refreshLayout,
    focus: () => terminal?.focus(),
  };
}
