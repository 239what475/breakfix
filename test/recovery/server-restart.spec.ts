import { expect, test } from "@playwright/test";
import {
  activeEnvironmentName,
  expectTerminalConnected,
  registerAndLogin,
  reconnectTerminal,
  runNodeRuntimeFixtureAnswer,
  startScenarioFromCatalog,
  stopScenario,
} from "../support/live-helpers";
import { nodeRuntimeFixture } from "../support/catalog-fixture";
import {
  attachRuntimeEnvironment,
  expectRuntimeEnvironmentPhase,
  restartDeployment,
  waitForRuntimeEnvironmentDeletion,
} from "../support/e2e-platform";

test("an existing runtime environment reconnects and completes after a Server restart", async ({ page }, testInfo) => {
  test.setTimeout(8 * 60_000);
  let scenarioID = "";
  let environmentName = "";

  try {
    await page.setViewportSize({ width: 1440, height: 900 });
    await registerAndLogin(page);
    const scenario = await startScenarioFromCatalog(page, nodeRuntimeFixture.title);
    scenarioID = scenario.id;
    await expectTerminalConnected(page);
    environmentName = await activeEnvironmentName(page, scenarioID);
    await expectRuntimeEnvironmentPhase(environmentName, "Ready");

    await restartDeployment("breakfix-server");
    await reconnectTerminal(page);
    expect(await activeEnvironmentName(page, scenarioID)).toBe(environmentName);

    await runNodeRuntimeFixtureAnswer(page);
    await expect(page.getByText("All checkpoints complete", { exact: true })).toBeVisible({ timeout: 90_000 });
  } finally {
    if (environmentName) await attachRuntimeEnvironment(testInfo, environmentName);
    if (scenarioID) {
      await stopScenario(page, scenarioID).catch(() => undefined);
      if (environmentName) await waitForRuntimeEnvironmentDeletion(environmentName);
    }
  }
});
