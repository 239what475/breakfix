import { expect, type Page } from "@playwright/test";
import type {
	AuthoringRunEvent,
	AuthoringSession,
	GeneratorGeneration,
	GeneratorWorkflow,
} from "../../web/src/api/generated";

const sleep = (milliseconds: number) =>
	new Promise<void>((resolve) => {
		setTimeout(resolve, milliseconds);
	});

async function authorizedJSON<T>(page: Page, path: string): Promise<T> {
	return page.evaluate(async (target) => {
		const response = await fetch(target, {
			headers: { Authorization: `Bearer ${localStorage.getItem("token") ?? ""}` },
		});
		if (!response.ok) throw new Error(await response.text());
		return response.json() as Promise<T>;
	}, path);
}

export async function readAuthoringSession(page: Page, sessionID: string): Promise<AuthoringSession> {
	return authorizedJSON<AuthoringSession>(page, `/api/authoring/sessions/${sessionID}`);
}

export async function currentAuthoringSessionID(page: Page): Promise<string> {
	const session = await authorizedJSON<{ id?: unknown }>(page, "/api/authoring/sessions/current");
	if (typeof session.id !== "string" || session.id === "") {
		throw new Error("current authoring session is missing its id");
	}
	return session.id;
}

export async function readGeneration(page: Page, workflowID: string): Promise<GeneratorGeneration> {
	return authorizedJSON<GeneratorGeneration>(page, `/api/generator/workflows/${workflowID}`);
}

export async function waitForActiveWorkflow(page: Page, sessionID: string): Promise<string> {
	let workflowID = "";
	await expect
		.poll(async () => {
			const snapshot = await readAuthoringSession(page, sessionID);
			workflowID = snapshot.workflows.at(-1)?.id ?? "";
			return workflowID !== "";
		}, { timeout: 30 * 60_000, intervals: [1_000, 2_000, 5_000] })
		.toBe(true);
	return workflowID;
}

// A rejected submission is a valid intermediate result, not an infrastructure
// failure: the workflow returns to Generating with durable Judge or Verify
// feedback so a later user-requested turn can repair and resubmit.
export class CandidateRejectedError extends Error {}

function workflowStoppedError(state: string, workflow: GeneratorWorkflow): Error {
	return new Error(
		`candidate workflow stopped: ${workflow.last_error ?? state}`,
	);
}

export async function waitForVerifiedCandidate(
	page: Page,
	workflowID: string,
	timeoutMilliseconds = 30 * 60_000,
): Promise<string> {
	const deadline = Date.now() + timeoutMilliseconds;
	while (Date.now() < deadline) {
		const generation = await readGeneration(page, workflowID);
		const workflow = generation.workflow;
		if (workflow.state === "Failed" || workflow.state === "Cancelled") {
			throw workflowStoppedError(workflow.state, workflow);
		}
		if (workflow.state === "Generating" && workflow.last_error) {
			throw new CandidateRejectedError(
				`candidate returned to the generator: ${workflow.last_error}`,
			);
		}
		const candidateID = generation.candidate?.id ?? "";
		if (
			workflow.state === "NeedsAuthorReview" &&
			generation.verification?.passed === true &&
			candidateID !== ""
		) {
			return candidateID;
		}
		await sleep(2_000);
	}
	throw new Error("candidate did not reach content review in time");
}

export async function waitForPublishedChallenge(page: Page, sessionID: string): Promise<string> {
	let challengeID = "";
	await expect
		.poll(async () => {
			const snapshot = await readAuthoringSession(page, sessionID);
			if (snapshot.workflows.some((workflow) => workflow.state === "Failed" || workflow.state === "Cancelled")) {
				const stopped = snapshot.workflows.find((workflow) => workflow.state === "Failed" || workflow.state === "Cancelled");
				throw workflowStoppedError(stopped?.state ?? "Failed", stopped ?? snapshot.workflows[0]);
			}
			challengeID = snapshot.publish_challenge_id ?? "";
			return snapshot.state === "Published" && challengeID !== "";
		}, { timeout: 10 * 60_000, intervals: [1_000, 2_000, 5_000] })
		.toBe(true);
	return challengeID;
}

export async function waitForCatalogChallenge(page: Page, challengeID: string): Promise<void> {
	await expect
		.poll(async () => {
			return page.evaluate(async (id) => {
				const response = await fetch("/api/challenges");
				if (!response.ok) throw new Error(await response.text());
				const body = (await response.json()) as { challenges: Array<{ id: string }> };
				return body.challenges.some((challenge) => challenge.id === id);
			}, challengeID);
		}, { timeout: 15 * 60_000, intervals: [1_000, 2_000, 5_000] })
		.toBe(true);
}

export async function waitForClassificationReview(page: Page, workflowID: string): Promise<void> {
	const deadline = Date.now() + 10 * 60_000;
	while (Date.now() < deadline) {
		const generation = await readGeneration(page, workflowID);
		const workflow = generation.workflow;
		if (workflow.state === "Failed" || workflow.state === "Cancelled") {
			throw workflowStoppedError(workflow.state, workflow);
		}
		if (
			workflow.state === "NeedsClassificationReview" &&
			generation.classification?.result === "proposed"
		) {
			return;
		}
		await sleep(2_000);
	}
	throw new Error("classification did not reach review in time");
}

export async function sendAuthoringMessage(page: Page, content: string, sessionID?: string): Promise<void> {
	const composer = page.locator(".authoring-composer textarea");
	await expect(composer).toBeEnabled({ timeout: 30 * 60_000 });
	await composer.fill(content);
	await page.getByRole("button", { name: "发送消息", exact: true }).click();
	if (!sessionID) {
		// Before publication the next decision becomes available once the
		// durable Authoring AgentRun has finished and committed its effects.
		await expect(composer).toBeEnabled({ timeout: 30 * 60_000 });
		return;
	}
	// A publish confirmation can make the session Published before its turn
	// finishes, which permanently disables the composer. The stable session id
	// stays readable, so treat Published as completion and otherwise wait for
	// the composer to accept the next message.
	await expect
		.poll(
			async () => {
				const session = await readAuthoringSession(page, sessionID);
				if (session.state === "Published") return true;
				return composer.isEnabled().catch(() => false);
			},
			{ timeout: 30 * 60_000, intervals: [1_000, 2_000, 5_000] },
		)
		.toBe(true);
}

export async function waitForAuthoringInterruption(
	page: Page,
	sessionID: string,
	timeoutMilliseconds = 5 * 60_000,
): Promise<AuthoringRunEvent> {
	let interrupted: AuthoringRunEvent | undefined;
	await expect
		.poll(async () => {
			const session = await readAuthoringSession(page, sessionID);
			interrupted = [...session.messages]
				.reverse()
				.find((message) => message.role === "event" && message.event?.resumable)
				?.event;
			return interrupted !== undefined && !session.authoring_turn_active;
		}, { timeout: timeoutMilliseconds, intervals: [500, 1_000, 2_000] })
		.toBe(true);
	return interrupted as AuthoringRunEvent;
}

export async function continueGeneratingWorkflow(
	page: Page,
	sessionID: string,
	workflowID: string,
): Promise<void> {
	const generation = await readGeneration(page, workflowID);
	if (generation.workflow.state !== "Generating") {
		throw new Error(`workflow ${workflowID} is ${generation.workflow.state}, not Generating`);
	}
	await sendAuthoringMessage(
		page,
		`继续生成任务 ${workflowID}。先读取任务和工作区当前状态，再完成剩余工作并提交 candidate。`,
		sessionID,
	);
}

export async function expectNoLifecycleButtons(page: Page): Promise<void> {
	for (const name of ["生成并验证题目", "确认题目内容", "确认分类并发布"]) {
		await expect(page.getByRole("button", { name, exact: true })).toHaveCount(0);
	}
}
