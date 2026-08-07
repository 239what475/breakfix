import { expect, test } from "@playwright/test";
import {
	activeEnvironmentName,
	challengeCardByID,
	expectTerminalConnected,
	registerAndLogin,
	runTerminalCommand,
	stopChallenge,
	waitForVerifiedRevision,
} from "../support/live-helpers";
import { attachEnvironmentIdentity, waitForEnvironmentDeletion } from "../support/e2e-platform";
import {
	waitForCatalogChallenge,
	waitForClassificationReview,
	waitForPublishedChallenge,
	waitForVerifiedCandidate,
} from "./authoring-live-helpers";

const agentLiveTest = process.env.RUN_AGENT_LIVE_E2E === "1" ? test : test.skip;

agentLiveTest("generator verifies, publishes, and runs a k8s challenge", async ({ page }, testInfo) => {
	test.setTimeout(50 * 60_000);
	let challengeID = "";
	let environmentName = "";
	let completed = false;
	try {
		await page.setViewportSize({ width: 1440, height: 900 });
		await registerAndLogin(page);
		await page.getByRole("button", { name: "Challenge studio", exact: true }).click();

		const composer = page.locator(".authoring-composer textarea");
		await expect(composer).toBeEnabled({ timeout: 30_000 });
		await composer.fill(
			"创建一道 runtime: k8s 的练习题。学习者在 default namespace 创建 Deployment web：replicas 为 2，标签 app=web，" +
				"容器 nginx 使用 nginx:1.27.5 并暴露 80。再创建 ClusterIP Service web，selector app=web，port 和 targetPort 都为 80。" +
				"初始时资源不存在，只有两个检查点：Deployment 的期望与 Ready 副本数；Service 的类型、selector 和端口。",
		);
		await page.getByRole("button", { name: "发送消息", exact: true }).click();
		await expect(composer).toBeEnabled({ timeout: 8 * 60_000 });
		await composer.fill("题意已经完整，请不要添加额外资源或检查点。");
		await page.getByRole("button", { name: "发送消息", exact: true }).click();
		await expect(page.getByRole("button", { name: "生成并验证题目", exact: true })).toBeEnabled({ timeout: 8 * 60_000 });

		const generation = page.waitForResponse(
			(response) => response.request().method() === "POST" && response.url().includes("/generate"),
		);
		await page.getByRole("button", { name: "生成并验证题目", exact: true }).click();
		const session = (await (await generation).json()) as { id: string };
		await waitForVerifiedCandidate(page, session.id);
		await waitForVerifiedRevision(page);
		await page.getByRole("button", { name: "确认题目内容", exact: true }).click();
		await waitForClassificationReview(page, session.id);

		await page.getByRole("button", { name: "确认分类并发布", exact: true }).click();
		challengeID = await waitForPublishedChallenge(page, session.id);
		await waitForCatalogChallenge(page, challengeID);

		await page.getByRole("button", { name: "Catalog", exact: true }).click();
		const card = challengeCardByID(page, challengeID);
		await expect(card).toBeVisible({ timeout: 30_000 });
		await card.getByRole("button", { name: "Start challenge", exact: true }).click();
		await expectTerminalConnected(page);
		environmentName = await activeEnvironmentName(page, challengeID);
		await runTerminalCommand(page, "/bin/bash /opt/breakfix/challenge/k8s/answer.sh");
		await expect(page.getByText("All checkpoints complete", { exact: true })).toBeVisible({ timeout: 2 * 60_000 });
		completed = true;
	} finally {
		if (environmentName) await attachEnvironmentIdentity(testInfo, "vk8senvironment", environmentName);
		if (completed && environmentName && challengeID) {
			await stopChallenge(page, challengeID);
			await waitForEnvironmentDeletion("vk8senvironment", environmentName);
		}
	}
});
