import { computed, onMounted, ref, watch } from "vue";
import { api, clearToken, isLoggedIn, tokenUserName } from "../../api/client";
import type { Scenario } from "../../api/types";

type Notice = (message: string, kind?: "error" | "info") => void;

// Owns scenario loading and the one visible workspace. Catalog filtering and
// scroll state stay in the catalog page while it is hidden behind a workspace.
export function useScenarioSession(notify: Notice) {
	const loggedIn = ref(isLoggedIn());
	const accountName = ref(tokenUserName());
	const loading = ref(false);
	const scenarios = ref<Scenario[]>([]);
	const workspaceId = ref<string | null>(null);
	const startingId = ref<string | null>(null);
	const pendingStartId = ref<string | null>(null);
  const workspace = computed(
    () =>
      scenarios.value.find(
        (scenario) => scenario.id === workspaceId.value,
      ) ?? null,
  );

  async function loadScenarios(silent = false) {
    loading.value = true;
    try {
		scenarios.value = (await api.listScenarios()).scenarios ?? [];
    } catch (err) {
      if (!silent) {
        notify(
          err instanceof Error ? err.message : "Unable to load scenarios",
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
			await loadScenarios();
			const scenarioID = pendingStartId.value;
			pendingStartId.value = null;
			if (scenarioID) await activateScenario(scenarioID);
		})();
	}

  function logout() {
		clearToken();
		loggedIn.value = false;
		accountName.value = undefined;
		workspaceId.value = null;
		pendingStartId.value = null;
		notify("Signed out");
		void loadScenarios(true);
	}

	async function activateScenario(id: string) {
		startingId.value = id;
		try {
			await api.startScenario(id);
			workspaceId.value = id;
			await loadScenarios(true);
			return true;
		} catch (err) {
			notify(
				err instanceof Error ? err.message : "Unable to start scenario",
				"error",
			);
			return false;
		} finally {
			startingId.value = null;
		}
	}

	async function startScenario(id: string, openAuth: () => void) {
		if (!loggedIn.value) {
			pendingStartId.value = id;
			openAuth();
			return false;
		}
		return activateScenario(id);
	}

	function closeWorkspace() {
		workspaceId.value = null;
		void loadScenarios(true);
	}

	watch(loggedIn, () => void loadScenarios(true));
  onMounted(() => void loadScenarios());

  return {
		loggedIn,
		accountName,
		loading,
		scenarios,
		workspace,
		startingId,
		loadScenarios,
		authenticated,
		logout,
		startScenario,
		closeWorkspace,
	};
}
