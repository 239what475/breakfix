import { expect, test } from "@playwright/test";
import {
	challengeCard,
	expectTerminalConnected,
	registerAndLogin,
	runNodeRuntimeFixtureAnswer,
	startChallengeFromCatalog,
	stopChallenge,
} from "../support/live-helpers";
import { nodeRuntimeFixture } from "../support/catalog-fixture";

const runtimeTest = process.env.RUN_RUNTIME_E2E === "1" ? test : test.skip;

runtimeTest("node workbench records progress and completion", async ({ page }) => {
	test.setTimeout(4 * 60_000);
	await page.setViewportSize({ width: 1440, height: 900 });
	await registerAndLogin(page);

	const challenge = await startChallengeFromCatalog(page, nodeRuntimeFixture.title);
	await expectTerminalConnected(page);
	await page.getByRole("button", { name: "Catalog", exact: true }).click();
	const card = challengeCard(page, challenge.title);
	await expect(card.locator(".challenge-state.in-progress")).toContainText("In progress", { timeout: 30_000 });

	await startChallengeFromCatalog(page, challenge.title);
	await expectTerminalConnected(page);
	await runNodeRuntimeFixtureAnswer(page);
	await expect(page.getByText("All checkpoints complete", { exact: true })).toBeVisible({ timeout: 60_000 });
	await page.getByRole("button", { name: "Catalog", exact: true }).click();
	await expect(card.getByText("Completed", { exact: true })).toBeVisible({ timeout: 30_000 });

	await stopChallenge(page, challenge.id);
});

runtimeTest("opening the authoring page does not end a node environment", async ({ page }) => {
	test.setTimeout(4 * 60_000);
	await page.setViewportSize({ width: 1440, height: 900 });
	await registerAndLogin(page);
	const challenge = await startChallengeFromCatalog(page, nodeRuntimeFixture.title);
	await expectTerminalConnected(page);

	await page.getByRole("button", { name: "Challenge studio", exact: true }).click();
	await expect(page.getByLabel("Challenge authoring workspace")).toBeVisible();
	await page.getByRole("button", { name: "Catalog", exact: true }).click();
	await expect(challengeCard(page, challenge.title).locator(".challenge-state.in-progress")).toContainText("In progress", {
		timeout: 30_000,
	});
	await stopChallenge(page, challenge.id);
});

runtimeTest("My space projects a real terminal and checkpoint lifecycle", async ({ page }) => {
	test.setTimeout(4 * 60_000);
	await page.setViewportSize({ width: 1440, height: 900 });
	await registerAndLogin(page);
	const challenge = await startChallengeFromCatalog(page, nodeRuntimeFixture.title);
	await expectTerminalConnected(page);
	await page.getByRole("button", { name: "My space", exact: true }).click();
	await expect(page.locator(".active-environment-row")).toContainText(challenge.title, { timeout: 30_000 });

	await page.locator(".active-environment-row").getByRole("button", { name: "Start challenge", exact: true }).click();
	await expectTerminalConnected(page);
	await runNodeRuntimeFixtureAnswer(page);
	await expect(page.getByText("All checkpoints complete", { exact: true })).toBeVisible({ timeout: 60_000 });
	await page.getByRole("button", { name: "My space", exact: true }).click();
	await expect(page.locator(".history-row", { hasText: challenge.title })).toContainText("Completed", { timeout: 30_000 });

	await stopChallenge(page, challenge.id);
});
