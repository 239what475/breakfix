import { execFile as execFileCallback } from "node:child_process";
import { promisify } from "node:util";
import { expect, type TestInfo } from "@playwright/test";

const execFile = promisify(execFileCallback);
const namespace = process.env.BREAKFIX_NAMESPACE ?? process.env.BREAKFIX_E2E_NAMESPACE ?? "breakfix-system";

type NodeEnvironment = {
  metadata?: { name?: string; uid?: string };
  status?: { environment?: { phase?: string } };
};

async function kubectl(args: string[]) {
  return execFile("kubectl", args, { encoding: "utf8" });
}

export async function restartDeployment(name: "breakfix-server" | "breakfix-controller") {
  await kubectl(["-n", namespace, "rollout", "restart", `deployment/${name}`]);
  await kubectl(["-n", namespace, "rollout", "status", `deployment/${name}`, "--timeout=3m"]);
}

export async function nodeEnvironment(name: string): Promise<NodeEnvironment> {
  const { stdout } = await kubectl(["-n", namespace, "get", "nodeenvironment", name, "-o", "json"]);
  return JSON.parse(stdout) as NodeEnvironment;
}

export async function nodeEnvironmentPhase(name: string): Promise<string> {
  return (await nodeEnvironment(name)).status?.environment?.phase ?? "";
}

export async function expectNodeEnvironmentPhase(name: string, expected: "Ready" | "Completed") {
  await expect.poll(() => nodeEnvironmentPhase(name), {
    timeout: 90_000,
    intervals: [500, 1_000, 2_000, 5_000],
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
