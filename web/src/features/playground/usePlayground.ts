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

  // A reset is generational: the click pins the environment's identity and
  // generation, and a ready read only counts once the reported generation has
  // moved past the pin. A ready that still carries the clicked generation is
  // the pre-wipe session — the controller has not adopted the wipe yet — so
  // the loop keeps waiting instead of flashing the old session back.
  let resetWipePending: { environmentId: string; generation: number } | null = null;

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

  function staleReady(environment: PlaygroundEnvironment) {
    return (
      resetWipePending !== null &&
      environment.environment_id === resetWipePending.environmentId &&
      (environment.generation ?? 0) <= resetWipePending.generation
    );
  }

  function adoptAndSchedule(environment: PlaygroundEnvironment, epoch: number) {
    adopt(environment);
    if (epoch !== pollEpoch) return;
    if (state.value === "ready" && staleReady(environment)) {
      // The pre-wipe generation is still answering: the ball keeps showing
      // creating until a ready belongs to the wiped generation.
      state.value = "creating";
      pollTimer = window.setTimeout(() => void pollOnce(epoch), pollInterval);
      return;
    }
    if (state.value === "ready") {
      resetWipePending = null;
      return;
    }
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
      // The capacity gate answers 429 with a stable message; everything else
      // surfaces verbatim. The button stays retryable either way.
      const message = errorMessage(error, "Unable to start the playground.");
      notify(
        message.includes("at capacity")
          ? "The playground is at capacity. Try again once another session closes."
          : message,
        "error",
      );
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
      // The POST response is the optimistic projection, not an observation:
      // it still reports the pre-wipe generation. Pinning that generation
      // here is what makes a later ready trustworthy or stale.
      resetWipePending = {
        environmentId: environment.environment_id ?? "",
        generation: environment.generation ?? 0,
      };
      adopt(environment);
      pollTimer = window.setTimeout(() => void pollOnce(epoch), pollInterval);
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
    // drains and deletes the environment in the background. The wipe pin
    // belongs to the old session and never gates a future one.
    state.value = "none";
    environmentId.value = "";
    resetWipePending = null;
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
