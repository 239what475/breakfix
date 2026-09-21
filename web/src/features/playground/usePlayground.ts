import { computed, onScopeDispose, ref } from "vue";
import { api } from "../../api/client";
import type { PlaygroundEnvironment } from "../../api/generated";

type Notify = (text: string, kind?: "error" | "info") => void;

export type PlaygroundState = "none" | "creating" | "ready" | "failed";

const pollInterval = 2_000;

function errorMessage(error: unknown, fallback: string) {
  return error instanceof Error && error.message ? error.message : fallback;
}

function adoptPlayground(environment: PlaygroundEnvironment): PlaygroundState {
  const state = environment.state;
  if (state === "creating" || state === "ready" || state === "failed") return state;
  return "none";
}

// One playground per user, bound to the user alone and carried across the
// whole site. The verbs are create, reset, and close; readiness arrives by
// polling, never by blocking a request, and lifecycle reclamation reads as
// none. The dock renders only for signed-in users, so every entry point here
// assumes a session.
export function usePlayground(notify: Notify) {
  const state = ref<PlaygroundState>("none");
  const environmentId = ref("");
  const runtime = ref<"node" | "k8s">("k8s");
  const starting = ref(false);
  const stopping = ref(false);
  const resetting = ref(false);
  let pollTimer: number | undefined;
  let pollEpoch = 0;

  const sessionOpen = computed(() => state.value !== "none");

  function stopPolling() {
    pollEpoch += 1;
    if (pollTimer !== undefined) {
      window.clearTimeout(pollTimer);
      pollTimer = undefined;
    }
  }

  function adopt(environment: PlaygroundEnvironment) {
    state.value = adoptPlayground(environment);
    environmentId.value = environment.environment_id ?? "";
    if (environment.runtime === "node") runtime.value = "node";
    else runtime.value = "k8s";
  }

  function adoptAndSchedule(environment: PlaygroundEnvironment, epoch: number) {
    adopt(environment);
    if (epoch !== pollEpoch) return;
    if (state.value === "creating") {
      pollTimer = window.setTimeout(() => void pollOnce(epoch), pollInterval);
    }
  }

  async function pollOnce(epoch: number) {
    try {
      const environment = await api.getPlayground();
      adoptAndSchedule(environment, epoch);
    } catch {
      if (epoch !== pollEpoch) return;
      pollTimer = window.setTimeout(() => void pollOnce(epoch), pollInterval);
    }
  }

  // The initial read reconciles a session started anywhere on the site: a
  // creating session keeps polling here, a ready one just reattaches.
  async function refresh() {
    const epoch = ++pollEpoch;
    try {
      const environment = await api.getPlayground();
      adoptAndSchedule(environment, epoch);
    } catch {
      /* no session yet or the state read failed; the ball stays at none */
    }
  }

  async function create() {
    if (starting.value || state.value === "creating" || state.value === "ready") return;
    starting.value = true;
    const epoch = ++pollEpoch;
    try {
      const environment = await api.createPlayground();
      if (epoch !== pollEpoch) return;
      adoptAndSchedule(environment, epoch);
    } catch (error) {
      if (epoch !== pollEpoch) return;
      stopPolling();
      notify(errorMessage(error, "Unable to start the playground."), "error");
    } finally {
      starting.value = false;
    }
  }

  async function reset() {
    if (resetting.value) return;
    resetting.value = true;
    const epoch = ++pollEpoch;
    try {
      const environment = await api.resetPlayground();
      if (epoch !== pollEpoch) return;
      adoptAndSchedule(environment, epoch);
      notify("Playground reset.", "info");
    } catch (error) {
      if (epoch !== pollEpoch) return;
      notify(errorMessage(error, "Unable to reset the playground."), "error");
    } finally {
      resetting.value = false;
    }
  }

  async function close() {
    if (stopping.value) return;
    stopping.value = true;
    stopPolling();
    // Optimistic: the dock leaves the session right away while the server
    // drains and deletes the environment in the background.
    state.value = "none";
    environmentId.value = "";
    try {
      await api.closePlayground();
    } catch (error) {
      notify(errorMessage(error, "Unable to close the playground."), "error");
      await refresh();
    } finally {
      stopping.value = false;
    }
  }

  onScopeDispose(stopPolling);

  return {
    state,
    environmentId,
    runtime,
    starting,
    stopping,
    resetting,
    sessionOpen,
    refresh,
    create,
    reset,
    close,
  };
}
