import { expect, test } from "@playwright/test";
import {
	challengeCard,
	expectTerminalConnected,
	registerAndLogin,
	runAnswer,
	startChallengeFromCatalog,
	stopChallenge,
} from "../support/live-helpers";

const runtimeTest = process.env.RUN_RUNTIME_E2E === "1" ? test : test.skip;

runtimeTest("container workbench records progress and completion", async ({ page }) => {
	test.setTimeout(4 * 60_000);
	await page.setViewportSize({ width: 1440, height: 900 });
	await registerAndLogin(page);

	await startChallengeFromCatalog(page, "批量压缩旧日志");
	await expectTerminalConnected(page);
	await page.getByRole("button", { name: "Catalog", exact: true }).click();
	const card = challengeCard(page, "批量压缩旧日志");
	await expect(card.locator(".challenge-state.in-progress")).toContainText("In progress", { timeout: 30_000 });

	await startChallengeFromCatalog(page, "批量压缩旧日志");
	await expectTerminalConnected(page);
	await runAnswer(page);
	await expect(page.getByText("All checkpoints complete", { exact: true })).toBeVisible({ timeout: 60_000 });
	await page.getByRole("button", { name: "Catalog", exact: true }).click();
	await expect(card.getByText("Completed", { exact: true })).toBeVisible({ timeout: 30_000 });

	await stopChallenge(page, "chal-r7m4x2q9v6kp");
});

runtimeTest("opening the authoring page does not end a container environment", async ({ page }) => {
	test.setTimeout(4 * 60_000);
	await page.setViewportSize({ width: 1440, height: 900 });
	await registerAndLogin(page);
	await startChallengeFromCatalog(page, "批量压缩旧日志");
	await expectTerminalConnected(page);

	await page.getByRole("button", { name: "Challenge studio", exact: true }).click();
	await expect(page.getByLabel("Challenge authoring workspace")).toBeVisible();
	await page.getByRole("button", { name: "Catalog", exact: true }).click();
	await expect(challengeCard(page, "批量压缩旧日志").locator(".challenge-state.in-progress")).toContainText("In progress", {
		timeout: 30_000,
	});
	await stopChallenge(page, "chal-r7m4x2q9v6kp");
});

runtimeTest("My space projects a real terminal and checkpoint lifecycle", async ({ page }) => {
	test.setTimeout(4 * 60_000);
	await page.setViewportSize({ width: 1440, height: 900 });
	await registerAndLogin(page);
	await startChallengeFromCatalog(page, "批量压缩旧日志");
	await expectTerminalConnected(page);
	await page.getByRole("button", { name: "My space", exact: true }).click();
	await expect(page.locator(".active-environment-row")).toContainText("批量压缩旧日志", { timeout: 30_000 });

	await page.locator(".active-environment-row").getByRole("button", { name: "Start challenge", exact: true }).click();
	await expectTerminalConnected(page);
	await runAnswer(page);
	await expect(page.getByText("All checkpoints complete", { exact: true })).toBeVisible({ timeout: 60_000 });
	await page.getByRole("button", { name: "My space", exact: true }).click();
	await expect(page.locator(".history-row", { hasText: "批量压缩旧日志" })).toContainText("Completed", { timeout: 30_000 });

	await stopChallenge(page, "chal-r7m4x2q9v6kp");
});
