import { expect, test } from "@playwright/test";
import {
	challengeCardByID,
	expectTerminalConnected,
	registerAndLogin,
	runTerminalCommand,
	stopChallenge,
	waitForVerifiedRevision,
} from "../support/live-helpers";
import { waitForEnvironmentDeletion } from "../support/runtime-cleanup";
import { waitForPublishedChallenge, waitForVerifiedCandidate } from "./authoring-live-helpers";

const agentLiveTest = process.env.RUN_AGENT_LIVE_E2E === "1" ? test : test.skip;

agentLiveTest("generator verifies, publishes, and runs a node challenge", async ({ page }) => {
	test.setTimeout(50 * 60_000);
	let challengeID = "";
	let environmentName = "";
	try {
		await page.setViewportSize({ width: 1440, height: 900 });
		await registerAndLogin(page);
		await page.getByRole("button", { name: "Challenge studio", exact: true }).click();

		const composer = page.locator(".authoring-composer textarea");
		await expect(composer).toBeEnabled({ timeout: 30_000 });
		await composer.fill(
			"创建一道 runtime: node 的单节点日志清理题，节点名为 host。/var/log/app 中有一个实际 mtime 超过 7 天的 app.log 和一个今天的 current.log。" +
				"学习者需要创建可重复执行的 /usr/local/bin/archive-old-log.sh，将旧 app.log 压缩为 /var/log/app/app.log.gz，不得处理 current.log。" +
				"题目有两个检查点：脚本存在且可执行；旧日志被压缩而新日志保留。",
		);
		await page.getByRole("button", { name: "发送消息", exact: true }).click();
		await expect(composer).toBeEnabled({ timeout: 8 * 60_000 });
		await composer.fill(
			"题意已经完整。初始脚本写入 app.log 后必须让它的实际 mtime 超过 7 天；标准答案必须执行归档脚本。不要增加节点、资源或检查点。",
		);
		await page.getByRole("button", { name: "发送消息", exact: true }).click();
		await expect(page.getByRole("button", { name: "生成并验证题目", exact: true })).toBeEnabled({ timeout: 8 * 60_000 });

		const generation = page.waitForResponse(
			(response) => response.request().method() === "POST" && response.url().includes("/generate"),
		);
		await page.getByRole("button", { name: "生成并验证题目", exact: true }).click();
		const session = (await (await generation).json()) as { id: string };
		await waitForVerifiedCandidate(page, session.id);
		await waitForVerifiedRevision(page);

		await page.getByRole("button", { name: "发布挑战", exact: true }).click();
		challengeID = await waitForPublishedChallenge(page, session.id);
		const title = (await page.locator(".verified-challenge h2").textContent())?.trim() ?? "";

		await page.getByRole("button", { name: "Catalog", exact: true }).click();
		const card = challengeCardByID(page, challengeID);
		await expect(card).toBeVisible({ timeout: 30_000 });
		if (title) await expect(card.getByRole("heading", { name: title, exact: true })).toBeVisible();
		await card.getByRole("button", { name: "Start challenge", exact: true }).click();
		await expectTerminalConnected(page);
		environmentName = await activeEnvironmentName(page, challengeID);
		await runTerminalCommand(page, "/bin/bash /opt/breakfix/challenge/nodes/host/answer.sh");
		await expect(page.getByText("All checkpoints complete", { exact: true })).toBeVisible({ timeout: 90_000 });
	} finally {
		if (environmentName && challengeID) {
			await stopChallenge(page, challengeID).catch(() => undefined);
			await waitForEnvironmentDeletion("nodeenvironment", environmentName).catch(() => undefined);
		}
	}
});

async function activeEnvironmentName(page: import("@playwright/test").Page, challengeID: string): Promise<string> {
	const read = () =>
		page.evaluate(async (id) => {
			const response = await fetch("/api/me/space", {
				headers: { Authorization: `Bearer ${localStorage.getItem("token") ?? ""}` },
			});
			if (!response.ok) throw new Error(await response.text());
			const body = (await response.json()) as {
				active_environments: Array<{ environment_id: string; challenge: { id: string } }>;
			};
			return body.active_environments.find((item) => item.challenge.id === id)?.environment_id ?? "";
		}, challengeID);
	await expect.poll(read, { timeout: 30_000 }).not.toBe("");
	return read();
}
