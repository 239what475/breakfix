import { expect, test } from "@playwright/test";
import {
	captureWorkspace,
	challengeCard,
	expectTerminalConnected,
	expectViewportWithoutPageOverflow,
	registerAndLogin,
	runAnswer,
	runTerminalCommand,
	startChallengeFromCatalog,
	stopChallenge,
} from "./live-helpers";

const liveTest = process.env.RUN_LIVE_E2E === "1" ? test : test.skip;

liveTest("catalog preserves completion after the challenge environment is stopped", async ({ page }) => {
	test.setTimeout(4 * 60_000);
	await page.setViewportSize({ width: 1440, height: 900 });
	await registerAndLogin(page);

	await startChallengeFromCatalog(page, "批量压缩旧日志");
	await expectTerminalConnected(page);
	await page.getByRole("button", { name: "Catalog", exact: true }).click();
	const cleanupCard = challengeCard(page, "批量压缩旧日志");
	const activeState = cleanupCard.locator(".challenge-state.in-progress");
	await expect(activeState).toContainText("In progress", { timeout: 30_000 });
	await expect(activeState).toContainText(/\d\/3 checkpoints/);

	await startChallengeFromCatalog(page, "批量压缩旧日志");
	await expectTerminalConnected(page);
	await runAnswer(page);
	await expect(page.getByText("All checkpoints complete", { exact: true })).toBeVisible({ timeout: 60_000 });

	await page.getByRole("button", { name: "Catalog", exact: true }).click();
	await expect(cleanupCard.getByText("Completed", { exact: true })).toBeVisible({ timeout: 30_000 });

	await stopChallenge(page, "cleanup-logs");
	await page.reload();
	await expect(challengeCard(page, "批量压缩旧日志").getByText("Completed", { exact: true })).toBeVisible({ timeout: 30_000 });
});

liveTest("workbench keeps the environment active when opening Challenge studio", async ({ page }) => {
	test.setTimeout(4 * 60_000);
	await page.setViewportSize({ width: 1440, height: 900 });
	await registerAndLogin(page);
	await startChallengeFromCatalog(page, "批量压缩旧日志");
	await expectTerminalConnected(page);

	await page.getByRole("button", { name: "Challenge studio", exact: true }).click();
	await expect(page.getByLabel("Challenge authoring workspace")).toBeVisible();
	await expect(page.getByRole("button", { name: "Challenge studio", exact: true })).toHaveAttribute("aria-current", "page");
	await page.getByRole("button", { name: "Catalog", exact: true }).click();

	const cleanupCard = challengeCard(page, "批量压缩旧日志");
	await expect(cleanupCard.locator(".challenge-state.in-progress")).toContainText("In progress", { timeout: 30_000 });
	await stopChallenge(page, "cleanup-logs");
});

liveTest("cleanup-logs runs through the component workbench", async ({ page }) => {
	test.setTimeout(10 * 60_000);
	await page.setViewportSize({ width: 1440, height: 900 });
	await page.goto("/");
	await startChallengeFromCatalog(page, "批量压缩旧日志");
	await expect(page.getByRole("dialog")).toBeVisible();
	await registerAndLogin(page, false);
	await expectTerminalConnected(page);
	const terminalMarker = `BREAKFIX_ASSISTANT_SCROLLBACK_${Date.now()}`;
	const readonlyMarker = `assistant-readonly-${Date.now()}`;
	await runTerminalCommand(page, `printf '%s\\n' '${terminalMarker}'`);
	await runTerminalCommand(page, `printf '%s' '${readonlyMarker}' > /tmp/breakfix-assistant-readonly-proof`);
	await expect(page.locator(".xterm-rows")).toContainText(terminalMarker);

	await page.getByRole("button", { name: "Assistant", exact: true }).click();
	const assistantComposer = page.getByRole("textbox", { name: "Assistant message" });
	await expect(assistantComposer).toBeVisible();
	await assistantComposer.fill(
		"这是一次完整的环境诊断。回答前必须依次调用 get_terminal_scrollback（当前 shell-1）、" +
			"get_checkpoint_status、list_environment_files（路径 /）、" +
			"read_environment_file（路径 /etc/hostname）和 get_solution。" +
			"我明确要求你读取参考答案。只根据工具实际返回的内容，告诉我你看到的 " +
			"BREAKFIX_ASSISTANT_SCROLLBACK 标记。最终回复必须使用 Markdown，包含二级标题“诊断结果”、" +
			"一个无序列表、一个两列表格和一个 fenced code block。",
	);
	await page.getByRole("button", { name: "Send message" }).click();
	await expect(assistantComposer).toBeDisabled();
	await page.waitForTimeout(150);

	await page.getByRole("button", { name: "Problem", exact: true }).click();
	await page.waitForTimeout(500);
	await page.getByRole("button", { name: "Assistant", exact: true }).click();
	await expect(page.getByRole("textbox", { name: "Assistant message" })).toBeVisible();

	await page.reload();
	await startChallengeFromCatalog(page, "批量压缩旧日志");
	await expectTerminalConnected(page);
	await page.getByRole("button", { name: "Assistant", exact: true }).click();

	await expect(page.getByText("终端 shell-1 的近期输出", { exact: true })).toBeVisible({ timeout: 4 * 60_000 });
	const completedAssistantReply = page.locator(".assistant-message.assistant").last();
	for (const evidence of ["检查点状态", "查看环境目录 /", "读取环境文件 /etc/hostname", "参考解答"]) {
		await expect(completedAssistantReply.locator(".assistant-evidence span").filter({ hasText: evidence })).toBeVisible({ timeout: 30_000 });
	}
	await expect(completedAssistantReply).toContainText("BREAKFIX_ASSISTANT_SCROLLBACK", { timeout: 30_000 });
	const renderedAssistantReply = completedAssistantReply.locator(".assistant-markdown");
	await expect(renderedAssistantReply.locator("ul")).toBeVisible();
	await expect(renderedAssistantReply.locator("table")).toBeVisible();
	await expect(renderedAssistantReply.locator("pre code")).toBeVisible();
	await renderedAssistantReply.scrollIntoViewIfNeeded();
	await captureWorkspace(page, "workspace-assistant-markdown");
	const persistedReply = (await completedAssistantReply.textContent()) ?? "";
	expect(persistedReply).not.toBe("");
	await page.getByRole("button", { name: "Problem", exact: true }).click();
	await page.getByRole("button", { name: "Assistant", exact: true }).click();
	await expect(page.locator(".assistant-message.assistant").last()).toContainText(persistedReply);
	await runTerminalCommand(page, "cat /tmp/breakfix-assistant-readonly-proof");
	await expect(page.locator(".xterm-rows")).toContainText(readonlyMarker);
	await captureWorkspace(page, "workspace-assistant-desktop");

	await page.getByRole("button", { name: "Problem", exact: true }).click();
	const resetRequest = page.waitForResponse((response) => {
		const url = new URL(response.url());
		return response.request().method() === "POST" && url.pathname === "/api/challenges/cleanup-logs/reset";
	});
	await page.getByRole("button", { name: "Reset", exact: true }).click();
	expect((await resetRequest).ok()).toBe(true);
	await page.reload();
	await startChallengeFromCatalog(page, "批量压缩旧日志");
	await expectTerminalConnected(page);
	await page.getByRole("button", { name: "Assistant", exact: true }).click();
	await expect(page.locator(".assistant-message")).toHaveCount(0);
	await page.getByRole("button", { name: "Problem", exact: true }).click();

	await expect(page.locator(".markdown-document")).toContainText("批量压缩旧日志");
	await page.getByRole("button", { name: "Solution", exact: true }).click();
	await expect(page.locator(".document-pane > .markdown-document")).toContainText("解答：批量压缩旧日志");
	await page.getByRole("button", { name: "Problem", exact: true }).click();
	await page.getByRole("button", { name: /创建清理脚本/ }).click();
	await expect(page.locator(".hint-panel")).toContainText("脚本路径");
	await page.getByTitle("Collapse sidebar").click();
	await page.getByRole("button", { name: "Show Solution" }).click();
	await expect(page.locator(".document-pane > .markdown-document")).toContainText("解答：批量压缩旧日志");
	await page.getByRole("button", { name: "Show Problem" }).click();
	await page.getByRole("button", { name: "Expand checkpoints" }).click();
	await captureWorkspace(page, "workspace-desktop");

	await page.getByRole("button", { name: "New terminal" }).click();
	await expect(page.getByRole("button", { name: "shell-2", exact: true })).toBeVisible();
	await page.getByRole("button", { name: "Close shell-2" }).click();
	await expect(page.getByRole("button", { name: "shell-2", exact: true })).toHaveCount(0);

	await page.setViewportSize({ width: 800, height: 900 });
	await expect(page.locator(".document-pane")).toBeVisible();
	await expect(page.locator(".terminal-pane")).toBeHidden();
	await expectViewportWithoutPageOverflow(page);
	await captureWorkspace(page, "workspace-narrow-documents");
	await page.getByRole("button", { name: "Terminal", exact: true }).click();
	await expect(page.locator(".terminal-pane")).toBeVisible();
	await expect(page.locator(".document-pane")).toBeHidden();
	await expectViewportWithoutPageOverflow(page);
	await captureWorkspace(page, "workspace-narrow-terminal");
	await page.getByRole("button", { name: "Docs", exact: true }).click();
	await page.getByRole("button", { name: "Show Assistant" }).click();
	await expect(page.locator(".assistant-chat")).toBeVisible();
	await expect(page.locator(".assistant-layer")).toHaveCount(0);
	await expectViewportWithoutPageOverflow(page);
	await captureWorkspace(page, "workspace-assistant-narrow");
	await page.getByRole("button", { name: "Show Problem" }).click();
	await page.setViewportSize({ width: 1440, height: 900 });

	await runAnswer(page);
	await expect(page.getByText("All checkpoints complete", { exact: true })).toBeVisible({ timeout: 45_000 });
	await page.getByRole("button", { name: "Catalog", exact: true }).click();
	await expect(challengeCard(page, "批量压缩旧日志").getByText("Completed", { exact: true })).toBeVisible({ timeout: 30_000 });

	await stopChallenge(page, "cleanup-logs");
	await page.reload();
	await expect(challengeCard(page, "批量压缩旧日志").getByText("Completed", { exact: true })).toBeVisible({ timeout: 30_000 });
});
