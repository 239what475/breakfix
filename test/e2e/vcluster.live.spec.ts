import { expect, test } from "@playwright/test";
import {
	expectTerminalConnected,
	registerAndLogin,
	startChallengeFromCatalog,
	stopChallenge,
} from "./live-helpers";

const liveTest = process.env.RUN_LIVE_E2E === "1" ? test : test.skip;

liveTest("vcluster workspace reports checkpoint progress", async ({ page }) => {
	test.setTimeout(8 * 60_000);
	await page.setViewportSize({ width: 1440, height: 900 });
	await registerAndLogin(page);

	await startChallengeFromCatalog(page, "修复错误的 Deployment 镜像");
	await expectTerminalConnected(page);
	await expect(
		page.getByText("Deployment 和 Service 均存在", { exact: true }),
	).toBeVisible({ timeout: 60_000 });

	await stopChallenge(page, "fix-broken-deployment-image");
});
