import { onScopeDispose, ref, watch, type Ref } from "vue";
import { api } from "../../api/client";
import type { CheckpointResult } from "../../api/types";

export function useScenarioProgress(
  scenarioId: Ref<string | null>,
  enabled: Ref<boolean>,
) {
  const checks = ref<CheckpointResult[]>([]);
  const loading = ref(false);
  const error = ref("");
  let timer: number | undefined;

  function stop() {
    if (timer !== undefined) window.clearTimeout(timer);
    timer = undefined;
  }

  async function refresh() {
    if (!scenarioId.value || !enabled.value || document.hidden) return;
    loading.value = true;
    error.value = "";
    try {
      checks.value =
        (await api.getScenarioProgress(scenarioId.value)).checks ?? [];
    } catch (err) {
      error.value =
        err instanceof Error ? err.message : "Unable to run checkpoint checks";
    } finally {
      loading.value = false;
    }
  }

  function schedule() {
    stop();
    if (!scenarioId.value || !enabled.value || document.hidden) return;
    timer = window.setTimeout(async () => {
      await refresh();
      schedule();
    }, 4000);
  }

  watch(
    [scenarioId, enabled],
    async () => {
      checks.value = [];
      error.value = "";
      if (enabled.value) await refresh();
      schedule();
    },
    { immediate: true },
  );

  const visibility = () => {
    if (document.hidden) {
      stop();
      return;
    }
    void refresh().then(schedule);
  };
  document.addEventListener("visibilitychange", visibility);
  onScopeDispose(() => {
    stop();
    document.removeEventListener("visibilitychange", visibility);
  });

  return { checks, loading, error, refresh };
}
