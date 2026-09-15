import { expect, test } from "@playwright/test";
import {
	activeEnvironmentName,
	expectTerminalConnected,
	registerAndLogin,
	runTerminalCommand,
	startScenarioFromCatalog,
	stopScenario,
} from "../support/live-helpers";
import { nodeRuntimeFixture } from "../support/catalog-fixture";
import { attachRuntimeEnvironment, waitForRuntimeEnvironmentDeletion } from "../support/e2e-platform";

const soakTest = process.env.RUN_AGENT_SOAK_E2E === "1" ? test : test.skip;

soakTest("assistant completes twenty real runs in one durable conversation", async ({ page }, testInfo) => {
	test.setTimeout(30 * 60_000);
	let environmentName = "";
	let completed = false;
	await page.setViewportSize({ width: 1440, height: 900 });
	await registerAndLogin(page);
	const scenario = await startScenarioFromCatalog(page, nodeRuntimeFixture.title);
	await expectTerminalConnected(page);
	environmentName = await activeEnvironmentName(page, scenario.id);

	const marker = `BREAKFIX_ASSISTANT_SOAK_${Date.now()}`;
	await runTerminalCommand(page, `printf '%s\\n' '${marker}'`);
	await expect(page.locator(".xterm-rows")).toContainText(marker);

	try {
		await page.getByRole("button", { name: "Assistant", exact: true }).click();
		const composer = page.getByRole("textbox", { name: "Assistant message" });
		const replies = page.locator(".assistant-message.assistant");
		for (let turn = 1; turn <= 20; turn++) {
			const message = turn === 1
				? `第 ${turn} 次验证：回答前必须调用 get_terminal_scrollback（shell-1）、get_checkpoint_status 和 get_solution。` +
					`只根据工具结果说明你看到的 ${marker}。`
				: `第 ${turn} 次连续运行检查：基于已经保存的会话，用一句中文给出下一步学习建议；不要执行任何环境操作。`;
			await composer.fill(message);
			await page.getByRole("button", { name: "Send message" }).click();
			await expect(composer).toBeDisabled();
			await expect(composer).toBeEnabled({ timeout: 4 * 60_000 });
			await expect(replies).toHaveCount(turn, { timeout: 4 * 60_000 });

			const reply = replies.last();
			await expect(reply.locator(".assistant-markdown")).not.toBeEmpty({ timeout: 30_000 });
			if (turn === 1) {
				await expect(reply).toContainText(marker);
				await expect(reply.locator(".assistant-evidence span").filter({ hasText: "终端 shell-1 的近期输出" })).toBeVisible({ timeout: 30_000 });
				await expect(reply.locator(".assistant-evidence span").filter({ hasText: "检查点状态" })).toBeVisible({ timeout: 30_000 });
				await expect(reply.locator(".assistant-evidence span").filter({ hasText: "参考解答" })).toBeVisible({ timeout: 30_000 });
			}
		}
		completed = true;
	} finally {
		if (environmentName) await attachRuntimeEnvironment(testInfo, environmentName);
		if (completed) {
			await stopScenario(page, scenario.id);
			await waitForRuntimeEnvironmentDeletion(environmentName);
		}
	}
});
