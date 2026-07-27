import { expect, test } from "@playwright/test";
import {
	challengeCardByID,
	expectTerminalConnected,
	registerAndLogin,
	runAnswer,
	stopChallenge,
	waitForVerifiedRevision,
} from "../support/live-helpers";
import { cleanupPublishedChallenge, cleanupVerifyTask, waitForEnvironmentDeletion } from "../support/runtime-cleanup";
import { waitForInitialVerifyTask, waitForVerifyTaskSucceeded } from "./authoring-live-helpers";

const agentLiveTest = process.env.RUN_AGENT_LIVE_E2E === "1" ? test : test.skip;

agentLiveTest("generator creates and verifies a container challenge on its first submission", async ({ page }) => {
	test.setTimeout(35 * 60_000);
	let taskID = "";
	let challengeID = "";
	let environmentName = "";
	try {
		await page.setViewportSize({ width: 1440, height: 900 });
		await registerAndLogin(page);
		await page.getByRole("button", { name: "Challenge studio", exact: true }).click();

		const composer = page.locator(".authoring-composer textarea");
		await composer.fill(
			"创建一道 runtime: container 的日志清理题。/var/log/app 中有一个实际文件 mtime 超过 7 天的 app.log 和一个今天的 current.log。" +
				"学习者需要创建可重复执行的 /usr/local/bin/archive-old-log.sh，将旧 app.log 压缩为 /var/log/app/app.log.gz，不得处理 current.log。" +
				"题目有两个检查点：脚本存在且可执行；旧日志被压缩而新日志保留。",
		);
		await page.getByRole("button", { name: "发送消息", exact: true }).click();
		await expect(composer).toBeEnabled({ timeout: 8 * 60_000 });
		await composer.fill(
			"题意已经完整，请落盘元数据、题目概览和这两个检查点，不要增加其他资源或检查点。" +
				"初始脚本写入 app.log 内容后仍必须让它的实际 mtime 超过 7 天；标准答案必须在结束前执行归档脚本，使 app.log.gz 已经存在。",
		);
		await page.getByRole("button", { name: "发送消息", exact: true }).click();
		await expect(page.getByRole("button", { name: "生成并验证题目", exact: true })).toBeEnabled({ timeout: 8 * 60_000 });

		const generation = page.waitForResponse((response) => response.request().method() === "POST" && response.url().includes("/generate"));
		await page.getByRole("button", { name: "生成并验证题目", exact: true }).click();
		const session = (await (await generation).json()) as { id: string };
		taskID = await waitForInitialVerifyTask(page, session.id);
		await waitForVerifyTaskSucceeded(taskID);
		await waitForVerifiedRevision(page);

		const publish = page.waitForResponse(
			(response) => response.request().method() === "POST" && response.url().includes(`/api/authoring/sessions/${session.id}/publish`),
		);
		await page.getByRole("button", { name: "发布挑战", exact: true }).click();
		const published = (await (await publish).json()) as {
			state?: string;
			publish_challenge_id?: string;
			verified?: { metadata?: { title?: string } };
		};
		expect(published.state).toBe("Published");
		expect(published.publish_challenge_id).toBeTruthy();
		challengeID = published.publish_challenge_id ?? "";
		const title = published.verified?.metadata?.title ?? "";
		expect(title).toBeTruthy();

		await page.getByRole("button", { name: "Catalog", exact: true }).click();
		const card = challengeCardByID(page, challengeID);
		await expect(card).toBeVisible({ timeout: 30_000 });
		await expect(card.getByRole("heading", { name: title, exact: true })).toBeVisible();
		await card.getByRole("button", { name: "Start challenge", exact: true }).click();
		await expectTerminalConnected(page);
		const activeEnvironmentName = () =>
			page.evaluate(async (id) => {
				const response = await fetch("/api/me/space", {
					headers: { Authorization: `Bearer ${localStorage.getItem("token") ?? ""}` },
				});
				if (!response.ok) throw new Error(await response.text());
				const body = (await response.json()) as { active_environments: Array<{ environment_id: string; challenge: { id: string } }> };
				return body.active_environments.find((item) => item.challenge.id === id)?.environment_id ?? "";
			}, challengeID);
		await expect.poll(activeEnvironmentName, { timeout: 30_000 }).not.toBe("");
		environmentName = await activeEnvironmentName();
		await runAnswer(page);
		await expect(page.getByText("All checkpoints complete", { exact: true })).toBeVisible({ timeout: 90_000 });
	} finally {
		if (environmentName && challengeID) {
			await stopChallenge(page, challengeID).catch(() => undefined);
			await waitForEnvironmentDeletion("containerenvironment", environmentName).catch(() => undefined);
		}
		if (taskID && challengeID) {
			await cleanupPublishedChallenge({
				challengeID,
				submissionID: taskID.replace(/^verify-/, ""),
				verifyTaskID: taskID,
			});
		} else if (taskID) {
			await cleanupVerifyTask({
				submissionID: taskID.replace(/^verify-/, ""),
				verifyTaskID: taskID,
			});
		}
	}
});
