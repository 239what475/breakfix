import { onScopeDispose, ref } from "vue";
import { api, isLoggedIn } from "../../api/client";

type Notify = (text: string, kind?: "error" | "info") => void;

const environmentPollInterval = 2_000;

function errorMessage(error: unknown, fallback: string) {
  return error instanceof Error && error.message ? error.message : fallback;
}

// One practice session at a time: clicking a practice starts (or resumes) its
// temporary environment, the panel polls the environment phase while the
// start request is in flight, and the terminal attaches once the environment
// is Ready. Closing the panel only detaches; the environment is reclaimed by
// its idle TTL, and practices keep no learning record.
export function usePracticeSession(notify: Notify, requestAuth: () => void) {
  const practiceId = ref<string | null>(null);
  const phase = ref("");
  const starting = ref(false);
  const stopping = ref(false);
  const resetting = ref(false);
  const environmentReady = ref(false);
  const runtime = ref<"node" | "k8s">("k8s");
  const nodes = ref<{ name: string; title: string }[]>([]);
  const pendingStartId = ref<string | null>(null);
  let pollTimer: number | undefined;
  let pollEpoch = 0;

  function stopPolling() {
    pollEpoch += 1;
    if (pollTimer !== undefined) {
      window.clearTimeout(pollTimer);
      pollTimer = undefined;
    }
  }

  function adoptEnvironment(environment: { phase: string; runtime: string; nodes: string[] }) {
    phase.value = environment.phase;
    runtime.value = environment.runtime === "k8s" ? "k8s" : "node";
    nodes.value = environment.nodes.map((name) => ({ name, title: name }));
  }

  // While the blocking start request is in flight the panel polls the
  // environment so the reader sees Pending → Provisioning → Ready.
  async function pollEnvironmentOnce(id: string, epoch: number) {
    try {
      const environment = await api.getDocumentationPracticeEnvironment(id);
      if (epoch !== pollEpoch || practiceId.value !== id) return;
      adoptEnvironment(environment);
    } catch {
      /* the environment may not exist yet; keep polling while starting */
    }
    if (epoch !== pollEpoch || practiceId.value !== id) return;
    if (environmentReady.value || !starting.value) return;
    pollTimer = window.setTimeout(() => void pollEnvironmentOnce(id, epoch), environmentPollInterval);
  }

  async function discardCurrent(message?: string) {
    const previous = practiceId.value;
    practiceId.value = null;
    environmentReady.value = false;
    starting.value = false;
    phase.value = "";
    nodes.value = [];
    stopPolling();
    if (!previous) return;
    try {
      await api.stopDocumentationPracticeEnvironment(previous);
      if (message) notify(message, "info");
    } catch {
      // The previous environment may already be gone; its idle TTL reclaims
      // it either way, so a failed stop is not surfaced to the reader.
    }
  }

  async function start(id: string) {
    if (practiceId.value === id) return;
    if (practiceId.value) {
      await discardCurrent("The previous practice session was stopped.");
    }
    practiceId.value = id;
    starting.value = true;
    environmentReady.value = false;
    phase.value = "Pending";
    const epoch = ++pollEpoch;
    pollTimer = window.setTimeout(() => void pollEnvironmentOnce(id, epoch), environmentPollInterval);
    try {
      const environment = await api.startDocumentationPracticeEnvironment(id);
      if (epoch !== pollEpoch || practiceId.value !== id) return;
      adoptEnvironment(environment);
      environmentReady.value = true;
      starting.value = false;
    } catch (error) {
      if (epoch !== pollEpoch || practiceId.value !== id) return;
      stopPolling();
      starting.value = false;
      environmentReady.value = false;
      practiceId.value = null;
      phase.value = "";
      notify(errorMessage(error, "Unable to start the practice environment."), "error");
    }
  }

  // The practice entry is the session entry: an anonymous reader is sent to
  // the auth dialog and the start resumes once the login lands.
  function startFor(id: string) {
    if (!isLoggedIn()) {
      pendingStartId.value = id;
      requestAuth();
      return;
    }
    void start(id);
  }

  async function resumeAfterAuth() {
    const id = pendingStartId.value;
    pendingStartId.value = null;
    if (id && isLoggedIn() && !practiceId.value) await start(id);
  }

  async function stop() {
    const id = practiceId.value;
    if (!id || stopping.value) return;
    // Optimistic: the panel leaves the ready state right away while the
    // server drains and deletes the environment in the background.
    notify("Practice environment stop requested.", "info");
    if (practiceId.value === id) detach();
    try {
      await api.stopDocumentationPracticeEnvironment(id);
    } catch (error) {
      notify(errorMessage(error, "Unable to stop the practice environment."), "error");
    }
  }

  async function reset() {
    const id = practiceId.value;
    if (!id || resetting.value) return;
    resetting.value = true;
    try {
      await api.resetDocumentationPracticeEnvironment(id);
      notify("Practice environment reset.", "info");
    } catch (error) {
      notify(errorMessage(error, "Unable to reset the practice environment."), "error");
    } finally {
      resetting.value = false;
    }
  }

  // Closing the panel only detaches: the terminal disconnects with the panel
  // and the environment is reclaimed by its idle TTL. Starting the same
  // practice again finds the existing environment and reattaches.
  function detach() {
    stopPolling();
    practiceId.value = null;
    environmentReady.value = false;
    starting.value = false;
    phase.value = "";
    nodes.value = [];
  }

  onScopeDispose(stopPolling);

  return {
    practiceId,
    phase,
    starting,
    stopping,
    resetting,
    environmentReady,
    runtime,
    nodes,
    start,
    startFor,
    resumeAfterAuth,
    stop,
    reset,
    detach,
  };
}
