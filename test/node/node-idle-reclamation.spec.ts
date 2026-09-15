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
  scaleController,
  waitForRuntimeEnvironmentDeletion,
} from "../support/e2e-platform";

test("released Node runtime is reaped after Controller recovery", async ({ page }, testInfo) => {
  test.setTimeout(8 * 60_000);
  let scenarioID = "";
  let environmentName = "";
  let reclaimed = false;
  let controllerPaused = false;

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
    await scaleController(0);
    controllerPaused = true;
    await requestRuntimeEnvironmentRelease(environmentName);
    await scaleController(1);
    controllerPaused = false;
    await waitForRuntimeEnvironmentDeletion(environmentName);
    reclaimed = true;
    await expectNoRuntimeEnvironments();

  } finally {
    if (controllerPaused) await scaleController(1).catch(() => undefined);
    if (environmentName) await attachRuntimeEnvironment(testInfo, environmentName);
    if (scenarioID && !reclaimed) {
      await stopScenario(page, scenarioID).catch(() => undefined);
      if (environmentName) await waitForRuntimeEnvironmentDeletion(environmentName);
    }
  }
});
