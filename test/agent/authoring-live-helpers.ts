import { execFile as execFileCallback } from "node:child_process";
import { promisify } from "node:util";
import type { Page } from "@playwright/test";

const execFile = promisify(execFileCallback);

type AuthoringSnapshot = {
	state?: string;
	verify_task_id?: string;
};

export async function waitForInitialVerifyTask(page: Page, sessionID: string): Promise<string> {
	const deadline = Date.now() + 10 * 60_000;
	while (Date.now() < deadline) {
		const snapshot = await page.evaluate(async (id) => {
			const response = await fetch(`/api/authoring/sessions/${id}`, {
				headers: { Authorization: `Bearer ${localStorage.getItem("token") ?? ""}` },
			});
			if (!response.ok) throw new Error(await response.text());
			return response.json() as Promise<AuthoringSnapshot>;
		}, sessionID);
		if (snapshot.verify_task_id) return snapshot.verify_task_id;
		if (snapshot.state === "VerificationInfrastructureFailed") {
			throw new Error("authoring generation stopped on an infrastructure failure");
		}
		await page.waitForTimeout(1_000);
	}
	throw new Error("initial VerifyTask was not created before timeout");
}

export async function waitForVerifyTaskSucceeded(taskID: string): Promise<void> {
	const deadline = Date.now() + 20 * 60_000;
	while (Date.now() < deadline) {
		const { stdout } = await execFile("kubectl", ["-n", "breakfix-system", "get", "verifytask", taskID, "-o", "json"], {
			encoding: "utf8",
		});
		const task = JSON.parse(stdout) as { status?: { phase?: string; report?: { summary?: string } } };
		if (task.status?.phase === "Succeeded") return;
		if (task.status?.phase === "Failed") {
			throw new Error(`initial VerifyTask failed: ${task.status.report?.summary ?? "no report"}`);
		}
		await new Promise((resolve) => setTimeout(resolve, 1_000));
	}
	throw new Error(`VerifyTask ${taskID} did not complete before timeout`);
}
