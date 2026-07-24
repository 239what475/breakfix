import { execFile as execFileCallback } from "node:child_process";
import { mkdir, mkdtemp, readFile, rm, symlink, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { promisify } from "node:util";
import { expect, test, type Page } from "@playwright/test";
import {
  expectTerminalConnected,
  registerAndLogin,
  startChallengeFromCatalog,
} from "./live-helpers";

const recoveryTest = process.env.RUN_SERVER_RECOVERY_E2E === "1" ? test : test.skip;
const execFile = promisify(execFileCallback);
const projectRoot = resolve(dirname(fileURLToPath(import.meta.url)), "../..");
const serverPIDPath = "/tmp/breakfix-server.pid";
const controllerPIDPath = "/tmp/breakfix-controller.pid";

type ActiveEnvironment = {
  environment_id: string;
  challenge: { id: string };
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

async function serverOnline() {
  try {
    const response = await fetch("http://localhost:9090/api/openapi.json", {
      signal: AbortSignal.timeout(1_000),
    });
    return response.ok;
  } catch {
    return false;
  }
}

async function controllerOnline() {
  try {
    const response = await fetch("http://localhost:8081/healthz", {
      signal: AbortSignal.timeout(1_000),
    });
    return response.ok;
  } catch {
    return false;
  }
}

async function stopServer() {
	await stopProcess(serverPIDPath, serverOnline, "server");
}

async function stopController() {
	await stopProcess(controllerPIDPath, controllerOnline, "controller");
}

async function stopProcess(pidPath: string, online: () => Promise<boolean>, name: string) {
	const pid = Number((await readFile(pidPath, "utf8")).trim());
	if (!Number.isInteger(pid) || pid <= 0) {
		throw new Error(`${name} pid is invalid`);
	}
	process.kill(pid, "SIGTERM");
	await expect.poll(online, { timeout: 20_000 }).toBe(false);
}

async function createRecoveryConfig() {
  const directory = await mkdtemp(join(tmpdir(), "breakfix-server-recovery-"));
  const dataDir = join(directory, "data");
  await mkdir(dataDir, { recursive: true });
  await symlink(resolve(projectRoot, "data", "challenges"), join(dataDir, "challenges"), "dir");
  const localConfigPath = join(projectRoot, "config", "breakfix.local.yaml");
  await runMake("dev-config", localConfigPath);
  const source = await readFile(localConfigPath, "utf8");
  const config = `${source
    .replace(/^data_dir:.*(?:\r?\n|$)/m, `data_dir: ${dataDir}\n`)
    .replace(/^cooldown_minutes:.*(?:\r?\n|$)/m, "")
    .trimEnd()}\ncooldown_minutes: 1\n`;
  const path = join(directory, "breakfix-recovery.yaml");
  await writeFile(path, config, "utf8");
  return { directory, path };
}

async function startRuntime(configPath: string) {
  await runMake("dev-build", configPath);
  await runMake("dev-start-controller", configPath);
  await runMake("dev-start-server", configPath);
  await expect.poll(controllerOnline, { timeout: 20_000 }).toBe(true);
  await expect.poll(serverOnline, { timeout: 20_000 }).toBe(true);
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

async function startCleanupEnvironment(page: Page) {
  await page.setViewportSize({ width: 1440, height: 900 });
	await registerAndLogin(page);
	await startChallengeFromCatalog(page, "批量压缩旧日志");
	await expectTerminalConnected(page);
	await expect.poll(() => currentCleanupEnvironment(page), { timeout: 30_000 }).not.toBeNull();
	const environment = await currentCleanupEnvironment(page);
	if (environment === null) throw new Error("cleanup-logs environment was not created");
	return environment;
}

recoveryTest("server restart leaves controller reconciliation active", async ({ page }) => {
  test.setTimeout(8 * 60_000);
  const recovery = await createRecoveryConfig();
  let environmentName = "";
  let environmentNamespace = "";
  try {
    await startRuntime(recovery.path);
    const environment = await startCleanupEnvironment(page);
    environmentName = environment?.environment_id ?? "";
    expect(environmentName).not.toBe("");
    environmentNamespace = await kubectlValue(environmentName, "{.status.namespace}");
    expect(environmentNamespace).not.toBe("");

    await stopServer();
    await expect.poll(controllerOnline, { timeout: 20_000 }).toBe(true);
    await expect.poll(() => kubectlValue(environmentName, "{.status.phase}"), {
      timeout: 2 * 60_000,
      intervals: [1_000, 2_000, 5_000],
    }).toBe("Draining");
    await expect.poll(() => kubectlValue(environmentName, "{.status.phase}"), {
      timeout: 2 * 60_000,
      intervals: [1_000, 2_000, 5_000],
    }).toBe("Destroyed");

    await runMake("dev-start-server", recovery.path);
    await expect.poll(serverOnline, { timeout: 20_000 }).toBe(true);
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
    await runMake("dev-start-controller", join(projectRoot, "config", "breakfix.local.yaml")).catch(() => undefined);
    await runMake("dev-start-server", join(projectRoot, "config", "breakfix.local.yaml")).catch(() => undefined);
    await rm(recovery.directory, { recursive: true, force: true });
  }
});

recoveryTest("controller restart reconciles an existing environment", async ({ page }) => {
  test.setTimeout(5 * 60_000);
  const recovery = await createRecoveryConfig();
  let environmentName = "";
  try {
    await startRuntime(recovery.path);
    const environment = await startCleanupEnvironment(page);
    environmentName = environment?.environment_id ?? "";
    expect(environmentName).not.toBe("");

		await stopController();
		await expect.poll(serverOnline, { timeout: 20_000 }).toBe(true);
		await runMake("dev-start-controller", recovery.path);
    await expect.poll(controllerOnline, { timeout: 20_000 }).toBe(true);

    await page.getByRole("textbox", { name: "Terminal input" }).focus();
    await page.keyboard.type("/answer.sh");
    await page.keyboard.press("Enter");
    await expect(page.getByText("All checkpoints complete", { exact: true })).toBeVisible({ timeout: 90_000 });
    await expect.poll(() => kubectlValue(environmentName, "{.status.phase}"), { timeout: 30_000 }).toBe("Completed");
  } finally {
    if (environmentName) {
      await execFile(
        "kubectl",
        ["delete", "containerenvironment", environmentName, "-n", "breakfix-system", "--ignore-not-found", "--wait=false"],
        { cwd: projectRoot },
      ).catch(() => undefined);
    }
    await runMake("dev-start-controller", join(projectRoot, "config", "breakfix.local.yaml")).catch(() => undefined);
    await runMake("dev-start-server", join(projectRoot, "config", "breakfix.local.yaml")).catch(() => undefined);
    await rm(recovery.directory, { recursive: true, force: true });
  }
});
