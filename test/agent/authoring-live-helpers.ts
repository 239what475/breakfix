import type { Page } from "@playwright/test";

export type AuthoringSnapshot = {
	state: "DraftConversation" | "IntentReview" | "Published";
	workflow?: {
		state: "Queued" | "Generating" | "Judging" | "Building" | "ArtifactPublishing" | "Verifying" | "NeedsAuthorReview" | "ChallengePublishing" | "CleaningUp" | "Completed" | "Failed" | "Cancelled";
		candidate_revision_id?: string;
		last_error?: string;
	};
	publish_challenge_id?: string;
	candidate?: { id: string };
	verification?: { passed: boolean };
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
		if (snapshot.workflow?.state === "NeedsAuthorReview" && snapshot.verification?.passed && snapshot.candidate?.id) {
			return snapshot.candidate.id;
		}
		if (snapshot.workflow?.state === "Failed" || snapshot.workflow?.state === "Cancelled") {
			throw new Error(`candidate workflow stopped: ${snapshot.workflow.last_error ?? snapshot.workflow.state}`);
		}
		await page.waitForTimeout(1_000);
	}
	throw new Error(`authoring session ${sessionID} did not produce a verified CandidateRevision before timeout`);
}

export async function waitForPublishedChallenge(page: Page, sessionID: string): Promise<string> {
	const deadline = Date.now() + 10 * 60_000;
	while (Date.now() < deadline) {
		const snapshot = await readAuthoringSession(page, sessionID);
		if (snapshot.state === "Published" && snapshot.publish_challenge_id) {
			return snapshot.publish_challenge_id;
		}
		if (snapshot.workflow?.state === "Failed" || snapshot.workflow?.state === "Cancelled") {
			throw new Error(`challenge publication stopped: ${snapshot.workflow.last_error ?? snapshot.workflow.state}`);
		}
		await page.waitForTimeout(1_000);
	}
	throw new Error(`authoring session ${sessionID} did not publish before timeout`);
}

export async function waitForCatalogChallenge(page: Page, challengeID: string): Promise<void> {
	const deadline = Date.now() + 15 * 60_000;
	while (Date.now() < deadline) {
		const published = await page.evaluate(async (id) => {
			const response = await fetch("/api/challenges");
			if (!response.ok) throw new Error(await response.text());
			const body = (await response.json()) as { challenges: Array<{ id: string }> };
			return body.challenges.some((challenge) => challenge.id === id);
		}, challengeID);
		if (published) return;
		await page.waitForTimeout(1_000);
	}
	throw new Error(`published challenge ${challengeID} did not enter the public catalog before timeout`);
}
