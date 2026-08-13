import { setTimeout as sleep } from "node:timers/promises";

const connectionTimeout = 30_000;

function configuredBaseURL() {
	const value = process.env.BREAKFIX_E2E_BASE_URL?.trim();
	if (!value) {
		throw new Error(
			"BREAKFIX_E2E_BASE_URL is required; use make test-e2e, make test-e2e-node, make test-e2e-recovery, make test-acceptance-node, or make test-acceptance-mcp",
		);
	}
	const url = new URL(value);
	if (url.protocol !== "http:" && url.protocol !== "https:") {
		throw new Error(`BREAKFIX_E2E_BASE_URL must be an HTTP(S) URL, got ${value}`);
	}
	url.pathname = "";
	url.search = "";
	url.hash = "";
	return url.toString().replace(/\/$/, "");
}

async function serverReady(baseURL: string) {
	try {
		const response = await fetch(`${baseURL}/readyz`, { signal: AbortSignal.timeout(1_000) });
		return response.ok;
	} catch {
		return false;
	}
}

// Platform preparation owns Catalog installation and its longer bounded wait.
// Playwright only verifies that its explicitly supplied connection is ready.
export default async function globalSetup() {
	const baseURL = configuredBaseURL();
	const deadline = Date.now() + connectionTimeout;
	while (Date.now() < deadline) {
		if (await serverReady(baseURL)) return;
		await sleep(250);
	}
	throw new Error(`could not reach prepared Breakfix server at ${baseURL} within ${connectionTimeout / 1_000}s`);
}
