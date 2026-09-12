import { expect, test, type Page } from "@playwright/test";
import {
	currentAuthoringSessionID,
	continueGeneratingWorkflow,
	readAuthoringSession,
	readGeneration,
	sendAuthoringMessage,
	waitForAuthoringInterruption,
	waitForCatalogChallenge,
	waitForPublishedChallenge,
	waitForVerifiedCandidate,
} from "./authoring-live-helpers";
import { registerAndLogin } from "../support/live-helpers";
import { setServerAuthoringDeadline } from "../support/e2e-platform";

const agentLiveTest = process.env.RUN_AGENT_LIVE_E2E === "1" ? test : test.skip;
const challengeTitle = `Restore the Node task marker ${Date.now().toString(36)}`;

type Plan = {
	metadata: {
		title: string;
		description: string;
		runtime: "node" | "k8s";
	};
	overview: string;
	checkpoints: Array<{
		id: string;
		title: string;
		markdown: string;
		position: number;
	}>;
};

const plan: Plan = {
	metadata: {
		title: challengeTitle,
		description: "Repair the missing Node task marker.",
		runtime: "node",
	},
	overview:
		"# 场景\n\nNode 题目节点丢失了 `/var/lib/breakfix/mcp-e2e-task/ready` 任务标记。学习者需要恢复该标记，使文件内容精确等于 `ready`。\n\n# 节点\n\n- host",
	checkpoints: [
		{
			id: "task-marker-ready",
			title: "Task marker ready",
			markdown: "Node 题目节点的任务标记存在且内容精确等于 `ready`。",
			position: 1,
		},
	],
};

const candidateFiles: Record<string, string> = {
		"challenge.yaml":
			"runtime: node\n" +
			"type: operations-scenario\n" +
			`title: ${challengeTitle}\n` +
		"description: Repair the missing Node task marker.\n" +
		"nodes:\n" +
		"  - name: host\n" +
		"    title: Runtime host\n" +
		"checkpoints:\n" +
		"  - id: task-marker-ready\n" +
		"    title: Task marker ready\n" +
		"    description: The Node task marker exists at /var/lib/breakfix/mcp-e2e-task/ready with the exact content ready.\n" +
		"    hint: hints/task-marker-ready.md\n" +
		"    node: host\n",
	"problem.md":
		`# ${challengeTitle}\n\n` +
		"The Node task host has lost its task marker at `/var/lib/breakfix/mcp-e2e-task/ready`. Restore the marker so its content is exactly `ready`.\n",
	"hints/task-marker-ready.md": "Check the expected marker path and its exact content.\n",
	"solution.md":
		"# Solution\n\n" +
		"The task expects `/var/lib/breakfix/mcp-e2e-task/ready` to contain exactly `ready`, without a trailing newline.\n\n" +
		"<!-- checkpoint: task-marker-ready -->\n\n" +
		"Create the marker directory and write `ready` to the marker file.\n",
	"nodes/host/generate.sh":
		"#!/bin/bash\nset -euo pipefail\nrm -rf /var/lib/breakfix/mcp-e2e-task\n",
	"nodes/host/checks.sh":
		"#!/bin/bash\nset -u\n" +
		"marker=/var/lib/breakfix/mcp-e2e-task/ready\n" +
		"if cmp -s <(printf 'ready') \"$marker\" 2>/dev/null; then\n" +
		"  passed=true\n  details='task marker is ready'\n" +
		"else\n  passed=false\n  details='task marker is missing or invalid'\nfi\n" +
		"printf '{\"checks\":[{\"id\":\"task-marker-ready\",\"passed\":%s,\"summary\":\"Task marker ready\",\"details\":\"%s\"}]}' \"$passed\" \"$details\"\n",
	"nodes/host/answer.sh":
		"#!/bin/bash\nset -euo pipefail\nmkdir -p /var/lib/breakfix/mcp-e2e-task\nprintf 'ready' >/var/lib/breakfix/mcp-e2e-task/ready\n",
};

async function authorized<T>(page: Page, path: string, method: string, body?: unknown): Promise<T> {
	return page.evaluate(async ({ path, method, body }) => {
		const response = await fetch(`/api${path}`, {
			method,
			headers: {
				"Content-Type": "application/json",
				Authorization: `Bearer ${localStorage.getItem("token") ?? ""}`,
			},
			body: body === undefined ? undefined : JSON.stringify(body),
		});
		const text = await response.text();
		if (!response.ok) throw new Error(`${method} ${path}: ${text}`);
		return (text ? JSON.parse(text) : undefined) as T;
	}, { path, method, body });
}

async function seedWorkspace(page: Page, workflowID: string): Promise<void> {
	const turnID = `interruption-seed-${Date.now()}`;
	await authorized(page, `/generator/workflows/${workflowID}/workspace/turn`, "POST", { turn_id: turnID });
	for (const [path, content] of Object.entries(candidateFiles)) {
		await authorized(page, `/generator/workflows/${workflowID}/workspace/files`, "PUT", {
			turn_id: turnID,
			path,
			content,
		});
	}
	await authorized(page, `/generator/workflows/${workflowID}/workspace/turn/end`, "POST", { turn_id: turnID });
	// Let the post-turn snapshot request finish before the first Authoring turn
	// tries to acquire the same workspace writer lease.
	await new Promise((resolve) => setTimeout(resolve, 5_000));
}

async function submitSeededCandidate(page: Page, workflowID: string): Promise<void> {
	let turnID = "";
	let lastError: unknown;
	for (let attempt = 0; attempt < 30; attempt += 1) {
		turnID = `interruption-submit-${Date.now()}-${attempt}`;
		try {
			await authorized(page, `/generator/workflows/${workflowID}/workspace/turn`, "POST", { turn_id: turnID });
			lastError = undefined;
			break;
		} catch (cause) {
			lastError = cause;
			await new Promise((resolve) => setTimeout(resolve, 2_000));
		}
	}
	if (lastError) throw lastError;
	await authorized(page, `/generator/workflows/${workflowID}/candidate`, "POST", {
		turn_id: turnID,
		idempotency_key: `interruption-submit-${Date.now()}`,
	});
}

async function assertSnapshotRestoredWorkspace(page: Page, workflowID: string): Promise<void> {
	const turnID = `interruption-restore-${Date.now()}`;
	await authorized(page, `/generator/workflows/${workflowID}/workspace/turn`, "POST", { turn_id: turnID });
	try {
		const restored = await authorized<{ content?: unknown }>(
			page,
			`/generator/workflows/${workflowID}/workspace/file?turn_id=${encodeURIComponent(turnID)}&path=challenge.yaml`,
			"GET",
		);
		expect(restored.content).toContain(challengeTitle);
	} finally {
		await authorized(page, `/generator/workflows/${workflowID}/workspace/turn/end`, "POST", { turn_id: turnID });
	}
}

agentLiveTest("authoring resumes after its deadline and publishes the same workflow", async ({ page }) => {
	test.setTimeout(45 * 60_000);
	await page.setViewportSize({ width: 1440, height: 900 });
	await registerAndLogin(page);
	await page.getByRole("button", { name: "Challenge studio", exact: true }).click();

	const sessionID = await currentAuthoringSessionID(page);
	const savedPlan = await authorized<{ session_id: string; plan_revision: number }>(page, "/generator/plans", "POST", {
		session_id: sessionID,
		expected_revision: 0,
		idempotency_key: `interruption-plan-${Date.now()}`,
		plan,
	});
	const workflow = await authorized<{ id: string }>(page, "/generator/workflows", "POST", {
		session_id: savedPlan.session_id,
		plan_revision: savedPlan.plan_revision,
		idempotency_key: `interruption-confirm-${Date.now()}`,
	});
	const workflowID = workflow.id;
	await seedWorkspace(page, workflowID);

	await sendAuthoringMessage(
		page,
		`继续生成任务 ${workflowID}。这是跨回合恢复验收的第一轮：先读取当前工作区，然后必须调用 run_workspace_command 执行 sleep 120，保持本轮不要提交 candidate，等待时间预算结束。不要重新 confirm_generation，也不要修改题意。`,
		sessionID,
	);
	const event = await waitForAuthoringInterruption(page, sessionID, 5 * 60_000);
	expect(event.kind).toBe("authoring_run_interrupted");
	expect(event.reason).toBe("deadline_exceeded");
	expect(event.resumable).toBe(true);
	expect(["workspace", "snapshot"]).toContain(event.recovery);

	// The runner uses a short deadline only to force the first interruption.
	// Restore the normal budget before the next durable turn and restart the
	// Server so the next turn also exercises snapshot-backed workspace rebuild.
	await setServerAuthoringDeadline("30m");
	await expect.poll(async () => {
		try {
			await readGeneration(page, workflowID);
			return true;
		} catch {
			// The runner's port-forward reconnects while the restarted Server is
			// becoming ready; do not turn that short transport gap into a test
			// failure.
			return false;
		}
	}, { timeout: 90_000, intervals: [500, 1_000, 2_000, 5_000] }).toBe(true);
	await assertSnapshotRestoredWorkspace(page, workflowID);
	await continueGeneratingWorkflow(page, sessionID, workflowID);
	const resumedSession = await readAuthoringSession(page, sessionID);
	expect(resumedSession.authoring_turn_active).toBe(false);
	const resumedGeneration = await readGeneration(page, workflowID);
	let candidateID = resumedGeneration.candidate?.id ?? "";
	if (!candidateID && resumedGeneration.workflow.state === "Generating") {
		await submitSeededCandidate(page, workflowID);
	}
	if (!candidateID) candidateID = await waitForVerifiedCandidate(page, workflowID, 30 * 60_000);
	if (!candidateID) throw new Error("continued authoring turn did not produce a candidate");

	await authorized(page, `/generator/workflows/${workflowID}/content/confirm`, "POST", {
		candidate_revision_id: candidateID,
		idempotency_key: `interruption-content-${Date.now()}`,
	});
	const challengeID = await waitForPublishedChallenge(page, sessionID);
	await waitForCatalogChallenge(page, challengeID);
});
