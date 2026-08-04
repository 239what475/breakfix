import { spawn, type ChildProcess } from "node:child_process";
import { setTimeout as sleep } from "node:timers/promises";
import { nodeRuntimeFixture } from "./support/catalog-fixture";

const defaultServerURL = "http://localhost:9090";
const namespace = process.env.BREAKFIX_RUNTIME_NAMESPACE ?? "breakfix-system";
const catalogInstallTimeout = 20 * 60_000;

async function serverReady(url: string) {
	try {
		const response = await fetch(`${url}/readyz`, { signal: AbortSignal.timeout(1_000) });
		return response.ok;
	} catch {
		return false;
	}
}

async function stopPortForward(process: ChildProcess) {
	if (process.exitCode !== null) return;
	const exited = new Promise<void>((resolve) => process.once("exit", () => resolve()));
	process.kill("SIGTERM");
	await Promise.race([exited, sleep(5_000)]);
	if (process.exitCode === null) process.kill("SIGKILL");
}

export default async function globalSetup() {
	const serverURL = (process.env.BREAKFIX_E2E_BASE_URL ?? defaultServerURL).replace(/\/$/, "");
	let cleanup: (() => Promise<void>) | undefined;
	if (process.env.BREAKFIX_E2E_BASE_URL) {
		if (!(await serverReady(serverURL))) {
			throw new Error(`could not reach configured Breakfix server at ${serverURL}`);
		}
	} else if (await serverReady(serverURL)) {
		cleanup = undefined;
	} else {
		let stderr = "";
		const portForward = spawn(
			"kubectl",
			["port-forward", "-n", namespace, "service/breakfix-server", "9090:9090"],
			{ stdio: ["ignore", "ignore", "pipe"] },
		);
		portForward.stderr?.on("data", (chunk: Buffer) => {
			stderr += chunk.toString("utf8");
		});

		const deadline = Date.now() + 30_000;
		while (Date.now() < deadline) {
			if (await serverReady(serverURL)) {
				cleanup = () => stopPortForward(portForward);
				break;
			}
			if (portForward.exitCode !== null) break;
			await sleep(250);
		}
		if (!cleanup) {
			await stopPortForward(portForward);
			throw new Error(`could not reach ${serverURL} through breakfix-server port-forward${stderr ? `: ${stderr.trim()}` : ""}`);
		}
	}

	try {
		await waitForConfiguredCatalog(serverURL);
	} catch (error) {
		if (cleanup) await cleanup();
		throw error;
	}
	return cleanup;
}

async function waitForConfiguredCatalog(serverURL: string) {
	const deadline = Date.now() + catalogInstallTimeout;
	while (Date.now() < deadline) {
		try {
			const installed = await readCatalog(serverURL);
			if (installed.some((challenge) => challenge.title === nodeRuntimeFixture.title)) return;
		} catch {
			// Server may still be recovering its configured immutable release.
		}
		await sleep(1_000);
	}
	throw new Error(
		`timed out waiting for the configured Catalog Release to expose ${nodeRuntimeFixture.title}; configure the Server with the immutable E2E fixture reference before running browser tests`,
	);
}

async function readCatalog(serverURL: string): Promise<Array<{ title?: string }>> {
	const response = await fetch(`${serverURL}/api/challenges`);
	if (!response.ok) throw new Error(`read catalog before E2E: ${await response.text()}`);
	const body = (await response.json()) as { challenges?: Array<{ title?: string }> };
	return body.challenges ?? [];
}
