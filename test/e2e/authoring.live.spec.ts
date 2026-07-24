import { expect, test } from "@playwright/test";
import {
	captureWorkspace,
	expectElementsWithinViewport,
	expectTerminalConnected,
	expectViewportWithoutPageOverflow,
	registerAndLogin,
	runAnswer,
	waitForVerifiedRevision,
} from "./live-helpers";

const generationLiveTest = process.env.RUN_GENERATION_E2E === "1" ? test : test.skip;

generationLiveTest("an author-reviewed challenge is published and completes in the workbench", async ({ page }) => {
	test.setTimeout(90 * 60_000);
	await page.setViewportSize({ width: 1440, height: 900 });
	await registerAndLogin(page);

	await page.getByRole("button", { name: "Challenge studio", exact: true }).click();
	await expect(page.getByRole("region", { name: "Challenge authoring workspace" })).toBeVisible();
	await expect(page.locator(".toast")).toHaveCount(0);
	await expectElementsWithinViewport(page, ".app-topbar:visible, .authoring-plan-heading > *");
	await expectViewportWithoutPageOverflow(page);
	await captureWorkspace(page, "authoring-desktop");

	await page.setViewportSize({ width: 390, height: 844 });
	await expectElementsWithinViewport(page, ".app-topbar:visible, .authoring-narrow-tabs > *");
	await expect(page.locator(".authoring-plan-pane")).toBeVisible();
	await expect(page.locator(".authoring-chat-pane")).toBeHidden();
	await expectViewportWithoutPageOverflow(page);
	await captureWorkspace(page, "authoring-narrow-plan");
	await page.getByRole("button", { name: "对话", exact: true }).click();
	await expect(page.locator(".authoring-chat-pane")).toBeVisible();
	await expect(page.locator(".authoring-plan-pane")).toBeHidden();
	await expectViewportWithoutPageOverflow(page);
	await captureWorkspace(page, "authoring-narrow-chat");
	await page.setViewportSize({ width: 1440, height: 900 });

	const composer = page.locator(".authoring-composer textarea");
	await expect(composer).toBeEnabled();
	await composer.fill(
		"请创建一道 container 题，参考 cleanup-logs：/var/log/app 下有多个超过 7 天的未压缩日志。" +
			"学习者需要编写可重复执行的清理脚本，把符合条件的日志压缩为 .gz，并保留最近 7 天的文件。" +
			"题目应有两个公开检查点，分别验证脚本存在且可执行，以及旧日志已压缩而新日志未被误处理。",
	);
	await expect(page.getByRole("button", { name: "发送消息", exact: true })).toBeEnabled();
	await page.getByRole("button", { name: "发送消息", exact: true }).click();
	await expect(composer).toBeEnabled({ timeout: 8 * 60_000 });
	await composer.fill(
		"脚本路径由你根据容器环境选择合理且稳定的位置。题意已经足够明确，请现在通过题意约定函数落盘完整题意和两个检查点。",
	);
	await page.getByRole("button", { name: "发送消息", exact: true }).click();
	await expect(composer).toBeEnabled({ timeout: 8 * 60_000 });
	await expect(page.getByRole("button", { name: "生成并验证题目", exact: true })).toBeEnabled({ timeout: 8 * 60_000 });
	await expect(page.locator(".authoring-status")).toContainText(/r[2-9]/);
	await expect(page.locator(".authoring-change-impact").first()).toBeVisible();

	await composer.fill("第二个检查点只验证最终文件状态，不要规定用户必须使用 gzip 或固定脚本实现。");
	await page.getByRole("button", { name: "发送消息", exact: true }).click();
	await expect(page.getByRole("button", { name: "生成并验证题目", exact: true })).toBeVisible({ timeout: 5 * 60_000 });

	const generationStart = page.waitForResponse(
		(response) => response.request().method() === "POST" && response.url().includes("/generate"),
	);
	await page.getByRole("button", { name: "生成并验证题目", exact: true }).click();
	const generationSession = (await (await generationStart).json()) as {
		id: string;
		verify_task_id?: string;
		artifact?: unknown;
	};
	expect(generationSession.artifact).toBeFalsy();
	await expect(page.getByText(/正在生成并验证题目 revision/)).toBeVisible();
	await waitForVerifiedRevision(page);
	const verifiedReview = await page.evaluate(async (sessionID) => {
		const response = await fetch(`/api/authoring/sessions/${sessionID}`, {
			headers: { Authorization: `Bearer ${localStorage.getItem("token") ?? ""}` },
		});
		if (!response.ok) throw new Error(await response.text());
		return response.json() as Promise<{
			artifact?: unknown;
			visible_revision?: number;
			verification?: { phase?: string; task_id?: string };
		}>;
	}, generationSession.id);
	expect(verifiedReview.artifact).toBeTruthy();
	expect(verifiedReview.verification?.phase).toBe("Succeeded");
	expect(verifiedReview.visible_revision).toBeGreaterThan(0);
	expect(verifiedReview.verification?.task_id).toBeTruthy();

	await page.getByRole("button", { name: "Assets", exact: true }).click();
	await expect(page.getByLabel("已验证文件")).toContainText("challenge.yaml");
	expect(await page.getByLabel("已验证文件").locator("option").allTextContents()).toEqual(
		expect.arrayContaining([
			"challenge.yaml",
			"Dockerfile",
			"generate.sh",
			"checks/checkpoints.sh",
			"problem.md",
			"solution.md",
			"answer.sh",
		]),
	);
	await page.getByRole("button", { name: "Diff", exact: true }).click();
	await expect(page.locator(".authoring-code-view pre")).not.toBeEmpty();

	await composer.fill("请把题目简介明确为只处理 .log 文件，并重新生成和真实验证后再给我审核。");
	await page.getByRole("button", { name: "发送消息", exact: true }).click();
	await expect(page.getByText("正在生成并验证修订题目", { exact: true })).toBeVisible({ timeout: 5 * 60_000 });
	await waitForVerifiedRevision(page);
	const revisedReview = await page.evaluate(async (sessionID) => {
		const response = await fetch(`/api/authoring/sessions/${sessionID}`, {
			headers: { Authorization: `Bearer ${localStorage.getItem("token") ?? ""}` },
		});
		if (!response.ok) throw new Error(await response.text());
		return response.json() as Promise<{
			artifact?: unknown;
			visible_revision?: number;
			verification?: { phase?: string; task_id?: string };
		}>;
	}, generationSession.id);
	expect(revisedReview.artifact).toBeTruthy();
	expect(revisedReview.verification?.phase).toBe("Succeeded");
	expect(revisedReview.visible_revision).toBeGreaterThan(verifiedReview.visible_revision ?? 0);
	expect(revisedReview.verification?.task_id).not.toBe(verifiedReview.verification?.task_id);
	await page.getByRole("button", { name: "发布挑战", exact: true }).click();
	await page.getByRole("button", { name: "查看已发布题目", exact: true }).click();
	await expect(page.locator("article.challenge-card").first()).toBeVisible({ timeout: 35 * 60_000 });

	await page.locator("article.challenge-card").first().getByRole("button", { name: "Start challenge", exact: true }).click();
	await expectTerminalConnected(page);
	await runAnswer(page);
	await expect(page.getByText("All checkpoints complete", { exact: true })).toBeVisible({ timeout: 45_000 });
});
