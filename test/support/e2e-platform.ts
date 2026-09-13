import { execFile as execFileCallback } from "node:child_process";
import { promisify } from "node:util";
import { expect, type TestInfo } from "@playwright/test";

const execFile = promisify(execFileCallback);
const namespace = process.env.BREAKFIX_NAMESPACE ?? process.env.BREAKFIX_E2E_NAMESPACE ?? "breakfix-system";

type NodeEnvironment = {
	metadata?: { name?: string; uid?: string };
	status?: { environment?: { phase?: string } };
};

type VK8sEnvironment = NodeEnvironment;

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

export async function nodeEnvironment(name: string): Promise<NodeEnvironment> {
  const { stdout } = await kubectl(["-n", namespace, "get", "nodeenvironment", name, "-o", "json"]);
  return JSON.parse(stdout) as NodeEnvironment;
}

export async function nodeEnvironmentPhase(name: string): Promise<string> {
	return (await nodeEnvironment(name)).status?.environment?.phase ?? "";
}

export async function nodeEnvironmentUID(name: string): Promise<string> {
	return (await nodeEnvironment(name)).metadata?.uid ?? "";
}

export async function expectNodeEnvironmentPhase(name: string, expected: "Ready" | "Completed") {
  await expect.poll(() => nodeEnvironmentPhase(name), {
    timeout: 90_000,
    intervals: [500, 1_000, 2_000, 5_000],
  }).toBe(expected);
}

export async function vk8sEnvironment(name: string): Promise<VK8sEnvironment> {
	const { stdout } = await kubectl(["-n", namespace, "get", "vk8senvironment", name, "-o", "json"]);
	return JSON.parse(stdout) as VK8sEnvironment;
}

export async function vk8sEnvironmentPhase(name: string): Promise<string> {
	return (await vk8sEnvironment(name)).status?.environment?.phase ?? "";
}

export async function expectVK8sEnvironmentPhase(name: string, expected: "Ready" | "Completed") {
	await expect.poll(() => vk8sEnvironmentPhase(name), {
		timeout: 10 * 60_000,
		intervals: [1_000, 2_000, 5_000],
	}).toBe(expected);
}

export async function waitForNodeEnvironmentDeletion(name: string) {
	await waitForEnvironmentDeletion("nodeenvironment", name);
}

export async function waitForEnvironmentDeletion(kind: "nodeenvironment" | "vk8senvironment", name: string) {
  await expect.poll(async () => {
    const { stdout } = await kubectl(["-n", namespace, "get", kind, name, "--ignore-not-found", "-o", "name"]);
    return stdout.trim() === "";
  }, {
    timeout: 5 * 60_000,
    intervals: [1_000, 2_000, 5_000],
  }).toBe(true);
}

export async function attachEnvironmentIdentity(
  testInfo: TestInfo,
  kind: "nodeenvironment" | "vk8senvironment",
  name: string,
) {
  try {
    const { stdout } = await kubectl(["-n", namespace, "get", kind, name, "-o", "yaml"]);
    await testInfo.attach(`${kind}-${name}`, { body: stdout, contentType: "text/yaml" });
    console.info(`Breakfix E2E ${kind}: ${namespace}/${name}`);
  } catch (error) {
    console.info(`Breakfix E2E ${kind} unavailable: ${namespace}/${name}: ${String(error)}`);
  }
}

export async function attachNodeEnvironmentIdentity(testInfo: TestInfo, name: string) {
  await attachEnvironmentIdentity(testInfo, "nodeenvironment", name);
}
