import { expect, test } from "@playwright/test";
import {
	activeEnvironmentName,
	scenarioCardByID,
	expectTerminalConnected,
	registerAndLogin,
	runTerminalCommand,
	stopScenario,
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
	waitForCatalogScenario,
	waitForActiveWorkflow,
	waitForPublishedScenario,
	waitForVerifiedCandidate,
} from "./authoring-live-helpers";

const agentLiveTest = process.env.RUN_AGENT_LIVE_E2E === "1" ? test : test.skip;

agentLiveTest("conversation confirms, verifies, publishes, and runs a node scenario", async ({ page }, testInfo) => {
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
			"创建一道 runtime: node 的单节点 Linux 日志归档题，节点名为 host。" +
				"节点上的应用日志位于 /var/log/breakfix-web-e2e/app.log，当前日志需要归档到 /var/log/breakfix-web-e2e/archive/app.log，" +
				"同时保留原日志文件。学习者需要完成归档。题目有一个检查点：归档副本存在，并且内容与原日志完全一致。",
		);
		await sendAuthoringMessage(
			page,
			"题意已经完整，我确认当前已持久化的 Plan revision。请创建生成任务并生成题目：" +
				"初始脚本必须创建包含几行固定内容的应用日志，并确保 archive/app.log 不存在；标准答案必须创建归档目录，" +
				"将原日志复制到 archive/app.log 并保留原文件。检查脚本只验证归档副本存在且与原日志内容一致。" +
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
			await sendAuthoringMessage(page, "我确认题目内容，请发布题目。", sessionID);
		scenarioID = await waitForPublishedScenario(page, sessionID);
		await waitForCatalogScenario(page, scenarioID);

		await page.getByRole("button", { name: "Catalog", exact: true }).click();
		const card = scenarioCardByID(page, scenarioID);
		await expect(card).toBeVisible({ timeout: 30_000 });
		await card.getByRole("button", { name: "Start scenario", exact: true }).click();
		await expectTerminalConnected(page);
		environmentName = await activeEnvironmentName(page, scenarioID);
		await runTerminalCommand(page, "/bin/bash /opt/breakfix/scenario/nodes/host/answer.sh");
		await expect(page.getByText("All checkpoints complete", { exact: true })).toBeVisible({ timeout: 90_000 });
		completed = true;
	} finally {
		if (environmentName) await attachNodeEnvironmentIdentity(testInfo, environmentName);
		if (completed && environmentName && scenarioID) {
			await stopScenario(page, scenarioID);
			await waitForNodeEnvironmentDeletion(environmentName);
		}
	}
});
