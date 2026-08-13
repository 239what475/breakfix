import { expect, test } from "@playwright/test";
import { existsSync } from "node:fs";
import { mkdtemp, readFile, readdir, rm, stat, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import { MCPConnectorClient, registerAndGetToken } from "../support/mcp-client";

const agentLiveTest = process.env.RUN_AGENT_LIVE_E2E === "1" ? test : test.skip;

type Workflow = {
	id: string;
	state: string;
	last_error?: string | null;
	candidate_revision_id?: string;
};

type GenerationResult = {
	generation: {
		workflow: Workflow;
		candidate?: { id: string; archive_sha256: string } | null;
		verification?: { passed: boolean } | null;
		classification?: {
			revision: number;
			result: "proposed" | "unclassifiable";
		} | null;
	};
	review?: {
		workflow_id: string;
		candidate_revision_id: string;
		kind: "content" | "classification";
		proposal_revision: number;
		review_path: string;
	} | null;
};

const plan = {
	metadata: {
		title: "Restore the Node runtime readiness marker",
		difficulty: "easy",
		description: "Repair the missing Node runtime readiness marker.",
		runtime: "node",
	},
	overview:
		"# 场景\n\nNode 运行时主机丢失了 `/var/lib/breakfix/mcp-e2e-marker/ready` 初始化标记。" +
		"学习者需要恢复该标记，使内容精确等于 `ready`。\n\n# 节点\n\n- host\n\n# 检查点\n\n" +
		"1. **runtime-marker-ready**：Node 运行时 readiness 标记存在且内容为 `ready`。",
	checkpoints: [
		{
			id: "runtime-marker-ready",
			title: "Runtime marker ready",
			markdown: "Node 运行时 readiness 标记 `/var/lib/breakfix/mcp-e2e-marker/ready` 存在且内容精确等于 `ready`。",
			position: 1,
		},
	],
};

function candidateFiles(answerScript: string): Record<string, string> {
	return {
		"challenge.yaml":
			"runtime: node\n" +
			"title: Restore the Node runtime readiness marker\n" +
			"difficulty: easy\n" +
			"description: Repair the missing Node runtime readiness marker.\n" +
			"nodes:\n" +
			"  - name: host\n" +
			"    title: Runtime host\n" +
			"checkpoints:\n" +
			"  - id: runtime-marker-ready\n" +
			"    title: Runtime marker ready\n" +
			"    description: The Node runtime readiness marker exists at /var/lib/breakfix/mcp-e2e-marker/ready with the exact content ready.\n" +
			"    hint: hints/runtime-marker-ready.md\n" +
			"    node: host\n",
		"problem.md":
			"# Restore the Node runtime readiness marker\n\n" +
			"The Node runtime host has lost its readiness marker at `/var/lib/breakfix/mcp-e2e-marker/ready`. " +
			"Restore the marker so its content is exactly `ready`.\n",
		"hints/runtime-marker-ready.md":
			"Check the expected marker path and its exact content.\n",
		"solution.md":
			"# Solution\n\n" +
			"The runtime init unit expects `/var/lib/breakfix/mcp-e2e-marker/ready` to contain exactly `ready`.\n\n" +
			"<!-- checkpoint: runtime-marker-ready -->\n\n" +
			"- Create the marker directory and write `ready` to the marker file.\n",
		"nodes/host/generate.sh":
			"#!/bin/bash\nset -euo pipefail\n" +
			"rm -rf /var/lib/breakfix/mcp-e2e-marker\n",
		"nodes/host/checks.sh":
			"#!/bin/bash\nset -u\n" +
			"marker=/var/lib/breakfix/mcp-e2e-marker/ready\n" +
			"if [[ \"$(cat \"$marker\" 2>/dev/null || true)\" == \"ready\" ]]; then\n" +
			"  passed=true\n  details='runtime readiness marker is ready'\n" +
			"else\n" +
			"  passed=false\n  details='runtime readiness marker is missing or invalid'\n" +
			"fi\n" +
			"printf '{\"checks\":[{\"id\":\"runtime-marker-ready\",\"passed\":%s,\"summary\":\"Runtime marker ready\",\"details\":\"%s\"}]}' \"$passed\" \"$details\"\n",
		"nodes/host/answer.sh": answerScript,
	};
}

const brokenAnswer =
	"#!/bin/bash\nset -euo pipefail\n" +
	"# Deliberately broken first submission: it creates the marker with content\n" +
	"# that does not satisfy the checkpoint.\n" +
	"mkdir -p /var/lib/breakfix/mcp-e2e-marker\n" +
	"printf 'not-ready\\n' >/var/lib/breakfix/mcp-e2e-marker/ready\n";

const fixedAnswer =
	"#!/bin/bash\nset -euo pipefail\n" +
	"mkdir -p /var/lib/breakfix/mcp-e2e-marker\n" +
	"printf 'ready\\n' >/var/lib/breakfix/mcp-e2e-marker/ready\n";

const sleep = (milliseconds: number) =>
	new Promise<void>((resolve) => {
		setTimeout(resolve, milliseconds);
	});

function connectorBinaryPath(): string {
	const candidates = [
		path.resolve(process.cwd(), "..", "bin", "breakfix-mcp"),
		path.resolve(process.cwd(), "bin", "breakfix-mcp"),
	];
	const found = candidates.find((candidate) => existsSync(candidate));
	if (!found) {
		throw new Error(
			`breakfix-mcp binary was not found; run make build first (tried ${candidates.join(", ")})`,
		);
	}
	return found;
}

async function generation(
	client: MCPConnectorClient,
	workflowID: string,
): Promise<GenerationResult> {
	const result = await client.callTool("get_generation", { workflow_id: workflowID });
	return result as GenerationResult;
}

async function waitForGeneration(
	client: MCPConnectorClient,
	workflowID: string,
	done: (result: GenerationResult) => boolean,
	timeoutMilliseconds: number,
	what: string,
): Promise<GenerationResult> {
	const deadline = Date.now() + timeoutMilliseconds;
	while (Date.now() < deadline) {
		const result = await generation(client, workflowID);
		if (result.generation.workflow.state === "Failed" || result.generation.workflow.state === "Cancelled") {
			throw new Error(
				`${what} stopped: ${result.generation.workflow.last_error ?? result.generation.workflow.state}`,
			);
		}
		if (done(result)) return result;
		await sleep(2_000);
	}
	throw new Error(`${what} did not complete in time`);
}

async function writeCandidateFiles(
	client: MCPConnectorClient,
	workflowID: string,
	files: Record<string, string>,
): Promise<void> {
	for (const [filePath, content] of Object.entries(files)) {
		const written = (await client.callTool("write_workspace_file", {
			workflow_id: workflowID,
			path: filePath,
			content,
		})) as { workflow_id: string; path: string };
		expect(written.workflow_id).toBe(workflowID);
		expect(written.path).toBe(filePath);
	}
}

// A submission settles at either content review (Judge passed and Verify
// passed) or Generating with durable feedback (Judge or Verify sent the
// candidate back). Both are valid observable outcomes of the state machine.
function submissionSettled(
	client: MCPConnectorClient,
	workflowID: string,
	what: string,
	timeoutMilliseconds: number,
): Promise<GenerationResult> {
	return waitForGeneration(
		client,
		workflowID,
		(result) => {
			const state = result.generation.workflow.state;
			const passed =
				state === "NeedsAuthorReview" &&
				result.generation.verification?.passed === true &&
				Boolean(result.review);
			const rejected =
				state === "Generating" && Boolean(result.generation.workflow.last_error);
			return passed || rejected;
		},
		timeoutMilliseconds,
		what,
	);
}

async function expectFile(reviewPath: string, relative: string): Promise<void> {
	const target = path.join(reviewPath, relative);
	const info = await stat(target);
	expect(info.isFile(), `${target} should be a regular file`).toBe(true);
}

async function reviewFiles(root: string): Promise<string[]> {
	const result: string[] = [];
	async function walk(directory: string) {
		const entries = await readdir(directory, { withFileTypes: true });
		for (const entry of entries) {
			const target = path.join(directory, entry.name);
			if (entry.isDirectory()) {
				await walk(target);
			} else {
				result.push(target);
			}
		}
	}
	await walk(root);
	return result;
}

async function expectNoSensitiveContent(root: string, token: string): Promise<void> {
	for (const file of await reviewFiles(root)) {
		const content = await readFile(file, "utf8");
		expect(content.includes(token), `${file} must not contain the user token`).toBe(false);
		for (const secret of ["sandbox_id", "pvc_name", "opensandbox", "Bearer "]) {
			expect(content.toLowerCase().includes(secret), `${file} must not contain ${secret}`).toBe(false);
		}
	}
}

agentLiveTest("MCP connector repairs, reviews, projects, and publishes a node challenge", async () => {
	test.setTimeout(70 * 60_000);
	const baseURL = process.env.BREAKFIX_E2E_BASE_URL?.trim();
	if (!baseURL) throw new Error("BREAKFIX_E2E_BASE_URL is required");

	const username = `mcp-${Date.now()}-${Math.random().toString(36).slice(2, 7)}`;
	const password = "test-password-123";
	const token = await registerAndGetToken(baseURL, username, password);

	const directory = await mkdtemp(path.join(tmpdir(), "breakfix-mcp-e2e-"));
	const configPath = path.join(directory, "mcp.yaml");
	await writeFile(
		configPath,
		`server_url: ${baseURL.replace(/\/$/, "")}/api\ntoken_env: BREAKFIX_E2E_MCP_TOKEN\n`,
	);
	const binaryPath = connectorBinaryPath();
	const client = new MCPConnectorClient(binaryPath, configPath, token);
	let workflowID = "";
	let candidateRevisionID = "";
	let publishedTitle = "";
	try {
		await client.initialize();
		const toolNames = (await client.listTools()).map((tool) => tool.name);
		for (const expected of [
			"set_generation_plan",
			"confirm_generation",
			"list_workspace_files",
			"write_workspace_file",
			"run_workspace_command",
			"submit_candidate",
			"get_generation",
			"sync_review",
			"confirm_content",
			"confirm_classification_and_publish",
		]) {
			expect(toolNames, "connector should expose the shared generator tools").toContain(expected);
		}

		const savedPlan = (await client.callTool("set_generation_plan", {
			expected_revision: 0,
			idempotency_key: "mcp-e2e-plan-1",
			plan,
		})) as { session_id: string; plan_revision: number };
		expect(savedPlan.session_id).toBeTruthy();
		expect(savedPlan.plan_revision).toBeGreaterThan(0);

		const workflow = (await client.callTool("confirm_generation", {
			session_id: savedPlan.session_id,
			plan_revision: savedPlan.plan_revision,
			idempotency_key: "mcp-e2e-confirm-1",
		})) as Workflow;
		workflowID = workflow.id;
		expect(workflow.state).toBe("Generating");

		const firstListing = (await client.callTool("list_workspace_files", {
			workflow_id: workflowID,
		})) as { files: Array<{ path: string }> };
		expect(firstListing.files).toEqual([]);

		await writeCandidateFiles(client, workflowID, candidateFiles(brokenAnswer));
		const command = (await client.callTool("run_workspace_command", {
			workflow_id: workflowID,
			command: "find . -type f | sort",
		})) as { exit_code: number; output: string };
		expect(command.exit_code).toBe(0);
		expect(command.output).toContain("challenge.yaml");
		expect(command.output).toContain("nodes/host/answer.sh");

		await client.callTool("submit_candidate", {
			workflow_id: workflowID,
			idempotency_key: "mcp-e2e-submit-broken",
		});

		// The deliberately broken answer must be sent back to the Generator by
		// the Judge (and would also be rejected by Verify if the Judge passed).
		let settled = await submissionSettled(
			client,
			workflowID,
			"broken candidate rejection",
			20 * 60_000,
		);
		expect(settled.generation.workflow.state).toBe("Generating");
		expect(settled.generation.workflow.last_error).toBeTruthy();

		// Repair against the durable feedback and resubmit. One repair rewrites
		// the complete candidate, and any residual Judge feedback is answered
		// by the same user-requested repair loop instead of failing the run.
		await writeCandidateFiles(client, workflowID, candidateFiles(fixedAnswer));
		await client.callTool("submit_candidate", {
			workflow_id: workflowID,
			idempotency_key: "mcp-e2e-submit-fixed",
		});
		settled = await submissionSettled(
			client,
			workflowID,
			"repaired candidate review",
			40 * 60_000,
		);
		for (
			let attempt = 0;
			attempt < 3 && settled.generation.workflow.state !== "NeedsAuthorReview";
			attempt++
		) {
			await writeCandidateFiles(client, workflowID, candidateFiles(fixedAnswer));
			await client.callTool("submit_candidate", {
				workflow_id: workflowID,
				idempotency_key: `mcp-e2e-submit-repair-${attempt + 1}`,
			});
			settled = await submissionSettled(
				client,
				workflowID,
				"repaired candidate review",
				40 * 60_000,
			);
		}
		expect(settled.generation.workflow.state).toBe("NeedsAuthorReview");
		const reviewed = settled;
		candidateRevisionID = reviewed.generation.candidate?.id ?? "";
		expect(candidateRevisionID).toBeTruthy();
		const contentReview = reviewed.review;
		if (!contentReview) throw new Error("content review projection is missing");
		expect(contentReview.kind).toBe("content");
		expect(contentReview.candidate_revision_id).toBe(candidateRevisionID);

		const reviewRoot = path.dirname(path.dirname(path.dirname(contentReview.review_path)));
		await expectFile(contentReview.review_path, "manifest.json");
		await expectFile(contentReview.review_path, "overview.md");
		await expectFile(contentReview.review_path, "judge.md");
		await expectFile(contentReview.review_path, "verification.md");
		await expectFile(contentReview.review_path, "checkpoints/runtime-marker-ready.md");
		await expectFile(contentReview.review_path, "candidate/challenge.yaml");
		await expectFile(contentReview.review_path, "candidate/problem.md");
		const manifest = JSON.parse(
			await readFile(path.join(contentReview.review_path, "manifest.json"), "utf8"),
		) as Record<string, unknown>;
		expect(manifest.workflow_id).toBe(workflowID);
		expect(manifest.candidate_revision_id).toBe(candidateRevisionID);
		expect(manifest.kind).toBe("content");
		await expectNoSensitiveContent(path.join(reviewRoot, workflowID), token);

		// Syncing the same immutable snapshot is idempotent and deleting the
		// disposable projection is recovered from the Server authority.
		const resynced = (await client.callTool("sync_review", { workflow_id: workflowID })) as {
			review_path: string;
		};
		expect(resynced.review_path).toBe(contentReview.review_path);
		await rm(path.join(reviewRoot, workflowID), { recursive: true, force: true });
		const restored = (await client.callTool("sync_review", { workflow_id: workflowID })) as {
			review_path: string;
		};
		expect(restored.review_path).toBe(contentReview.review_path);
		await expectFile(contentReview.review_path, "manifest.json");

		// A different user token cannot read this workflow or its review.
		const otherToken = await registerAndGetToken(baseURL, `${username}-other`, password);
		const otherRead = await fetch(`${baseURL}/api/generator/workflows/${workflowID}`, {
			headers: { Authorization: `Bearer ${otherToken}` },
		});
		expect(otherRead.status).toBe(404);

		await client.callTool("confirm_content", {
			workflow_id: workflowID,
			candidate_revision_id: candidateRevisionID,
			idempotency_key: "mcp-e2e-confirm-content",
		});

		// The candidate is a Node runtime validation scenario that belongs to
		// the prepared Roadmap's existing topic, so the Server Classifier must
		// return a reviewable proposal; clients never write topic/tag data.
		const classificationReview = await waitForGeneration(
			client,
			workflowID,
			(result) =>
				result.generation.workflow.state === "NeedsClassificationReview" &&
				result.generation.classification?.result === "proposed" &&
				Boolean(result.review),
			20 * 60_000,
			"classification review",
		);
		const proposalRevision = classificationReview.generation.classification?.revision ?? 0;
		expect(proposalRevision).toBeGreaterThan(0);
		const classificationProjection = classificationReview.review;
		if (!classificationProjection) throw new Error("classification review projection is missing");
		expect(classificationProjection.kind).toBe("classification");
		expect(classificationProjection.proposal_revision).toBe(proposalRevision);
		await expectFile(classificationProjection.review_path, "manifest.json");
		await expectFile(classificationProjection.review_path, "topic.md");
		await expectFile(classificationProjection.review_path, "tags.md");
		await expectNoSensitiveContent(path.join(reviewRoot, workflowID), token);

		await client.callTool("confirm_classification_and_publish", {
			workflow_id: workflowID,
			candidate_revision_id: candidateRevisionID,
			proposal_revision: proposalRevision,
			idempotency_key: "mcp-e2e-publish",
		});
		await waitForGeneration(
			client,
			workflowID,
			(result) => result.generation.workflow.state === "Published",
			20 * 60_000,
			"challenge publication",
		);
		publishedTitle = plan.metadata.title;
	} finally {
		await client.close();
		await rm(directory, { recursive: true, force: true });
	}

	// The published challenge enters the same public Catalog as a web-authored
	// challenge.
	await expect
		.poll(
			async () => {
				const response = await fetch(`${baseURL}/api/challenges`);
				if (!response.ok) throw new Error(await response.text());
				const body = (await response.json()) as {
					challenges: Array<{ title: string }>;
				};
				return body.challenges.some((challenge) => challenge.title === publishedTitle);
			},
			{ timeout: 15 * 60_000, intervals: [1_000, 2_000, 5_000] },
		)
		.toBe(true);
});
