import { execFile as execFileCallback } from "node:child_process";
import { promisify } from "node:util";
import { expect, test } from "@playwright/test";
import { parse as parseYaml } from "yaml";
import {
  activeEnvironmentName,
  expectTerminalConnected,
  learningHistory,
  reconnectTerminal,
  registerAndLogin,
  runNodeRuntimeFixtureAnswer,
  startScenarioFromCatalog,
  stopScenario,
} from "../support/live-helpers";
import { nodeRuntimeFixture } from "../support/catalog-fixture";
import {
  attachRuntimeEnvironment,
  expectRuntimeEnvironmentPhase,
  expectRuntimeEnvironmentReset,
  runtimeEnvironmentResourceRefs,
  runtimeEnvironmentResetNonce,
  runtimeEnvironmentUID,
  waitForRuntimeEnvironmentDeletion,
} from "../support/e2e-platform";

const execFile = promisify(execFileCallback);
const incusRemote = process.env.BREAKFIX_E2E_INCUS_REMOTE ?? "incus-cluster";

type IncusProfile = {
  config?: Record<string, string>;
  devices?: Record<string, Record<string, string>>;
};

function resourceID(refs: Awaited<ReturnType<typeof runtimeEnvironmentResourceRefs>>, kind: string) {
  return refs.find((ref) => ref.provider === "incus" && ref.kind === kind)?.id ?? "";
}

function incusResource(name: string) {
  return `${incusRemote}:${name}`;
}

async function expectIncusRuntimeConstraints(environmentName: string) {
  const refs = await runtimeEnvironmentResourceRefs(environmentName);
  const project = resourceID(refs, "project");
  const network = resourceID(refs, "network");
  const acl = resourceID(refs, "acl");
  const profile = resourceID(refs, "profile");
  const instance = resourceID(refs, "instance:host");
  expect({ project, network, acl, profile, instance }).toEqual({
    project: expect.any(String), network: expect.any(String), acl: expect.any(String), profile: expect.any(String), instance: expect.any(String),
  });
  for (const value of [project, network, acl, profile, instance]) expect(value).not.toBe("");

  const { stdout: fingerprint } = await execFile("incus", ["config", "get", incusResource(instance), "volatile.base_image", "--project", project]);
  expect(fingerprint.trim()).toMatch(/^[a-f0-9]{64}$/);
  const { stdout: profileYAML } = await execFile("incus", ["profile", "show", incusResource(profile), "--project", project]);
  const value = parseYaml(profileYAML) as IncusProfile;
  expect(value.config).toMatchObject({
    "limits.cpu": "1",
    "limits.memory": "512MiB",
    "limits.processes": "512",
    "security.privileged": "false",
  });
  expect(value.devices?.root).toMatchObject({ type: "disk", path: "/", size: "5GiB" });
  return refs;
}

test("learner validates, resets, and stops a resource-bounded Node workspace while preserving completion", async ({ page }, testInfo) => {
  test.setTimeout(12 * 60_000);
  let scenarioID = "";
  let environmentName = "";
  let stopped = false;

  try {
    await page.setViewportSize({ width: 1440, height: 900 });
    await registerAndLogin(page);
    const scenario = await startScenarioFromCatalog(page, nodeRuntimeFixture.title);
    scenarioID = scenario.id;
    await expectTerminalConnected(page);
    environmentName = await activeEnvironmentName(page, scenarioID);
    await expectRuntimeEnvironmentPhase(environmentName, "Ready");
    const initialUID = await runtimeEnvironmentUID(environmentName);
    const initialResetNonce = await runtimeEnvironmentResetNonce(environmentName);
    const initialResources = await expectIncusRuntimeConstraints(environmentName);

    await runNodeRuntimeFixtureAnswer(page);
    await expect(page.getByText("All checkpoints complete", { exact: true })).toBeVisible({ timeout: 90_000 });

    await page.getByRole("button", { name: "Reset", exact: true }).click();
    await expect(page.getByText("Scenario reset.", { exact: true })).toBeVisible({ timeout: 5 * 60_000 });
    await expectRuntimeEnvironmentReset(environmentName, initialResetNonce + 1);
    expect(await runtimeEnvironmentUID(environmentName)).toBe(initialUID);
    expect(await runtimeEnvironmentResourceRefs(environmentName)).toEqual(initialResources);
    await reconnectTerminal(page);
    // Reset reinitializes the stable resource identity. Immutable first-pass
    // learning facts remain attached to that same environment UID.
    await expect(page.getByText("All checkpoints complete", { exact: true })).toBeVisible();

    page.once("dialog", (dialog) => dialog.accept());
    await page.getByRole("button", { name: "Stop", exact: true }).click();
    await expect(page.getByText("Environment stopped.", { exact: true })).toBeVisible();
    await waitForRuntimeEnvironmentDeletion(environmentName);
    stopped = true;
    await expect(page.getByRole("article", { name: `Scenario: ${scenario.title}`, exact: true })).toBeVisible();

    await page.getByRole("button", { name: "My space", exact: true }).click();
    await page.getByRole("button", { name: "Learning", exact: true }).first().click();
    await expect(page.getByRole("heading", { name: scenario.title, exact: true })).toHaveCount(1);
    await expect.poll(async () => {
      const items = await learningHistory(page);
      const attempts = items.filter((item) => item.scenario.id === scenarioID && item.scenario.runtime === "node");
      return attempts.map((item) => item.state).sort().join(",");
    }, { timeout: 30_000, intervals: [500, 1_000, 2_000] }).toBe("completed");
    const historyRecord = page.locator("article.history-row").filter({
      has: page.getByRole("heading", { name: scenario.title, exact: true }),
    });
    await expect(historyRecord.locator("p").filter({ hasText: /^Completed/ })).toBeVisible();
  } finally {
    if (environmentName) await attachRuntimeEnvironment(testInfo, environmentName);
    if (scenarioID && !stopped) {
      await stopScenario(page, scenarioID).catch(() => undefined);
      if (environmentName) await waitForRuntimeEnvironmentDeletion(environmentName);
    }
  }
});
