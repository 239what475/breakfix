import { expect, test } from "@playwright/test";
import {
	activeEnvironmentName,
	scenarioCardByID,
	expectTerminalConnected,
	registerAndLogin,
	runTerminalCommand,
	stopScenario,
} from "../support/live-helpers";
import { attachEnvironmentIdentity, waitForEnvironmentDeletion } from "../support/e2e-platform";
import {
	CandidateRejectedError,
	currentAuthoringSessionID,
	expectNoLifecycleButtons,
	sendAuthoringMessage,
	waitForActiveWorkflow,
	waitForCatalogScenario,
	waitForPublishedScenario,
	waitForVerifiedCandidate,
} from "./authoring-live-helpers";

const agentLiveTest = process.env.RUN_AGENT_LIVE_E2E === "1" ? test : test.skip;

agentLiveTest("conversation confirms, verifies, publishes, and runs a k8s scenario", async ({ page }, testInfo) => {
	test.setTimeout(50 * 60_000);
	let scenarioID = "";
	let environmentName = "";
	let completed = false;
	try {
		await page.setViewportSize({ width: 1440, height: 900 });
		await registerAndLogin(page);
		await page.getByRole("button", { name: "Scenario studio", exact: true }).click();

		const composer = page.locator(".authoring-composer textarea");
		await expect(composer).toBeEnabled({ timeout: 30_000 });
		await expectNoLifecycleButtons(page);
		await sendAuthoringMessage(
			page,
			"创建一道属于 Kubernetes 工作负载与服务运维方向的 runtime: k8s 练习题。学习者在 default namespace 创建 Deployment web：replicas 为 2，标签 app=web，" +
				"容器 nginx 使用 nginx:1.27.5 并暴露 80。再创建 ClusterIP Service web，selector app=web，port 和 targetPort 都为 80。" +
				"初始时资源不存在，只有两个检查点：Deployment 的期望与 Ready 副本数；Service 的类型、selector 和端口。",
		);
		await sendAuthoringMessage(
			page,
			"题意已经完整，我确认当前已持久化的 Plan revision。请创建生成任务并生成题目，" +
				"不要添加额外资源或检查点，生成完成后直接提交 candidate。",
		);
		const sessionID = await currentAuthoringSessionID(page);
		const workflowID = await waitForActiveWorkflow(page, sessionID);
		try {
			await waitForVerifiedCandidate(page, workflowID);
		} catch (cause) {
			if (!(cause instanceof CandidateRejectedError)) throw cause;
			await sendAuthoringMessage(
				page,
				"请读取生成任务的反馈，修复 candidate 后重新提交；不要改动题意约定。",
				sessionID,
			);
			await waitForVerifiedCandidate(page, workflowID);
		}
			await sendAuthoringMessage(page, "我确认题目内容，请发布题目。", sessionID);
		scenarioID = await waitForPublishedScenario(page, sessionID);
		await waitForCatalogScenario(page, scenarioID);

		await page.getByRole("button", { name: "Catalog", exact: true }).click();
		const card = scenarioCardByID(page, scenarioID);
		await expect(card).toBeVisible({ timeout: 30_000 });
		await card.getByRole("button", { name: "Start scenario", exact: true }).click();
		await expectTerminalConnected(page);
		environmentName = await activeEnvironmentName(page, scenarioID);
		await runTerminalCommand(page, "/bin/bash /opt/breakfix/scenario/k8s/answer.sh");
		await expect(page.getByText("All checkpoints complete", { exact: true })).toBeVisible({ timeout: 2 * 60_000 });
		completed = true;
	} finally {
		if (environmentName) await attachEnvironmentIdentity(testInfo, "vk8senvironment", environmentName);
		if (completed && environmentName && scenarioID) {
			await stopScenario(page, scenarioID);
			await waitForEnvironmentDeletion("vk8senvironment", environmentName);
		}
	}
});
