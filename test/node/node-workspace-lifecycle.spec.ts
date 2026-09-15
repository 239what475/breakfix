import { expect, test } from "@playwright/test";
import {
  activeEnvironmentName,
  expectTerminalConnected,
  learningHistory,
  reconnectTerminal,
  registerAndLogin,
  startScenarioFromCatalog,
  stopScenario,
} from "../support/live-helpers";
import { nodeRuntimeFixture } from "../support/catalog-fixture";
import {
  attachRuntimeEnvironment,
  expectRuntimeEnvironmentPhase,
  expectRuntimeEnvironmentReset,
  runtimeEnvironmentResetNonce,
  runtimeEnvironmentUID,
  waitForRuntimeEnvironmentDeletion,
} from "../support/e2e-platform";

test("learner can reset and stop a Node workspace with one runtime environment identity", async ({ page }, testInfo) => {
  test.setTimeout(8 * 60_000);
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

    await page.getByRole("button", { name: "Reset", exact: true }).click();
    await expect(page.getByText("Scenario reset.", { exact: true })).toBeVisible({ timeout: 5 * 60_000 });
    await expectRuntimeEnvironmentReset(environmentName, initialResetNonce + 1);
    expect(await runtimeEnvironmentUID(environmentName)).toBe(initialUID);
    await reconnectTerminal(page);

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
    }, { timeout: 30_000, intervals: [500, 1_000, 2_000] }).toBe("stopped");
    await expect(page.getByText(/^Attempt ended/)).toHaveCount(1);
  } finally {
    if (environmentName) await attachRuntimeEnvironment(testInfo, environmentName);
    if (scenarioID && !stopped) {
      await stopScenario(page, scenarioID).catch(() => undefined);
      if (environmentName) await waitForRuntimeEnvironmentDeletion(environmentName);
    }
  }
});
