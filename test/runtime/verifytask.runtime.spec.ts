import { execFile as execFileCallback } from "node:child_process";
import { randomBytes } from "node:crypto";
import { cp, mkdtemp, readFile, rm, stat, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { promisify } from "node:util";
import { expect, test } from "@playwright/test";
import { parse } from "yaml";
import { cleanupVerifyTask, waitForEnvironmentDeletion } from "../support/runtime-cleanup";

const runtimeTest = process.env.RUN_RUNTIME_E2E === "1" ? test : test.skip;
const execFile = promisify(execFileCallback);
const projectRoot = resolve(dirname(fileURLToPath(import.meta.url)), "../..");
const namespace = "breakfix-system";
const stageImage = "172.18.0.1:5000/break-fix/breakfix-base:latest";
const serverDataPVC = "breakfix-server-data";

type VerifyTask = {
	metadata?: {
		name?: string;
	};
	status?: {
		phase?: string;
		stage?: string;
		buildJobName?: string;
		publisherJobName?: string;
		verifierJobName?: string;
		stagingImage?: string;
		image?: string;
		report?: {
			class?: string;
			buildPassed?: boolean;
			answerPassed?: boolean;
			checkpointsPassed?: boolean;
			summary?: string;
			issues?: Array<{ code?: string; message?: string }>;
		};
	};
};

async function jobLogs(name: string | undefined) {
	if (!name) return "";
	try {
		const { stdout } = await kubectl(["-n", namespace, "logs", `job/${name}`, "--tail=200"]);
		return stdout.trim();
	} catch (error) {
		return `unable to read ${name} logs: ${String(error)}`;
	}
}

type Fixture = {
	runtime: "container" | "vcluster";
	directory: string;
	checkpointIDs: string[];
};

type ChallengeManifest = {
	runtime?: unknown;
	checkpoints?: Array<{ id?: unknown }>;
};

async function kubectl(args: string[]) {
	return execFile("kubectl", args, { cwd: projectRoot, encoding: "utf8", maxBuffer: 4 * 1024 * 1024 });
}

function resourceName(prefix: string) {
	return `${prefix}-${randomBytes(6).toString("hex")}`;
}

async function exists(kind: string, name: string) {
	try {
		await kubectl(["-n", namespace, "get", kind, name, "-o", "name"]);
		return true;
	} catch {
		return false;
	}
}

async function waitForTerminalTask(taskID: string, environmentKind: string, environmentName: string) {
	const deadline = Date.now() + 20 * 60_000;
	let environmentObserved = false;
	while (Date.now() < deadline) {
		environmentObserved ||= await exists(environmentKind, environmentName);
		const { stdout } = await kubectl(["-n", namespace, "get", "verifytask", taskID, "-o", "json"]);
		const task = JSON.parse(stdout) as VerifyTask;
		if (task.status?.phase === "Succeeded") {
			expect(environmentObserved).toBe(true);
			expect(task.status.image).toMatch(/@sha256:[a-f0-9]{64}$/);
			await assertUntrustedBuilderJob(task.status.buildJobName);
			return task;
		}
		if (task.status?.phase === "Failed") {
			const report = task.status.report;
			const issues = report?.issues?.map((issue) => `${issue.code ?? "UNKNOWN"}: ${issue.message ?? ""}`).join("\n") ?? "";
			const status = task.status;
			const logs = await jobLogs(status.stage === "Building" ? status.buildJobName : status.stage === "Publishing" ? status.publisherJobName : status.verifierJobName);
			throw new Error(`fixed ${environmentKind} artifact failed: ${report?.summary ?? "no report"}${issues ? `\n${issues}` : ""}${logs ? `\nJob logs:\n${logs}` : ""}`);
		}
		await new Promise((resolve) => setTimeout(resolve, 1_000));
	}
	throw new Error(`VerifyTask ${taskID} did not complete before timeout`);
}

async function fixtureForSourceSlug(name: string): Promise<Fixture> {
	const normalized = name.trim();
	if (!normalized || normalized.includes("/") || normalized.includes("\\") || normalized === "." || normalized === "..") {
		throw new Error("VERIFY_CHALLENGE_NAME must be one challenge source directory name");
	}
	const candidates = [
		join(projectRoot, "data", "challenges", normalized),
		join(projectRoot, "test", "fixtures", "challenges", normalized),
	];
	let directory = "";
	for (const candidate of candidates) {
		try {
			if ((await stat(candidate)).isDirectory()) {
				directory = candidate;
				break;
			}
		} catch {
			// Check the next supported source root.
		}
	}
	if (!directory) throw new Error(`challenge source ${normalized} was not found under data/challenges or test/fixtures/challenges`);
	const manifest = parse(await readFile(join(directory, "challenge.yaml"), "utf8")) as ChallengeManifest;
	if ((manifest.runtime !== "container" && manifest.runtime !== "vcluster") || !Array.isArray(manifest.checkpoints)) {
		throw new Error(`challenge source ${normalized} does not provide a valid runtime/checkpoint execution snapshot`);
	}
	const checkpointIDs = manifest.checkpoints.map((checkpoint) => typeof checkpoint?.id === "string" ? checkpoint.id.trim() : "");
	if (!checkpointIDs.length || checkpointIDs.some((id) => !id)) {
		throw new Error(`challenge source ${normalized} has an invalid checkpoint execution snapshot`);
	}
	return { directory, runtime: manifest.runtime, checkpointIDs };
}

async function waitForFailedTask(taskID: string) {
	const deadline = Date.now() + 10 * 60_000;
	while (Date.now() < deadline) {
		const { stdout } = await kubectl(["-n", namespace, "get", "verifytask", taskID, "-o", "json"]);
		const task = JSON.parse(stdout) as VerifyTask;
		if (task.status?.phase === "Failed") return task;
		if (task.status?.phase === "Succeeded") {
			throw new Error(`VerifyTask ${taskID} unexpectedly succeeded`);
		}
		await new Promise((resolve) => setTimeout(resolve, 1_000));
	}
	throw new Error(`VerifyTask ${taskID} did not fail before timeout`);
}

function registryManifestURL(image: string) {
	const firstSlash = image.indexOf("/");
	const lastColon = image.lastIndexOf(":");
	if (firstSlash <= 0 || lastColon <= firstSlash) throw new Error(`invalid staging image ${image}`);
	const registry = image.slice(0, firstSlash);
	const repository = image.slice(firstSlash + 1, lastColon);
	const reference = image.slice(lastColon + 1);
	return `http://${registry}/v2/${repository}/manifests/${reference}`;
}

async function waitForStagingImageDeletion(image: string | undefined) {
	if (!image) throw new Error("failed VerifyTask has no staging image");
	const url = registryManifestURL(image);
	const deadline = Date.now() + 3 * 60_000;
	while (Date.now() < deadline) {
		const response = await fetch(url, {
			headers: { Accept: "application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json" },
		});
		if (response.status === 404) return;
		if (!response.ok) throw new Error(`read staging manifest: ${response.status} ${await response.text()}`);
		await new Promise((resolve) => setTimeout(resolve, 1_000));
	}
	throw new Error(`staging image ${image} was not deleted after verification failure`);
}

async function jobSucceeded(name: string | undefined) {
	if (!name) return false;
	const { stdout } = await kubectl(["-n", namespace, "get", "job", name, "-o", "jsonpath={.status.succeeded}"]);
	return stdout.trim() === "1";
}

async function waitForJobSucceeded(name: string | undefined) {
	if (!name) return false;
	const deadline = Date.now() + 2 * 60_000;
	while (Date.now() < deadline) {
		if (await jobSucceeded(name)) return true;
		await new Promise((resolve) => setTimeout(resolve, 1_000));
	}
	return false;
}

async function jobFailed(name: string | undefined) {
	if (!name) return false;
	const { stdout } = await kubectl(["-n", namespace, "get", "job", name, "-o", "jsonpath={.status.failed}"]);
	return stdout.trim() === "1";
}

async function assertUntrustedBuilderJob(name: string | undefined) {
	if (!name) throw new Error("VerifyTask succeeded without a Builder job name");
	const { stdout } = await kubectl(["-n", namespace, "get", "job", name, "-o", "json"]);
	const job = JSON.parse(stdout) as {
		spec: {
			template: {
				spec: {
					automountServiceAccountToken?: boolean;
					serviceAccountName?: string;
					imagePullSecrets?: unknown[];
					volumes?: Array<{ name?: string; emptyDir?: { sizeLimit?: string } }>;
					containers: Array<{
						env?: Array<{ name?: string }>;
						envFrom?: unknown[];
						resources?: { limits?: Record<string, string> };
						securityContext?: { privileged?: boolean; runAsNonRoot?: boolean; runAsUser?: number };
					}>;
				};
			};
		};
	};
	const pod = job.spec.template.spec;
	const container = pod.containers[0];
	expect(pod.serviceAccountName ?? "").toBe("");
	expect(pod.automountServiceAccountToken).toBe(false);
	expect(pod.imagePullSecrets ?? []).toHaveLength(0);
	expect(container.envFrom ?? []).toHaveLength(0);
	expect(container.env ?? []).not.toContainEqual(expect.objectContaining({ name: expect.stringMatching(/REGISTRY|INTERNAL_API_KEY/) }));
	expect(container.securityContext?.privileged).toBe(false);
	expect(container.securityContext?.runAsNonRoot).toBe(true);
	expect(container.securityContext?.runAsUser).toBe(1000);
	expect(container.resources?.limits?.["ephemeral-storage"]).toBe("8Gi");
	expect(pod.volumes).toContainEqual(expect.objectContaining({ name: "buildkitd", emptyDir: expect.objectContaining({ sizeLimit: "8Gi" }) }));
}

async function createStagingPod(name: string, manifestPath: string) {
	const manifest = `apiVersion: v1
kind: Pod
metadata:
  name: ${name}
  namespace: ${namespace}
spec:
  restartPolicy: Never
  terminationGracePeriodSeconds: 0
  containers:
    - name: stage
      image: ${stageImage}
      imagePullPolicy: IfNotPresent
      command: ["sh", "-c", "sleep 1800"]
      volumeMounts:
        - name: data
          mountPath: /data
  volumes:
    - name: data
      persistentVolumeClaim:
        claimName: ${serverDataPVC}
`;
	await writeFile(manifestPath, manifest, "utf8");
	await kubectl(["apply", "-f", manifestPath]);
	await kubectl(["-n", namespace, "wait", `--for=condition=Ready`, `pod/${name}`, "--timeout=2m"]);
}

async function stageFixture(pod: string, submissionID: string, fixture: Fixture, archivePath: string) {
	await execFile("tar", ["-czf", archivePath, "-C", fixture.directory, "."], { cwd: projectRoot });
	const directory = `/data/submissions/${submissionID}`;
	await kubectl(["-n", namespace, "exec", pod, "--", "mkdir", "-p", directory]);
	await kubectl(["-n", namespace, "cp", archivePath, `${namespace}/${pod}:${directory}/input.tar.gz`]);
}

async function createVerifyTask(taskID: string, submissionID: string, fixture: Fixture, manifestPath: string) {
	const manifest = `apiVersion: breakfix.dev/v1
kind: VerifyTask
metadata:
  name: ${taskID}
  namespace: ${namespace}
spec:
  source:
    ref: runtime-fixture-${submissionID}
  submission:
    id: ${submissionID}
  execution:
    runtime: ${fixture.runtime}
    checkpointIds:
${fixture.checkpointIDs.map((id) => `      - ${id}`).join("\n")}
`;
	await writeFile(manifestPath, manifest, "utf8");
	await kubectl(["apply", "-f", manifestPath]);
}

async function cleanup(taskID: string, pod: string, submissionID: string) {
	try {
		await cleanupVerifyTask({ submissionID, verifyTaskID: taskID });
	} finally {
		try {
			if (await exists("pod", pod)) {
				await kubectl(["-n", namespace, "exec", pod, "--", "rm", "-rf", `/data/submissions/${submissionID}`]);
			}
		} finally {
			await kubectl(["-n", namespace, "delete", "pod", pod, "--ignore-not-found=true", "--wait=true", "--timeout=2m"]);
		}
	}
}

async function verifyFixture(fixture: Fixture) {
	const suffix = resourceName(`runtime-${fixture.runtime}`);
	const submissionID = `sub-${suffix}`;
	const taskID = `verify-${submissionID}`;
	const environmentName = `verify-env-${taskID}`;
	const environmentKind = fixture.runtime === "vcluster" ? "vclusterenvironment" : "containerenvironment";
	const temp = await mkdtemp(join(tmpdir(), "breakfix-runtime-verify-"));
	const pod = `stage-${suffix}`;
	try {
		await createStagingPod(pod, join(temp, "stage.yaml"));
		await stageFixture(pod, submissionID, fixture, join(temp, "artifact.tar.gz"));
		await createVerifyTask(taskID, submissionID, fixture, join(temp, "verifytask.yaml"));
		return await waitForTerminalTask(taskID, environmentKind, environmentName);
	} finally {
		await cleanup(taskID, pod, submissionID);
		await rm(temp, { recursive: true, force: true });
	}
}

async function verifyFailedFixture(fixture: Fixture, assertFailure: (task: VerifyTask) => Promise<void>) {
	const suffix = resourceName(`runtime-${fixture.runtime}-failed`);
	const submissionID = `sub-${suffix}`;
	const taskID = `verify-${submissionID}`;
	const environmentName = `verify-env-${taskID}`;
	const environmentKind = fixture.runtime === "vcluster" ? "vclusterenvironment" : "containerenvironment";
	const temp = await mkdtemp(join(tmpdir(), "breakfix-runtime-verify-"));
	const pod = `stage-${suffix}`;
	try {
		await createStagingPod(pod, join(temp, "stage.yaml"));
		await stageFixture(pod, submissionID, fixture, join(temp, "artifact.tar.gz"));
		await createVerifyTask(taskID, submissionID, fixture, join(temp, "verifytask.yaml"));
		const task = await waitForFailedTask(taskID);
		await assertFailure(task);
		if (task.status?.publisherJobName) {
			await waitForEnvironmentDeletion(environmentKind, environmentName);
			await waitForStagingImageDeletion(task.status.stagingImage);
		}
	} finally {
		await cleanup(taskID, pod, submissionID);
		await rm(temp, { recursive: true, force: true });
	}
}

runtimeTest.describe.configure({ mode: "serial" });

runtimeTest("fixed container artifact completes a real VerifyTask", async () => {
	test.setTimeout(25 * 60_000);
	await verifyFixture({
		runtime: "container",
		directory: join(projectRoot, "test", "fixtures", "challenges", "container-runtime-init"),
		checkpointIDs: ["runtime-marker-ready"],
	});
});

runtimeTest("fixed dependent container artifact completes a real VerifyTask", async () => {
	test.setTimeout(25 * 60_000);
	await verifyFixture({
		runtime: "container",
		directory: join(projectRoot, "test", "fixtures", "challenges", "container-checkpoint-dependency"),
		checkpointIDs: ["configuration-ready", "derived-state-ready"],
	});
});

runtimeTest("fixed vcluster artifact completes a real VerifyTask", async () => {
	test.setTimeout(25 * 60_000);
	await verifyFixture({
		runtime: "vcluster",
		directory: join(projectRoot, "test", "fixtures", "challenges", "vcluster-web-service"),
		checkpointIDs: ["deployment-ready", "service-configured"],
	});
});

runtimeTest("build failure is reported as an artifact failure before publishing", async () => {
	test.setTimeout(15 * 60_000);
	const directory = await mkdtemp(join(tmpdir(), "breakfix-runtime-build-failure-"));
	try {
		await cp(join(projectRoot, "data", "challenges", "cleanup-logs"), directory, { recursive: true });
		await writeFile(join(directory, "Dockerfile"), "ARG BREAKFIX_BASE_IMAGE=breakfix-base:latest\nFROM ${BREAKFIX_BASE_IMAGE}\nRUN exit 1\n", "utf8");
		await verifyFailedFixture({
			runtime: "container",
			directory,
			checkpointIDs: ["cleanup-script-ready", "eligible-logs-archived", "protected-logs-preserved"],
		}, async (task) => {
			expect(task.status?.report?.class).toBe("artifact");
			expect(task.status?.report?.issues).toContainEqual(expect.objectContaining({ code: "BUILD_JOB_FAILED" }));
			expect(await jobFailed(task.status?.buildJobName)).toBe(true);
			expect(task.status?.publisherJobName).toBeFalsy();
			expect(task.status?.verifierJobName).toBeFalsy();
			await waitForStagingImageDeletion(task.status?.stagingImage);
		});
	} finally {
		await rm(directory, { recursive: true, force: true });
	}
});

runtimeTest("checkpoint failure deletes the published staging image", async () => {
	test.setTimeout(20 * 60_000);
	const directory = await mkdtemp(join(tmpdir(), "breakfix-runtime-checkpoint-failure-"));
	try {
		await cp(join(projectRoot, "data", "challenges", "cleanup-logs"), directory, { recursive: true });
		await writeFile(join(directory, "answer.sh"), "#!/bin/bash\nset -euo pipefail\n", "utf8");
		await verifyFailedFixture({
			runtime: "container",
			directory,
			checkpointIDs: ["cleanup-script-ready", "eligible-logs-archived", "protected-logs-preserved"],
		}, async (task) => {
			expect(task.status?.report?.class).toBe("artifact");
			expect(task.status?.report?.buildPassed).toBe(true);
			expect(task.status?.report?.answerPassed).toBe(true);
			expect(task.status?.report?.checkpointsPassed).toBeFalsy();
			expect(task.status?.report?.issues).toContainEqual(expect.objectContaining({ code: "CHECKPOINT_CLEANUP_SCRIPT_READY" }));
			expect(await jobSucceeded(task.status?.buildJobName)).toBe(true);
			expect(await jobSucceeded(task.status?.publisherJobName)).toBe(true);
			expect(await waitForJobSucceeded(task.status?.verifierJobName)).toBe(true);
		});
	} finally {
		await rm(directory, { recursive: true, force: true });
	}
});

runtimeTest("answer failure is reported as an artifact failure after publishing", async () => {
	test.setTimeout(20 * 60_000);
	const directory = await mkdtemp(join(tmpdir(), "breakfix-runtime-answer-failure-"));
	try {
		await cp(join(projectRoot, "test", "fixtures", "challenges", "container-runtime-init"), directory, { recursive: true });
		await writeFile(join(directory, "answer.sh"), "#!/bin/bash\nset -euo pipefail\necho answer failed >&2\nexit 23\n", "utf8");
		await verifyFailedFixture({
			runtime: "container",
			directory,
			checkpointIDs: ["runtime-marker-ready"],
		}, async (task) => {
			expect(task.status?.report?.class).toBe("artifact");
			expect(task.status?.report?.buildPassed).toBe(true);
			expect(task.status?.report?.answerPassed).toBeFalsy();
			expect(task.status?.report?.issues).toContainEqual(expect.objectContaining({ code: "ANSWER_EXIT_NONZERO" }));
			expect(await jobSucceeded(task.status?.buildJobName)).toBe(true);
			expect(await jobSucceeded(task.status?.publisherJobName)).toBe(true);
			expect(await waitForJobSucceeded(task.status?.verifierJobName)).toBe(true);
		});
	} finally {
		await rm(directory, { recursive: true, force: true });
	}
});

runtimeTest("developer-selected challenge completes through the real VerifyTask path", async () => {
	const name = process.env.VERIFY_CHALLENGE_NAME;
	test.skip(!name, "VERIFY_CHALLENGE_NAME is required for the developer acceptance command");
	test.setTimeout(25 * 60_000);
	const task = await verifyFixture(await fixtureForSourceSlug(name!));
	console.log(JSON.stringify({ verify_task: task.metadata?.name, phase: task.status?.phase, report: task.status?.report }, null, 2));
});
