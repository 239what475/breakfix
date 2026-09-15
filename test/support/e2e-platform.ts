import { execFile as execFileCallback } from "node:child_process";
import { promisify } from "node:util";
import { expect, type TestInfo } from "@playwright/test";

const execFile = promisify(execFileCallback);
const namespace = process.env.BREAKFIX_NAMESPACE ?? process.env.BREAKFIX_E2E_NAMESPACE ?? "breakfix-system";

type RuntimeResourceReference = {
	provider?: string;
	kind?: string;
	id?: string;
};

type RuntimeEnvironment = {
	metadata?: { name?: string; uid?: string; annotations?: Record<string, string> };
	spec?: { resetNonce?: number };
	status?: {
		phase?: string;
		operation?: string;
		runtime?: { resourceRefs?: RuntimeResourceReference[] };
		progress?: { reportRef?: { id?: string; digest?: string } };
	};
};

async function kubectl(args: string[]) {
  return execFile("kubectl", args, { encoding: "utf8" });
}

export async function restartDeployment(name: "breakfix-server" | "breakfix-controller") {
  await kubectl(["-n", namespace, "rollout", "restart", `deployment/${name}`]);
  await kubectl(["-n", namespace, "rollout", "status", `deployment/${name}`, "--timeout=3m"]);
}

// The interruption acceptance temporarily changes the Server ConfigMap. The
// production deployment has no runtime configuration mutation API; this
// helper is deliberately test infrastructure and always restarts the Server
// because configuration is loaded only during bootstrap.
export async function setServerAuthoringDeadline(deadline: string) {
  const value = deadline.trim();
  if (!value) throw new Error("authoring deadline must not be empty");
  const { stdout: deployment } = await kubectl([
    "-n", namespace, "get", "deployment", "breakfix-server", "-o", "json",
  ]);
  const configMap = (JSON.parse(deployment) as {
    spec?: { template?: { spec?: { volumes?: Array<{ name?: string; configMap?: { name?: string } }> } } };
  }).spec?.template?.spec?.volumes?.find((volume) => volume.name === "config")?.configMap?.name;
  if (!configMap) throw new Error("breakfix-server config ConfigMap is missing");

  const { stdout: configJSON } = await kubectl(["-n", namespace, "get", "configmap", configMap, "-o", "json"]);
  const config = (JSON.parse(configJSON) as { data?: Record<string, string> }).data?.["config.yaml"];
  if (!config) throw new Error("breakfix-server config ConfigMap has no config.yaml");
  const lines = config.split("\n");
  let replacements = 0;
  const patched = lines.map((line) => {
    if (!/^\s*authoring_run_deadline:\s*/.test(line)) return line;
    replacements += 1;
    return `  authoring_run_deadline: ${value}`;
  }).join("\n");
  if (replacements !== 1) throw new Error(`expected one authoring_run_deadline, found ${replacements}`);
  await kubectl([
    "-n", namespace, "patch", "configmap", configMap, "--type", "merge", "--patch",
    JSON.stringify({ data: { "config.yaml": patched } }),
  ]);
  await restartDeployment("breakfix-server");
}

export async function runtimeEnvironment(name: string): Promise<RuntimeEnvironment> {
	const { stdout } = await kubectl(["-n", namespace, "get", "runtimeenvironment", name, "-o", "json"]);
	return JSON.parse(stdout) as RuntimeEnvironment;
}

export async function runtimeEnvironmentPhase(name: string): Promise<string> {
	return (await runtimeEnvironment(name)).status?.phase ?? "";
}

export async function runtimeEnvironmentUID(name: string): Promise<string> {
	return (await runtimeEnvironment(name)).metadata?.uid ?? "";
}

export async function runtimeEnvironmentResetNonce(name: string): Promise<number> {
	return (await runtimeEnvironment(name)).spec?.resetNonce ?? 0;
}

export async function runtimeEnvironmentResourceRefs(name: string): Promise<RuntimeResourceReference[]> {
	return (await runtimeEnvironment(name)).status?.runtime?.resourceRefs ?? [];
}

export async function expectRuntimeEnvironmentPhase(name: string, expected: "Ready") {
	await expect.poll(() => runtimeEnvironmentPhase(name), {
		timeout: 10 * 60_000,
		intervals: [500, 1_000, 2_000, 5_000],
	}).toBe(expected);
}

export async function expectRuntimeEnvironmentReset(name: string, nonce: number) {
	await expect.poll(async () => {
		const environment = await runtimeEnvironment(name);
		return environment.spec?.resetNonce === nonce &&
			environment.metadata?.annotations?.["breakfix.dev/observed-reset-nonce"] === String(nonce) &&
			environment.status?.phase === "Ready" && environment.status?.operation === "None";
	}, {
		timeout: 10 * 60_000,
		intervals: [500, 1_000, 2_000, 5_000],
	}).toBe(true);
}

// The Server owns normal lease renewal. This helper deliberately bypasses the
// API to exercise Controller drain and asynchronous reaping on the E2E target.
export async function requestRuntimeEnvironmentRelease(name: string) {
	const releaseAt = new Date(Date.now() - 60_000).toISOString();
	await kubectl([
		"-n", namespace, "patch", "runtimeenvironment", name, "--type", "merge", "--patch",
		JSON.stringify({ spec: { lease: { releaseAt } } }),
	]);
}

// Reaper work is durable. Pausing the Controller before release proves that a
// restarted controller acquires the queued work rather than relying on an
// in-memory completion callback.
export async function scaleController(replicas: 0 | 1) {
	await kubectl(["-n", namespace, "scale", "deployment/breakfix-controller", `--replicas=${replicas}`]);
	if (replicas === 1) {
		await kubectl(["-n", namespace, "rollout", "status", "deployment/breakfix-controller", "--timeout=3m"]);
		return;
	}
	await expect.poll(async () => {
		const { stdout } = await kubectl(["-n", namespace, "get", "deployment", "breakfix-controller", "-o", "json"]);
		return (JSON.parse(stdout) as { status?: { replicas?: number } }).status?.replicas ?? 0;
	}, {
		timeout: 3 * 60_000,
		intervals: [500, 1_000, 2_000, 5_000],
	}).toBe(0);
}

export async function waitForRuntimeEnvironmentDeletion(name: string) {
  await expect.poll(async () => {
		const { stdout } = await kubectl(["-n", namespace, "get", "runtimeenvironment", name, "--ignore-not-found", "-o", "name"]);
    return stdout.trim() === "";
  }, {
    timeout: 5 * 60_000,
    intervals: [1_000, 2_000, 5_000],
	}).toBe(true);
}

export async function expectNoRuntimeEnvironments() {
	await expect.poll(async () => {
		const { stdout } = await kubectl(["-n", namespace, "get", "runtimeenvironment", "-o", "name"]);
		return stdout.trim();
	}, {
		timeout: 5 * 60_000,
		intervals: [1_000, 2_000, 5_000],
	}).toBe("");
}

export async function attachRuntimeEnvironment(testInfo: TestInfo, name: string) {
  try {
		const { stdout } = await kubectl(["-n", namespace, "get", "runtimeenvironment", name, "-o", "yaml"]);
		await testInfo.attach(`runtimeenvironment-${name}`, { body: stdout, contentType: "text/yaml" });
		console.info(`Breakfix E2E runtimeenvironment: ${namespace}/${name}`);
  } catch (error) {
		console.info(`Breakfix E2E runtimeenvironment unavailable: ${namespace}/${name}: ${String(error)}`);
  }
}
