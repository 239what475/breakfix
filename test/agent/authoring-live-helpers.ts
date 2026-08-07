import { expect, type Page } from "@playwright/test";
import type { AuthoringSession } from "../../web/src/api/generated";

async function readAuthoringSession(page: Page, sessionID: string): Promise<AuthoringSession> {
	return page.evaluate(async (id) => {
		const response = await fetch(`/api/authoring/sessions/${id}`, {
			headers: { Authorization: `Bearer ${localStorage.getItem("token") ?? ""}` },
		});
		if (!response.ok) throw new Error(await response.text());
		return response.json() as Promise<AuthoringSession>;
	}, sessionID);
}

export async function waitForVerifiedCandidate(page: Page, sessionID: string): Promise<string> {
	let candidateID = "";
	let terminalError = "";
	await expect.poll(async () => {
		const snapshot = await readAuthoringSession(page, sessionID);
		if (snapshot.workflow?.state === "Failed" || snapshot.workflow?.state === "Cancelled") {
			terminalError = `candidate workflow stopped: ${snapshot.workflow.last_error ?? snapshot.workflow.state}`;
			return true;
		}
		candidateID = snapshot.candidate?.id ?? "";
		return snapshot.workflow?.state === "NeedsAuthorReview" && snapshot.verification?.passed === true && candidateID !== "";
	}, { timeout: 30 * 60_000, intervals: [1_000, 2_000, 5_000] }).toBe(true);
	if (terminalError) throw new Error(terminalError);
	return candidateID;
}

export async function waitForPublishedChallenge(page: Page, sessionID: string): Promise<string> {
	let challengeID = "";
	let terminalError = "";
	await expect.poll(async () => {
		const snapshot = await readAuthoringSession(page, sessionID);
		if (snapshot.workflow?.state === "Failed" || snapshot.workflow?.state === "Cancelled") {
			terminalError = `challenge publication stopped: ${snapshot.workflow.last_error ?? snapshot.workflow.state}`;
			return true;
		}
		challengeID = snapshot.publish_challenge_id ?? "";
		return snapshot.state === "Published" && challengeID !== "";
	}, { timeout: 10 * 60_000, intervals: [1_000, 2_000, 5_000] }).toBe(true);
	if (terminalError) throw new Error(terminalError);
	return challengeID;
}

export async function waitForCatalogChallenge(page: Page, challengeID: string): Promise<void> {
	await expect.poll(async () => {
		return page.evaluate(async (id) => {
			const response = await fetch("/api/challenges");
			if (!response.ok) throw new Error(await response.text());
			const body = (await response.json()) as { challenges: Array<{ id: string }> };
			return body.challenges.some((challenge) => challenge.id === id);
		}, challengeID);
	}, { timeout: 15 * 60_000, intervals: [1_000, 2_000, 5_000] }).toBe(true);
}

export async function waitForClassificationReview(page: Page, sessionID: string): Promise<void> {
	let terminalError = "";
	await expect.poll(async () => {
		const snapshot = await readAuthoringSession(page, sessionID);
		if (snapshot.workflow?.state === "Failed" || snapshot.workflow?.state === "Cancelled") {
			terminalError = `classification stopped: ${snapshot.workflow.last_error ?? snapshot.workflow.state}`;
			return true;
		}
		return snapshot.workflow?.state === "NeedsClassificationReview" && snapshot.classification?.result === "proposed";
	}, { timeout: 10 * 60_000, intervals: [1_000, 2_000, 5_000] }).toBe(true);
	if (terminalError) throw new Error(terminalError);
}
