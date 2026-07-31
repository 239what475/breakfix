import { spawn, type ChildProcess } from "node:child_process";
import { setTimeout as sleep } from "node:timers/promises";

const defaultServerURL = "http://localhost:9090";
const namespace = process.env.BREAKFIX_RUNTIME_NAMESPACE ?? "breakfix-system";

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
	// A caller that supplies an endpoint owns its reachability. The default
	// path makes cluster-backed browser suites independently runnable on Kind.
	if (process.env.BREAKFIX_E2E_BASE_URL) return;
	if (await serverReady(defaultServerURL)) return;

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
		if (await serverReady(defaultServerURL)) return () => stopPortForward(portForward);
		if (portForward.exitCode !== null) break;
		await sleep(250);
	}
	await stopPortForward(portForward);
	throw new Error(`could not reach ${defaultServerURL} through breakfix-server port-forward${stderr ? `: ${stderr.trim()}` : ""}`);
}
