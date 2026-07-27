import { expect, test } from "@playwright/test";
import { registerAndLogin, waitForVerifiedRevision } from "../support/live-helpers";
import { cleanupPublishedChallenge, cleanupVerifyTask } from "../support/runtime-cleanup";
import { waitForInitialVerifyTask, waitForVerifyTaskSucceeded } from "./authoring-live-helpers";

const agentLiveTest = process.env.RUN_AGENT_LIVE_E2E === "1" ? test : test.skip;

agentLiveTest("generator creates and verifies a vcluster challenge on its first submission", async ({ page }) => {
	test.setTimeout(35 * 60_000);
	let taskID = "";
	let challengeID = "";
	try {
		await page.setViewportSize({ width: 1440, height: 900 });
		await registerAndLogin(page);
		await page.getByRole("button", { name: "Challenge studio", exact: true }).click();

		const composer = page.locator(".authoring-composer textarea");
		await composer.fill(
			"创建一道 runtime: vcluster 的 Kubernetes 练习题。学习者使用容器中的 kubectl 和 kubeconfig，在 default namespace 创建 Deployment web：" +
				"replicas 为 2，标签 app=web，容器 nginx 使用 nginx:1.27.5 并暴露 80。再创建 ClusterIP Service web，selector app=web，port 和 targetPort 都为 80。" +
				"初始时资源不存在，题目只有两个检查点，分别验证 Deployment 的期望与 Ready 副本数，以及 Service 的类型、selector 和端口。",
		);
		await page.getByRole("button", { name: "发送消息", exact: true }).click();
		await expect(composer).toBeEnabled({ timeout: 8 * 60_000 });
		await composer.fill("题意已经完整，请落盘元数据、题目概览和两个检查点，不要添加额外资源或检查点。");
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
		const published = (await (await publish).json()) as { state?: string; publish_challenge_id?: string };
		expect(published.state).toBe("Published");
		expect(published.publish_challenge_id).toBeTruthy();
		challengeID = published.publish_challenge_id ?? "";
	} finally {
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
