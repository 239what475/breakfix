import { onScopeDispose, ref, watch, type Ref } from "vue";
import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import { token } from "../../api/client";
import "@xterm/xterm/css/xterm.css";

type TerminalState = "idle" | "connecting" | "connected" | "disconnected";

export function useTerminalSession(
  host: Readonly<Ref<HTMLDivElement | undefined>>,
  challengeId: Readonly<Ref<string | null>>,
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

  function connect() {
    disconnect();
    if (!host.value || !challengeId.value || !windowName.value) {
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
    const protocol = location.protocol === "https:" ? "wss:" : "ws:";
    const auth = token();
    const query = new URLSearchParams({ window: windowName.value });
    if (auth) query.set("token", auth);
    socket = new WebSocket(
      `${protocol}//${location.host}/api/challenges/${challengeId.value}/terminal?${query.toString()}`,
    );
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
        if (message.type === "data") terminal?.write(message.data);
      } catch {
        terminal?.write(event.data);
      }
      // A WebSocket may be open before tmux has attached the shell. Reporting
      // Connected only after its first bytes arrive prevents early keystrokes
      // from being lost during a workspace re-entry.
      if (state.value === "connecting") state.value = "connected";
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

  watch([host, challengeId, windowName], connect, { flush: "post" });
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
