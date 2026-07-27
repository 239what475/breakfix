import { execFile as execFileCallback } from "node:child_process";
import { randomBytes } from "node:crypto";
import { mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { promisify } from "node:util";
import { expect, test } from "@playwright/test";
import { cleanupVerifyTask } from "../support/runtime-cleanup";

const runtimeTest = process.env.RUN_RUNTIME_E2E === "1" ? test : test.skip;
const execFile = promisify(execFileCallback);
const projectRoot = resolve(dirname(fileURLToPath(import.meta.url)), "../..");
const namespace = "breakfix-system";
const stageImage = "172.18.0.1:5000/break-fix/breakfix-base:latest";
const serverDataPVC = "breakfix-server-data";

type VerifyTask = {
	status?: {
		phase?: string;
		report?: {
			summary?: string;
			issues?: Array<{ code?: string; message?: string }>;
		};
	};
};

type Fixture = {
	runtime: "container" | "vcluster";
	directory: string;
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
			return;
		}
		if (task.status?.phase === "Failed") {
			const report = task.status.report;
			const issues = report?.issues?.map((issue) => `${issue.code ?? "UNKNOWN"}: ${issue.message ?? ""}`).join("\n") ?? "";
			throw new Error(`fixed ${environmentKind} artifact failed: ${report?.summary ?? "no report"}${issues ? `\n${issues}` : ""}`);
		}
		await new Promise((resolve) => setTimeout(resolve, 1_000));
	}
	throw new Error(`VerifyTask ${taskID} did not complete before timeout`);
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

async function createVerifyTask(taskID: string, submissionID: string, manifestPath: string) {
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
		await createVerifyTask(taskID, submissionID, join(temp, "verifytask.yaml"));
		await waitForTerminalTask(taskID, environmentKind, environmentName);
	} finally {
		await cleanup(taskID, pod, submissionID);
		await rm(temp, { recursive: true, force: true });
	}
}

runtimeTest.describe.configure({ mode: "serial" });

runtimeTest("fixed container artifact completes a real VerifyTask", async () => {
	test.setTimeout(25 * 60_000);
	await verifyFixture({ runtime: "container", directory: join(projectRoot, "data", "challenges", "cleanup-logs") });
});

runtimeTest("fixed vcluster artifact completes a real VerifyTask", async () => {
	test.setTimeout(25 * 60_000);
	await verifyFixture({ runtime: "vcluster", directory: join(projectRoot, "test", "fixtures", "challenges", "vcluster-web-service") });
});
