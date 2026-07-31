import type { Page } from "@playwright/test";

export type AuthoringSnapshot = {
	state: string;
	pipeline_state?: string;
	last_error?: string;
	publish_challenge_id?: string;
	candidate?: { id: string; state: string };
};

async function readAuthoringSession(page: Page, sessionID: string): Promise<AuthoringSnapshot> {
	return page.evaluate(async (id) => {
		const response = await fetch(`/api/authoring/sessions/${id}`, {
			headers: { Authorization: `Bearer ${localStorage.getItem("token") ?? ""}` },
		});
		if (!response.ok) throw new Error(await response.text());
		return response.json() as Promise<AuthoringSnapshot>;
	}, sessionID);
}

export async function waitForVerifiedCandidate(page: Page, sessionID: string): Promise<string> {
	const deadline = Date.now() + 30 * 60_000;
	while (Date.now() < deadline) {
		const snapshot = await readAuthoringSession(page, sessionID);
		if (
			snapshot.state === "AwaitingVerifiedReview" &&
			snapshot.pipeline_state === "Verified" &&
			snapshot.candidate?.state === "Verified"
		) {
			return snapshot.candidate.id;
		}
		if (snapshot.state === "InfrastructureFailed") {
			throw new Error(`candidate pipeline failed: ${snapshot.last_error ?? "infrastructure failure"}`);
		}
		await page.waitForTimeout(1_000);
	}
	throw new Error(`authoring session ${sessionID} did not produce a verified CandidateRevision before timeout`);
}

export async function waitForPublishedChallenge(page: Page, sessionID: string): Promise<string> {
	const deadline = Date.now() + 10 * 60_000;
	while (Date.now() < deadline) {
		const snapshot = await readAuthoringSession(page, sessionID);
		if (
			snapshot.state === "Published" &&
			snapshot.pipeline_state === "Published" &&
			snapshot.publish_challenge_id
		) {
			return snapshot.publish_challenge_id;
		}
		if (snapshot.state === "InfrastructureFailed") {
			throw new Error(`challenge publication failed: ${snapshot.last_error ?? "infrastructure failure"}`);
		}
		await page.waitForTimeout(1_000);
	}
	throw new Error(`authoring session ${sessionID} did not publish before timeout`);
}
