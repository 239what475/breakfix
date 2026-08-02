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
		await ensureCatalogRelease(serverURL);
	} catch (error) {
		if (cleanup) await cleanup();
		throw error;
	}
	return cleanup;
}

async function ensureCatalogRelease(serverURL: string) {
	const existing = await readCatalog(serverURL);
	if (existing.length > 0) {
		if (existing.some((challenge) => challenge.title === nodeRuntimeFixture.title)) return;
		throw new Error("target Catalog is not the dedicated Breakfix E2E fixture; use an empty test platform");
	}

	const bundle = process.env.BREAKFIX_E2E_CATALOG_REFERENCE?.trim();
	const token = process.env.BREAKFIX_CATALOG_ADMIN_TOKEN?.trim();
	if (!bundle || !token) {
		throw new Error(
			"catalog is empty; set BREAKFIX_E2E_CATALOG_REFERENCE to an immutable fixture release and BREAKFIX_CATALOG_ADMIN_TOKEN before running E2E",
		);
	}
	const install = await fetch(`${serverURL}/api/admin/catalog/releases`, {
		method: "POST",
		headers: {
			"Content-Type": "application/json",
			"X-Breakfix-Catalog-Token": token,
		},
		body: JSON.stringify({ bundle }),
	});
	if (!install.ok) throw new Error(`install E2E catalog release: ${await install.text()}`);
	const release = (await install.json()) as { id?: string };
	if (!release.id) throw new Error("catalog release installation returned no release id");

	const deadline = Date.now() + catalogInstallTimeout;
	while (Date.now() < deadline) {
		const response = await fetch(`${serverURL}/api/admin/catalog/releases/${encodeURIComponent(release.id)}`, {
			headers: { "X-Breakfix-Catalog-Token": token },
		});
		if (!response.ok) throw new Error(`read E2E catalog release: ${await response.text()}`);
		const current = (await response.json()) as { state?: string; last_error?: string };
		if (current.state === "Ready") {
			const installed = await readCatalog(serverURL);
			if (installed.some((challenge) => challenge.title === nodeRuntimeFixture.title)) return;
			throw new Error("installed E2E Catalog release does not contain the node runtime fixture");
		}
		if (current.state === "Failed") {
			throw new Error(`E2E catalog release failed${current.last_error ? `: ${current.last_error}` : ""}`);
		}
		await sleep(1_000);
	}
	throw new Error(`timed out waiting for E2E catalog release ${release.id} to become Ready`);
}

async function readCatalog(serverURL: string): Promise<Array<{ title?: string }>> {
	const response = await fetch(`${serverURL}/api/challenges`);
	if (!response.ok) throw new Error(`read catalog before E2E: ${await response.text()}`);
	const body = (await response.json()) as { challenges?: Array<{ title?: string }> };
	return body.challenges ?? [];
}
