import { expect, test } from "@playwright/test";
import {
  activeEnvironmentName,
  expectTerminalConnected,
  registerAndLogin,
  runNodeRuntimeFixtureAnswer,
  startChallengeFromCatalog,
  stopChallenge,
} from "../support/live-helpers";
import { nodeRuntimeFixture } from "../support/catalog-fixture";
import {
  attachNodeEnvironmentIdentity,
  expectNodeEnvironmentPhase,
  waitForNodeEnvironmentDeletion,
} from "../support/e2e-platform";

test("learner completes the prepared Node challenge and sees the learning record", async ({ page }, testInfo) => {
  test.setTimeout(6 * 60_000);
  let challengeID = "";
  let environmentName = "";
  let completed = false;

  try {
    await page.setViewportSize({ width: 1440, height: 900 });
    await registerAndLogin(page);
    const challenge = await startChallengeFromCatalog(page, nodeRuntimeFixture.title);
    challengeID = challenge.id;

    await expectTerminalConnected(page);
    environmentName = await activeEnvironmentName(page, challengeID);
    await expectNodeEnvironmentPhase(environmentName, "Ready");

    await runNodeRuntimeFixtureAnswer(page);
    await expect(page.getByText("All checkpoints complete", { exact: true })).toBeVisible({ timeout: 90_000 });
    await expectNodeEnvironmentPhase(environmentName, "Completed");

    await page.getByRole("button", { name: "My space", exact: true }).click();
    await page.getByRole("button", { name: "Learning", exact: true }).first().click();
    await expect(page.getByRole("heading", { name: challenge.title, exact: true })).toBeVisible({ timeout: 90_000 });
    await expect(page.getByLabel("Learning summary").getByText("Completed", { exact: true })).toBeVisible({ timeout: 90_000 });
    completed = true;
  } finally {
    if (environmentName) await attachNodeEnvironmentIdentity(testInfo, environmentName);
    if (completed && challengeID) {
      await stopChallenge(page, challengeID);
      if (environmentName) await waitForNodeEnvironmentDeletion(environmentName);
    }
  }
});
