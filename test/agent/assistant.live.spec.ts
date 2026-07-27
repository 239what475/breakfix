import { expect, test } from "@playwright/test";
import {
	expectTerminalConnected,
	registerAndLogin,
	runTerminalCommand,
	startChallengeFromCatalog,
	stopChallenge,
} from "../support/live-helpers";

const agentLiveTest = process.env.RUN_AGENT_LIVE_E2E === "1" ? test : test.skip;

agentLiveTest("assistant uses real terminal context and renders Markdown", async ({ page }) => {
	test.setTimeout(10 * 60_000);
	await page.setViewportSize({ width: 1440, height: 900 });
	await registerAndLogin(page);
	await startChallengeFromCatalog(page, "批量压缩旧日志");
	await expectTerminalConnected(page);

	const marker = `BREAKFIX_ASSISTANT_SCROLLBACK_${Date.now()}`;
	await runTerminalCommand(page, `printf '%s\\n' '${marker}'`);
	await expect(page.locator(".xterm-rows")).toContainText(marker);

	await page.getByRole("button", { name: "Assistant", exact: true }).click();
	const composer = page.getByRole("textbox", { name: "Assistant message" });
	await composer.fill(
		`回答前必须调用 get_terminal_scrollback（shell-1）、get_checkpoint_status 和 get_solution。` +
			`只根据工具结果说明你看到的 ${marker}。最终回复使用 Markdown，包含二级标题、无序列表、表格和代码块。`,
	);
	await page.getByRole("button", { name: "Send message" }).click();
	await expect(composer).toBeDisabled();

	const reply = page.locator(".assistant-message.assistant").last();
	for (const evidence of ["终端 shell-1 的近期输出", "检查点状态", "参考解答"]) {
		await expect(reply.locator(".assistant-evidence span").filter({ hasText: evidence })).toBeVisible({ timeout: 4 * 60_000 });
	}
	await expect(reply).toContainText(marker);
	const markdown = reply.locator(".assistant-markdown");
	await expect(markdown.locator("h2").first()).toBeVisible();
	await expect(markdown.locator("ul").first()).toBeVisible();
	await expect(markdown.locator("table").first()).toBeVisible();
	await expect(markdown.locator("pre code").first()).toBeVisible();

	await stopChallenge(page, "cleanup-logs");
});
