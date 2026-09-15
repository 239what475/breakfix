import { expect, test } from "@playwright/test";
import {
  activeEnvironmentName,
  expectTerminalConnected,
  learningHistory,
  registerAndLogin,
  startScenarioFromCatalog,
  stopScenario,
} from "../support/live-helpers";
import { k8sReproductionCoreFixture } from "../support/catalog-fixture";
import {
  attachRuntimeEnvironment,
  expectRuntimeEnvironmentPhase,
  waitForRuntimeEnvironmentDeletion,
} from "../support/e2e-platform";

test("Kubernetes reproduction core works without learning aids and is reclaimed after stop", async ({ page }, testInfo) => {
  test.setTimeout(15 * 60_000);
  let scenarioID = "";
  let environmentName = "";
  let stopped = false;

  try {
    await page.setViewportSize({ width: 1440, height: 900 });
    await registerAndLogin(page);
    const scenario = await startScenarioFromCatalog(page, k8sReproductionCoreFixture.title);
    scenarioID = scenario.id;
    await expectTerminalConnected(page);
    environmentName = await activeEnvironmentName(page, scenarioID);
    await expectRuntimeEnvironmentPhase(environmentName, "Ready");

    await expect(page.getByRole("heading", { name: "Scenario overview", exact: true })).toBeVisible();
    await expect(page.getByText("Target phenomenon", { exact: true })).toBeVisible();
    await expect(page.getByText("breakfix-runtime-fixture ConfigMap 不存在，因此目标现象已复现。", { exact: true })).toBeVisible();
    await expect(page.getByText("runtime-config-absent", { exact: true })).toBeVisible();
    await expect(page.getByRole("button", { name: "Problem", exact: true })).toHaveCount(0);
    await expect(page.getByRole("button", { name: "Solution", exact: true })).toHaveCount(0);
    await expect(page.getByText("All checkpoints complete", { exact: true })).toHaveCount(0);

    page.once("dialog", (dialog) => dialog.accept());
    await page.getByRole("button", { name: "Stop", exact: true }).click();
    await expect(page.getByText("Environment stopped.", { exact: true })).toBeVisible({ timeout: 10 * 60_000 });
    await waitForRuntimeEnvironmentDeletion(environmentName);
    stopped = true;

    await page.getByRole("button", { name: "My space", exact: true }).click();
    await page.getByRole("button", { name: "Learning", exact: true }).first().click();
    await expect.poll(async () => {
      const items = await learningHistory(page);
      return items.some((item) => item.scenario.id === scenarioID && item.scenario.runtime === "k8s" && item.state === "stopped");
    }, { timeout: 30_000, intervals: [500, 1_000, 2_000] }).toBe(true);
    await expect(page.getByText(/^Attempt ended/)).toBeVisible();
  } finally {
    if (environmentName) await attachRuntimeEnvironment(testInfo, environmentName);
    if (scenarioID && !stopped) {
      await stopScenario(page, scenarioID).catch(() => undefined);
      if (environmentName) await waitForRuntimeEnvironmentDeletion(environmentName);
    }
  }
});
