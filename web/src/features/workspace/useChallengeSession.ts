import { computed, onMounted, ref, watch } from "vue";
import { api, clearToken, isLoggedIn, tokenUserName } from "../../api/client";
import type { Challenge } from "../../api/types";

type Notice = (message: string, kind?: "error" | "info") => void;

// Owns challenge loading and the one visible workspace. Catalog filtering and
// scroll state stay in the catalog page while it is hidden behind a workspace.
export function useChallengeSession(notify: Notice) {
	const loggedIn = ref(isLoggedIn());
	const accountName = ref(tokenUserName());
	const loading = ref(false);
	const challenges = ref<Challenge[]>([]);
	const workspaceId = ref<string | null>(null);
	const startingId = ref<string | null>(null);
	const pendingStartId = ref<string | null>(null);
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
		accountName.value = name;
		notify(`Signed in as ${name}`);
		void (async () => {
			await loadChallenges();
			const challengeID = pendingStartId.value;
			pendingStartId.value = null;
			if (challengeID) await activateChallenge(challengeID);
		})();
	}

  function logout() {
		clearToken();
		loggedIn.value = false;
		accountName.value = undefined;
		workspaceId.value = null;
		pendingStartId.value = null;
		notify("Signed out");
		void loadChallenges(true);
	}

	async function activateChallenge(id: string) {
		startingId.value = id;
		try {
			await api.startChallenge(id);
			workspaceId.value = id;
			await loadChallenges(true);
			return true;
		} catch (err) {
			notify(
				err instanceof Error ? err.message : "Unable to start challenge",
				"error",
			);
			return false;
		} finally {
			startingId.value = null;
		}
	}

	async function startChallenge(id: string, openAuth: () => void) {
		if (!loggedIn.value) {
			pendingStartId.value = id;
			openAuth();
			return false;
		}
		return activateChallenge(id);
	}

	function closeWorkspace() {
		workspaceId.value = null;
		void loadChallenges(true);
	}

	watch(loggedIn, () => void loadChallenges(true));
  onMounted(() => void loadChallenges());

  return {
		loggedIn,
		accountName,
		loading,
		challenges,
		workspace,
		startingId,
		loadChallenges,
		authenticated,
		logout,
		startChallenge,
		closeWorkspace,
	};
}
