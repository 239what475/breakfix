import { expect, test } from "@playwright/test";
import {
  activeEnvironmentName,
  expectTerminalConnected,
  learningHistory,
  registerAndLogin,
  startScenarioFromCatalog,
  stopScenario,
} from "../support/live-helpers";
import { nodeRuntimeFixture } from "../support/catalog-fixture";
import {
  attachNodeEnvironmentIdentity,
  expectNoRuntimeEnvironments,
  expectNodeEnvironmentPhase,
  expireNodeEnvironmentForIdleReclamation,
  waitForNodeEnvironmentDeletion,
} from "../support/e2e-platform";

test("idle Node workspace is reclaimed and retained as an expired attempt", async ({ page }, testInfo) => {
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
    await expectNodeEnvironmentPhase(environmentName, "Ready");

    // Navigating away unmounts the terminal, so no terminal lease can renew
    // the activity timestamp that this controlled expiry replaces.
    await page.getByRole("button", { name: "My space", exact: true }).click();
    await expect(page.getByRole("heading", { name: "Your learning space", exact: true })).toBeVisible();
    await expireNodeEnvironmentForIdleReclamation(environmentName);
    await waitForNodeEnvironmentDeletion(environmentName);
    reclaimed = true;
    await expectNoRuntimeEnvironments();

    await page.getByRole("button", { name: "Learning", exact: true }).first().click();
    await expect.poll(async () => {
      const items = await learningHistory(page);
      return items.some((item) => item.scenario.id === scenarioID && item.scenario.runtime === "node" && item.state === "expired");
    }, { timeout: 30_000, intervals: [500, 1_000, 2_000] }).toBe(true);
    await expect(page.getByText(/^Attempt ended/)).toBeVisible();
  } finally {
    if (environmentName) await attachNodeEnvironmentIdentity(testInfo, environmentName);
    if (scenarioID && !reclaimed) {
      await stopScenario(page, scenarioID).catch(() => undefined);
      if (environmentName) await waitForNodeEnvironmentDeletion(environmentName);
    }
  }
});
