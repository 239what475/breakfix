import { spawn, type ChildProcess } from "node:child_process";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { expect, test } from "@playwright/test";

const taxonomyLiveTest = process.env.RUN_TAXONOMY_LIVE_E2E === "1" ? test : test.skip;
const projectRoot = resolve(dirname(fileURLToPath(import.meta.url)), "../..");

type TaxonomyStatus = {
	initial_snapshot_absent: boolean;
	catalog_mapped: boolean;
	current_revision?: string;
	mappings: Array<{ id: string; state: string; active_run_id?: string; last_error?: string }>;
	runs: Array<{ id: string; purpose: string; status: string; attempt: number; last_error?: string }>;
	snapshot_error?: string;
};

type Harness = {
	url: string;
	process: ChildProcess;
	logs: string[];
	stop: () => Promise<void>;
};

async function runCommand(command: string, args: string[], logs: string[]): Promise<void> {
	const child = spawn(command, args, { cwd: projectRoot, env: process.env, stdio: ["ignore", "pipe", "pipe"] });
	child.stdout?.setEncoding("utf8");
	child.stderr?.setEncoding("utf8");
	child.stdout?.on("data", (chunk: string) => logs.push(...chunk.split(/\r?\n/).filter(Boolean)));
	child.stderr?.on("data", (chunk: string) => logs.push(...chunk.split(/\r?\n/).filter(Boolean)));
	await new Promise<void>((resolveCommand, reject) => {
		child.once("exit", (code, signal) => {
			if (code === 0) resolveCommand();
			else reject(new Error(`${command} ${args.join(" ")} failed (code=${code}, signal=${signal})`));
		});
		child.once("error", (error) => reject(new Error(`start ${command}: ${error.message}`)));
	});
}

async function stopProcess(child: ChildProcess): Promise<void> {
	if (child.exitCode !== null || child.signalCode !== null) return;
	const exited = new Promise<void>((resolve) => child.once("exit", () => resolve()));
	child.kill("SIGTERM");
	const forced = setTimeout(() => child.kill("SIGKILL"), 15_000);
	await exited;
	clearTimeout(forced);
}

function requireDatabaseURL() {
	const value = process.env.BREAKFIX_TAXONOMY_E2E_DATABASE_URL?.trim();
	if (!value) throw new Error("BREAKFIX_TAXONOMY_E2E_DATABASE_URL is required for taxonomy live E2E");
	return value;
}

async function startHarness(): Promise<Harness> {
	const logs: string[] = [];
	const binaryDir = await mkdtemp(join(tmpdir(), "breakfix-taxonomy-e2e-bin-"));
	const binary = join(binaryDir, "taxonomy-e2e");
	try {
		await runCommand("go", ["build", "-o", binary, "./cmd/taxonomy-e2e"], logs);
	} catch (error) {
		await rm(binaryDir, { recursive: true, force: true });
		throw new Error(`${error instanceof Error ? error.message : String(error)}\n${logs.join("\n")}`);
	}
	const child = spawn(binary, ["-database-url", requireDatabaseURL()], {
		cwd: projectRoot,
		env: process.env,
		stdio: ["ignore", "pipe", "pipe"],
	});
	child.stdout?.setEncoding("utf8");
	child.stderr?.setEncoding("utf8");
	child.stdout?.on("data", (chunk: string) => logs.push(...chunk.split(/\r?\n/).filter(Boolean)));
	child.stderr?.on("data", (chunk: string) => logs.push(...chunk.split(/\r?\n/).filter(Boolean)));

	let url: string;
	try {
		url = await new Promise<string>((resolveURL, reject) => {
			const deadline = setTimeout(() => reject(new Error(`taxonomy E2E harness did not start\n${logs.join("\n")}`)), 90_000);
			const checkURL = () => {
				const line = logs.find((value) => value.startsWith("TAXONOMY_E2E_URL="));
				if (!line) return;
				clearTimeout(deadline);
				resolveURL(line.slice("TAXONOMY_E2E_URL=".length));
			};
			child.stdout?.on("data", checkURL);
			child.once("exit", (code, signal) => {
				clearTimeout(deadline);
				reject(new Error(`taxonomy E2E harness exited before startup (code=${code}, signal=${signal})\n${logs.join("\n")}`));
			});
			child.once("error", (error) => {
				clearTimeout(deadline);
				reject(new Error(`start taxonomy E2E harness: ${error.message}\n${logs.join("\n")}`));
			});
		});
	} catch (error) {
		await stopProcess(child).catch(() => undefined);
		await rm(binaryDir, { recursive: true, force: true });
		throw error;
	}

	return {
		url,
		process: child,
		logs,
		async stop() {
			try {
				await stopProcess(child);
			} finally {
				await rm(binaryDir, { recursive: true, force: true });
			}
		},
	};
}

async function status(baseURL: string): Promise<TaxonomyStatus> {
	const response = await fetch(`${baseURL}/__taxonomy-e2e/status`);
	if (!response.ok) throw new Error(`read taxonomy E2E status: ${response.status} ${await response.text()}`);
	return response.json() as Promise<TaxonomyStatus>;
}

function diagnostics(snapshot: TaxonomyStatus | undefined, logs: string[]) {
	return `taxonomy status: ${JSON.stringify(snapshot)}\nharness logs:\n${logs.join("\n")}`;
}

function terminalFailure(snapshot: TaxonomyStatus) {
	if (snapshot.snapshot_error) return snapshot.snapshot_error;
	const work = snapshot.mappings.find((item) => item.state === "Failed" || item.state === "Cancelled");
	if (work) return `taxonomy work ${work.id} entered ${work.state}: ${work.last_error ?? "no error"}`;
	const run = snapshot.runs.find((item) => item.status === "failed" || item.status === "cancelled");
	if (run) return `taxonomy agent run ${run.id} (${run.purpose}) entered ${run.status}: ${run.last_error ?? "no error"}`;
	return "";
}

taxonomyLiveTest("real taxonomy committee publishes cleanup-logs and projects it in the browser", async ({ page }) => {
	test.setTimeout(12 * 60_000);
	let harness: Harness | undefined;
	let latest: TaxonomyStatus | undefined;
	try {
		harness = await startHarness();
		latest = await status(harness.url);
		expect(latest.initial_snapshot_absent).toBe(true);

		await expect.poll(async () => {
			latest = await status(harness!.url);
			const failure = terminalFailure(latest);
			if (failure) throw new Error(failure);
			return latest.catalog_mapped;
		}, { timeout: 10 * 60_000, intervals: [500, 1_000, 2_000, 5_000] }).toBe(true);
		expect(latest.current_revision).toBeTruthy();
		expect(latest.mappings.some((item) => item.state === "Published")).toBe(true);
		expect(latest.runs.some((run) => run.purpose === "taxonomy-mapper" && run.status === "succeeded")).toBe(true);
		expect(latest.runs.some((run) => run.purpose === "taxonomy-review" && run.status === "succeeded")).toBe(true);

		const catalogResponse = await fetch(`${harness.url}/api/challenges`);
		expect(catalogResponse.ok).toBe(true);
		const catalog = (await catalogResponse.json()) as {
			challenges: Array<{ id: string; title: string; tags: Array<{ id: string; title: string }>; primary_outcome: { id: string; title: string } }>;
		};
		expect(catalog.challenges).toHaveLength(1);
		const challenge = catalog.challenges[0];
		expect(challenge.id).toBe("chal-r7m4x2q9v6kp");
		expect(challenge.tags.length).toBeGreaterThan(0);
		expect(challenge.primary_outcome.id).toMatch(/^skill-[a-f0-9]{16}$/);

		await page.setViewportSize({ width: 1440, height: 900 });
		await page.goto(harness.url);
		const card = page.locator(`article.challenge-card[data-challenge-id="${challenge.id}"]`);
		await expect(card).toBeVisible();
		for (const tag of challenge.tags) await expect(card.locator(".challenge-tags")).toContainText(tag.title);
		await expect(card.locator(".challenge-primary-skill")).toHaveText(`Practice: ${challenge.primary_outcome.title}`);
	} catch (error) {
		if (harness) latest = await status(harness.url).catch(() => latest);
		throw new Error(`${error instanceof Error ? error.message : String(error)}\n${diagnostics(latest, harness?.logs ?? [])}`);
	} finally {
		await harness?.stop();
	}
});
