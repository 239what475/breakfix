import { computed, onMounted, ref, watch } from "vue";
import { api, clearToken, isLoggedIn } from "../../api/client";
import type { Challenge } from "../../api/types";

type Notice = (message: string, kind?: "error" | "info") => void;

// Owns the catalog selection and the one active workspace. The app shell only
// composes feature views and dialogs; it does not implement environment flow.
export function useChallengeSession(notify: Notice) {
  const loggedIn = ref(isLoggedIn());
  const loading = ref(false);
  const challenges = ref<Challenge[]>([]);
  const selectedId = ref<string | null>(null);
  const workspaceId = ref<string | null>(null);

  const selected = computed(
    () =>
      challenges.value.find((challenge) => challenge.id === selectedId.value) ??
      null,
  );
  const workspace = computed(
    () =>
      challenges.value.find(
        (challenge) => challenge.id === workspaceId.value,
      ) ?? null,
  );

  async function loadChallenges(silent = false) {
    loading.value = true;
    try {
      challenges.value = (await api.listChallenges()).challenges ?? [];
      if (
        !selectedId.value ||
        !challenges.value.some((challenge) => challenge.id === selectedId.value)
      ) {
        selectedId.value = challenges.value[0]?.id ?? null;
      }
    } catch (err) {
      if (!silent) {
        notify(
          err instanceof Error ? err.message : "Unable to load challenges",
          "error",
        );
      }
    } finally {
      loading.value = false;
    }
  }

  function authenticated(name: string) {
    loggedIn.value = true;
    notify(`Signed in as ${name}`);
    void loadChallenges();
  }

  function logout() {
    clearToken();
    loggedIn.value = false;
    workspaceId.value = null;
    notify("Signed out");
    void loadChallenges(true);
  }

  async function openWorkspace(openAuth: () => void) {
    if (!loggedIn.value) {
      openAuth();
      return;
    }
    if (!selected.value) return;
    try {
      await api.startChallenge(selected.value.id);
      workspaceId.value = selected.value.id;
      await loadChallenges(true);
    } catch (err) {
      notify(
        err instanceof Error ? err.message : "Unable to start challenge",
        "error",
      );
    }
  }

  function closeWorkspace() {
    workspaceId.value = null;
  }

  function selectChallenge(id: string) {
    selectedId.value = id;
  }

  watch(loggedIn, () => void loadChallenges(true));
  onMounted(() => void loadChallenges());

  return {
    loggedIn,
    loading,
    challenges,
    selectedId,
    workspace,
    selected,
    loadChallenges,
    authenticated,
    logout,
    openWorkspace,
    closeWorkspace,
    selectChallenge,
  };
}
