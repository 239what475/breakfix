import { expect, test } from "@playwright/test";
import {
	activeEnvironmentName,
	challengeCardByID,
	expectTerminalConnected,
	registerAndLogin,
	runTerminalCommand,
	stopChallenge,
} from "../support/live-helpers";
import {
	attachNodeEnvironmentIdentity,
	waitForNodeEnvironmentDeletion,
} from "../support/e2e-platform";
import {
	CandidateRejectedError,
	currentAuthoringSessionID,
	expectNoLifecycleButtons,
	sendAuthoringMessage,
	waitForCatalogChallenge,
	waitForActiveWorkflow,
	waitForClassificationReview,
	waitForPublishedChallenge,
	waitForVerifiedCandidate,
} from "./authoring-live-helpers";

const agentLiveTest = process.env.RUN_AGENT_LIVE_E2E === "1" ? test : test.skip;

agentLiveTest("conversation confirms, verifies, publishes, and runs a node challenge", async ({ page }, testInfo) => {
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
		await expectNoLifecycleButtons(page);
		await sendAuthoringMessage(
			page,
			"创建一道 runtime: node 的单节点运行时初始化验收题，节点名为 host。" +
				"平台预期 /var/lib/breakfix/web-e2e/ready 在初始化后存在且内容精确等于 ready，但当前初始化丢失了这个标记文件。" +
				"学习者需要恢复该标记。题目有一个检查点：标记文件存在且内容精确为 ready。",
		);
		await sendAuthoringMessage(
			page,
			"题意已经完整，我确认当前已持久化的 Plan revision。请创建生成任务并生成题目：" +
				"初始脚本必须真的删除该标记文件；标准答案必须重新创建目录并写入精确内容 ready。" +
				"不要增加节点、资源或检查点，生成完成后直接提交 candidate。",
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
		await sendAuthoringMessage(page, "我确认题目内容，请进入分类。", sessionID);
		await waitForClassificationReview(page, workflowID);

		await sendAuthoringMessage(page, "我确认当前分类提案，请发布题目。", sessionID);
		challengeID = await waitForPublishedChallenge(page, sessionID);
		await waitForCatalogChallenge(page, challengeID);

		await page.getByRole("button", { name: "Catalog", exact: true }).click();
		const card = challengeCardByID(page, challengeID);
		await expect(card).toBeVisible({ timeout: 30_000 });
		await card.getByRole("button", { name: "Start challenge", exact: true }).click();
		await expectTerminalConnected(page);
		environmentName = await activeEnvironmentName(page, challengeID);
		await runTerminalCommand(page, "/bin/bash /opt/breakfix/challenge/nodes/host/answer.sh");
		await expect(page.getByText("All checkpoints complete", { exact: true })).toBeVisible({ timeout: 90_000 });
		completed = true;
	} finally {
		if (environmentName) await attachNodeEnvironmentIdentity(testInfo, environmentName);
		if (completed && environmentName && challengeID) {
			await stopChallenge(page, challengeID);
			await waitForNodeEnvironmentDeletion(environmentName);
		}
	}
});
