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

agentLiveTest("generator verifies, publishes, and runs a k8s challenge", async ({ page }) => {
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

		await page.getByRole("button", { name: "发布挑战", exact: true }).click();
		challengeID = await waitForPublishedChallenge(page, session.id);

		await page.getByRole("button", { name: "Catalog", exact: true }).click();
		const card = challengeCardByID(page, challengeID);
		await expect(card).toBeVisible({ timeout: 30_000 });
		await card.getByRole("button", { name: "Start challenge", exact: true }).click();
		await expectTerminalConnected(page);
		environmentName = await activeEnvironmentName(page, challengeID);
		await runTerminalCommand(page, "/bin/bash /opt/breakfix/challenge/k8s/answer.sh");
		await expect(page.getByText("All checkpoints complete", { exact: true })).toBeVisible({ timeout: 2 * 60_000 });
	} finally {
		if (environmentName && challengeID) {
			await stopChallenge(page, challengeID).catch(() => undefined);
			await waitForEnvironmentDeletion("vk8senvironment", environmentName).catch(() => undefined);
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
