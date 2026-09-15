import { expect, test } from "@playwright/test";
import {
  activeEnvironmentName,
  expectTerminalConnected,
  registerAndLogin,
  startScenarioFromCatalog,
  stopScenario,
} from "../support/live-helpers";
import { nodeRuntimeFixture } from "../support/catalog-fixture";
import {
  attachRuntimeEnvironment,
  expectNoRuntimeEnvironments,
  expectRuntimeEnvironmentPhase,
  requestRuntimeEnvironmentRelease,
  waitForRuntimeEnvironmentDeletion,
} from "../support/e2e-platform";

test("released Node runtime is asynchronously reaped", async ({ page }, testInfo) => {
  test.setTimeout(8 * 60_000);
  let scenarioID = "";
  let environmentName = "";
  let reclaimed = false;

  try {
    await page.setViewportSize({ width: 1440, height: 900 });
    await registerAndLogin(page);
    const scenario = await startScenarioFromCatalog(page, nodeRuntimeFixture.title);
    scenarioID = scenario.id;
    await expectTerminalConnected(page);
    environmentName = await activeEnvironmentName(page, scenarioID);
    await expectRuntimeEnvironmentPhase(environmentName, "Ready");

    // Releasing the Server-owned lease drives the same Controller drain and
    // Reaper path used after a lifecycle deadline.
    await page.getByRole("button", { name: "My space", exact: true }).click();
    await expect(page.getByRole("heading", { name: "Your learning space", exact: true })).toBeVisible();
    await requestRuntimeEnvironmentRelease(environmentName);
    await waitForRuntimeEnvironmentDeletion(environmentName);
    reclaimed = true;
    await expectNoRuntimeEnvironments();

  } finally {
    if (environmentName) await attachRuntimeEnvironment(testInfo, environmentName);
    if (scenarioID && !reclaimed) {
      await stopScenario(page, scenarioID).catch(() => undefined);
      if (environmentName) await waitForRuntimeEnvironmentDeletion(environmentName);
    }
  }
});
