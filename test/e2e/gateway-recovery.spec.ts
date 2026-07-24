import { execFile as execFileCallback } from "node:child_process";
import { mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { promisify } from "node:util";
import { expect, test, type Page } from "@playwright/test";
import {
  expectTerminalConnected,
  registerAndLogin,
  startChallengeFromCatalog,
} from "./live-helpers";

const recoveryTest = process.env.RUN_GATEWAY_RECOVERY_E2E === "1" ? test : test.skip;
const execFile = promisify(execFileCallback);
const projectRoot = process.cwd();
const gatewayPIDPath = "/tmp/breakfix-gateway.pid";

type ActiveEnvironment = {
  environment_id: string;
  challenge: { id: string };
  expires_at?: string;
};

async function runMake(target: string, configPath: string) {
  await execFile("make", [target], {
    cwd: projectRoot,
    env: { ...process.env, DEV_CONFIG: configPath },
  });
}

async function kubectlExists(kind: string, name: string, namespace?: string) {
  const args = ["get", kind, name];
  if (namespace) args.push("-n", namespace);
  args.push("-o", "name");
  try {
    await execFile("kubectl", args, { cwd: projectRoot });
    return true;
  } catch {
    return false;
  }
}

async function kubectlValue(name: string, jsonPath: string) {
  const { stdout } = await execFile(
    "kubectl",
    ["get", "containerenvironment", name, "-n", "breakfix-system", "-o", `jsonpath=${jsonPath}`],
    { cwd: projectRoot, encoding: "utf8" },
  );
  return stdout.trim();
}

async function gatewayOnline() {
  try {
    const response = await fetch("http://localhost:9090/api/openapi.json", {
      signal: AbortSignal.timeout(1_000),
    });
    return response.ok;
  } catch {
    return false;
  }
}

async function stopGateway() {
  const pid = Number((await readFile(gatewayPIDPath, "utf8")).trim());
  if (!Number.isInteger(pid) || pid <= 0) {
    throw new Error("gateway pid is invalid");
  }
  process.kill(pid, "SIGTERM");
  await expect.poll(gatewayOnline, { timeout: 20_000 }).toBe(false);
}

async function createRecoveryConfig() {
  const directory = await mkdtemp(join(tmpdir(), "breakfix-gateway-recovery-"));
  const source = await readFile(join(projectRoot, "breakfix-local.yaml"), "utf8");
  const config = `${source.replace(/^cooldown_minutes:.*(?:\r?\n|$)/m, "").trimEnd()}\ncooldown_minutes: 1\n`;
  const path = join(directory, "breakfix-recovery.yaml");
  await writeFile(path, config, "utf8");
  return { directory, path };
}

async function currentCleanupEnvironment(page: Page) {
  return page.evaluate(async () => {
    const response = await fetch("/api/me/space", {
      headers: { Authorization: `Bearer ${localStorage.getItem("token") ?? ""}` },
    });
    if (!response.ok) throw new Error(await response.text());
    const body = (await response.json()) as { active_environments: ActiveEnvironment[] };
    return body.active_environments.find((environment) => environment.challenge.id === "cleanup-logs") ?? null;
  });
}

recoveryTest("gateway restart expires an abandoned ready environment", async ({ page }) => {
  test.setTimeout(6 * 60_000);
  const recovery = await createRecoveryConfig();
  let environmentName = "";
  let environmentNamespace = "";
  try {
    await runMake("dev-build-gateway", recovery.path);
    await runMake("dev-start-gateway", recovery.path);
    await expect.poll(gatewayOnline, { timeout: 20_000 }).toBe(true);

    await page.setViewportSize({ width: 1440, height: 900 });
    await registerAndLogin(page);
    await startChallengeFromCatalog(page, "批量压缩旧日志");
    await expectTerminalConnected(page);

    await expect.poll(
      async () => {
        const environment = await currentCleanupEnvironment(page);
        return environment?.expires_at ? new Date(environment.expires_at).getTime() - Date.now() : 0;
      },
      { timeout: 30_000 },
    ).toBeGreaterThan(0);
    await expect.poll(
      async () => {
        const environment = await currentCleanupEnvironment(page);
        return environment?.expires_at ? new Date(environment.expires_at).getTime() - Date.now() : Infinity;
      },
      { timeout: 30_000 },
    ).toBeLessThanOrEqual(75_000);
    const environment = await currentCleanupEnvironment(page);
    expect(environment).not.toBeNull();
    environmentName = environment?.environment_id ?? "";
    expect(environmentName).not.toBe("");
    environmentNamespace = await kubectlValue(environmentName, "{.status.namespace}");
    expect(environmentNamespace).not.toBe("");

    await stopGateway();
    await expect(page.getByText("Disconnected", { exact: true })).toBeVisible({ timeout: 20_000 });
    await runMake("dev-start-gateway", recovery.path);
    await expect.poll(gatewayOnline, { timeout: 20_000 }).toBe(true);

    await expect.poll(
      () => kubectlExists("containerenvironment", environmentName, "breakfix-system"),
      { timeout: 2 * 60_000, intervals: [1_000, 2_000, 5_000] },
    ).toBe(false);
    await expect.poll(
      () => kubectlExists("namespace", environmentNamespace),
      { timeout: 30_000, intervals: [1_000, 2_000, 5_000] },
    ).toBe(false);
  } finally {
    if (environmentName) {
      await execFile(
        "kubectl",
        ["delete", "containerenvironment", environmentName, "-n", "breakfix-system", "--ignore-not-found", "--wait=false"],
        { cwd: projectRoot },
      ).catch(() => undefined);
    }
    await runMake("dev-start-gateway", join(projectRoot, "breakfix-local.yaml")).catch(() => undefined);
    await rm(recovery.directory, { recursive: true, force: true });
  }
});
