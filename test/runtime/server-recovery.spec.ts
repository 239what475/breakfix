import { execFile as execFileCallback, spawn, type ChildProcess } from "node:child_process";
import { promisify } from "node:util";
import { expect, test, type Page } from "@playwright/test";
import {
  expectTerminalConnected,
  registerAndLogin,
  startChallengeFromCatalog,
} from "../support/live-helpers";

const recoveryTest = process.env.RUN_SERVER_RECOVERY_E2E === "1" ? test : test.skip;
const execFile = promisify(execFileCallback);
const namespace = "breakfix-system";
const serverDeployment = "breakfix-server";
const controllerDeployment = "breakfix-controller";
const serverURL = process.env.BREAKFIX_E2E_BASE_URL ?? "http://localhost:9091";
const serverPort = new URL(serverURL).port || "9091";
let portForward: ChildProcess | undefined;

type ActiveEnvironment = {
  environment_id: string;
  challenge: { id: string };
};

async function kubectl(args: string[]) {
  return execFile("kubectl", args, { encoding: "utf8" });
}

async function kubectlExists(kind: string, name: string, resourceNamespace?: string) {
  const args = ["get", kind, name];
  if (resourceNamespace) args.push("-n", resourceNamespace);
  args.push("-o", "name");
  try {
    await kubectl(args);
    return true;
  } catch {
    return false;
  }
}

async function kubectlValue(name: string, jsonPath: string) {
  const { stdout } = await kubectl([
    "get", "containerenvironment", name, "-n", namespace, "-o", `jsonpath=${jsonPath}`,
  ]);
  return stdout.trim();
}

async function environmentDeletionRequested(name: string) {
  try {
    return (await kubectlValue(name, "{.metadata.deletionTimestamp}")) !== "";
  } catch {
    return true;
  }
}

async function serverOnline() {
  try {
    const response = await fetch(`${serverURL}/api/openapi.json`, {
      signal: AbortSignal.timeout(1_000),
    });
    return response.ok;
  } catch {
    return false;
  }
}

async function startPortForward() {
  portForward?.kill("SIGTERM");
  portForward = spawn(
    "kubectl",
    ["port-forward", "-n", namespace, `service/${serverDeployment}`, `${serverPort}:9090`],
    { stdio: "ignore" },
  );
  await expect.poll(serverOnline, { timeout: 30_000 }).toBe(true);
}

async function restartDeployment(name: string) {
  await kubectl(["rollout", "restart", "deployment", name, "-n", namespace]);
  await kubectl(["rollout", "status", "deployment", name, "-n", namespace, "--timeout=3m"]);
}

async function setShortIdleLease(name: string) {
  // The environment is initially created with the normal product idle TTL.
  // Pair the test-only TTL override with a newer activity event so Controller
  // deterministically recalculates the lease from this snapshot.
  const activityAt = new Date(Date.now() + 5_000).toISOString();
  const patch = JSON.stringify({
    spec: {
      activityAt,
      timeouts: {
        idleTtlSeconds: 10,
        drainGracePeriodSeconds: 10,
      },
    },
  });
  await kubectl(["patch", "containerenvironment", name, "-n", namespace, "--type=merge", "-p", patch]);
}

async function currentCleanupEnvironment(page: Page) {
  return page.evaluate(async () => {
    const response = await fetch("/api/me/space", {
      headers: { Authorization: `Bearer ${localStorage.getItem("token") ?? ""}` },
    });
    if (!response.ok) throw new Error(await response.text());
    const body = (await response.json()) as { active_environments: ActiveEnvironment[] };
    return body.active_environments.find((environment) => environment.challenge.id === "chal-r7m4x2q9v6kp") ?? null;
  });
}

async function startCleanupEnvironment(page: Page) {
  await page.setViewportSize({ width: 1440, height: 900 });
  await registerAndLogin(page);
  await startChallengeFromCatalog(page, "批量压缩旧日志");
  await expectTerminalConnected(page);
  await expect.poll(() => currentCleanupEnvironment(page), { timeout: 30_000 }).not.toBeNull();
  const environment = await currentCleanupEnvironment(page);
  if (environment === null) throw new Error("cleanup logs environment was not created");
  return environment;
}

async function deleteEnvironment(name: string) {
  if (!name) return;
  await kubectl(["delete", "containerenvironment", name, "-n", namespace, "--ignore-not-found", "--wait=false"])
    .catch(() => undefined);
}

test.beforeAll(async () => {
  await startPortForward();
});

test.afterAll(() => {
  portForward?.kill("SIGTERM");
});

recoveryTest("server restart leaves controller reconciliation active", async ({ page }) => {
  test.setTimeout(8 * 60_000);
  let environmentName = "";
  let environmentNamespace = "";
  try {
    const environment = await startCleanupEnvironment(page);
    environmentName = environment.environment_id;
    environmentNamespace = await kubectlValue(environmentName, "{.status.namespace}");
    expect(environmentNamespace).not.toBe("");

    await setShortIdleLease(environmentName);
    await restartDeployment(serverDeployment);
    await startPortForward();

    await expect.poll(() => kubectlValue(environmentName, "{.status.phase}"), {
      timeout: 90_000,
      intervals: [1_000, 2_000, 5_000],
    }).toBe("Draining");
    await expect.poll(() => kubectlValue(environmentName, "{.status.phase}"), {
      timeout: 90_000,
      intervals: [1_000, 2_000, 5_000],
    }).toBe("Destroyed");
    await expect.poll(() => environmentDeletionRequested(environmentName), {
      timeout: 30_000,
      intervals: [1_000, 2_000, 5_000],
    }).toBe(true);
    await expect.poll(
      () => kubectlExists("containerenvironment", environmentName, namespace),
      { timeout: 90_000, intervals: [1_000, 2_000, 5_000] },
    ).toBe(false);
    await expect.poll(
      () => kubectlExists("namespace", environmentNamespace),
      { timeout: 90_000, intervals: [1_000, 2_000, 5_000] },
    ).toBe(false);
  } finally {
    await deleteEnvironment(environmentName);
  }
});

recoveryTest("controller restart reconciles an existing environment", async ({ page }) => {
  test.setTimeout(5 * 60_000);
  let environmentName = "";
  try {
    const environment = await startCleanupEnvironment(page);
    environmentName = environment.environment_id;

    await restartDeployment(controllerDeployment);
    await page.getByRole("textbox", { name: "Terminal input" }).focus();
    await page.keyboard.type("/answer.sh");
    await page.keyboard.press("Enter");
    await expect(page.getByText("All checkpoints complete", { exact: true })).toBeVisible({ timeout: 90_000 });
    await expect.poll(() => kubectlValue(environmentName, "{.status.phase}"), { timeout: 30_000 }).toBe("Completed");
  } finally {
    await deleteEnvironment(environmentName);
  }
});
