import { onScopeDispose, ref, watch, type Ref } from "vue";
import { api, type MySpaceLearningQuery } from "../../api/client";
import type { MySpace, MySpaceLearningHistory } from "../../api/types";

export type LearningStateFilter = "all" | "active" | "completed" | "ended";
export type LearningRuntimeFilter = "all" | "node" | "k8s";

const activeEnvironmentRefreshInterval = 3_000;

export function useMySpace(
  active: Ref<boolean>,
  loggedIn: Ref<boolean>,
  stateFilter: Ref<LearningStateFilter>,
  runtimeFilter: Ref<LearningRuntimeFilter>,
) {
  const space = ref<MySpace>();
  const history = ref<MySpaceLearningHistory[]>([]);
  const nextCursor = ref<string>();
  const loading = ref(false);
  const loadingLearning = ref(false);
  const loadingMore = ref(false);
  const error = ref("");
  let historyRequest = 0;
  let refreshTimer: number | undefined;

  function stopRefreshSchedule() {
    if (refreshTimer !== undefined) window.clearTimeout(refreshTimer);
    refreshTimer = undefined;
  }

  function scheduleRefresh() {
    stopRefreshSchedule();
    const hasActiveLearning = space.value?.recent_learning.some((item) => item.state === "active");
    if (!active.value || !loggedIn.value || (!space.value?.active_environments.length && !hasActiveLearning)) return;
    refreshTimer = window.setTimeout(() => void refresh(), activeEnvironmentRefreshInterval);
  }

  function learningQuery(cursor?: string): MySpaceLearningQuery {
    return {
      cursor,
      state: stateFilter.value === "all" ? undefined : stateFilter.value,
      runtime: runtimeFilter.value === "all" ? undefined : runtimeFilter.value,
    };
  }

  async function refreshLearning() {
    const request = ++historyRequest;
    loadingLearning.value = true;
    history.value = [];
    nextCursor.value = undefined;
    try {
      const response = await api.getMySpaceLearning(learningQuery());
      if (request !== historyRequest) return;
      history.value = response.items;
      nextCursor.value = response.next_cursor ?? undefined;
    } catch (cause) {
      if (request === historyRequest) {
        error.value = cause instanceof Error ? cause.message : "Unable to load learning history";
      }
    } finally {
      if (request === historyRequest) loadingLearning.value = false;
    }
  }

  async function refresh() {
    if (!loggedIn.value) return;
    loading.value = true;
    error.value = "";
    try {
      const response = await api.getMySpace();
      space.value = response;
      await refreshLearning();
    } catch (cause) {
      error.value = cause instanceof Error ? cause.message : "Unable to load your space";
    } finally {
      loading.value = false;
      scheduleRefresh();
    }
  }

  async function loadMore() {
    if (!nextCursor.value || loadingMore.value) return;
    const cursor = nextCursor.value;
    const request = historyRequest;
    loadingMore.value = true;
    try {
      const response = await api.getMySpaceLearning(learningQuery(cursor));
      if (request !== historyRequest) return;
      history.value.push(...response.items);
      nextCursor.value = response.next_cursor ?? undefined;
    } catch (cause) {
      error.value = cause instanceof Error ? cause.message : "Unable to load more learning history";
    } finally {
      loadingMore.value = false;
    }
  }

  watch(
    [active, loggedIn],
    ([visible, signedIn], [wasVisible]) => {
      if (visible && signedIn && (!wasVisible || !space.value)) void refresh();
      if (!visible) stopRefreshSchedule();
      if (!signedIn) {
        stopRefreshSchedule();
        space.value = undefined;
        history.value = [];
        nextCursor.value = undefined;
        error.value = "";
      }
    },
    { immediate: true },
  );

  watch([stateFilter, runtimeFilter], () => {
    if (active.value && loggedIn.value) void refreshLearning();
  });

  onScopeDispose(stopRefreshSchedule);

  return { space, history, nextCursor, loading, loadingLearning, loadingMore, error, refresh, loadMore };
}
