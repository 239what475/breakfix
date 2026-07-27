import { execFile as execFileCallback } from "node:child_process";
import { randomBytes } from "node:crypto";
import { mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { promisify } from "node:util";

const execFile = promisify(execFileCallback);
const projectRoot = resolve(dirname(fileURLToPath(import.meta.url)), "../..");
const namespace = "breakfix-system";
const serverDataPVC = "breakfix-server-data";
const stageImage = "172.18.0.1:5000/break-fix/breakfix-base:latest";

type PublishedChallengeCleanup = {
	challengeID: string;
	submissionID: string;
	verifyTaskID: string;
};

type VerifyTaskCleanup = Omit<PublishedChallengeCleanup, "challengeID">;

async function kubectl(args: string[]) {
	return execFile("kubectl", args, { cwd: projectRoot, encoding: "utf8", maxBuffer: 4 * 1024 * 1024 });
}

function requireGeneratedID(value: string, field: string) {
	if (!/^[a-z0-9-]+$/.test(value)) throw new Error(`invalid ${field} for runtime cleanup`);
}

async function resourceExists(kind: string, name: string) {
	try {
		await kubectl(["-n", namespace, "get", kind, name, "-o", "name"]);
		return true;
	} catch {
		return false;
	}
}

async function waitForResourceDeletion(kind: string, name: string) {
	const deadline = Date.now() + 5 * 60_000;
	while (Date.now() < deadline) {
		if (!(await resourceExists(kind, name))) return;
		await new Promise((resolve) => setTimeout(resolve, 1_000));
	}
	throw new Error(`${kind}/${name} was not deleted during runtime cleanup`);
}

async function verifiedImage(taskID: string) {
	const { stdout } = await kubectl(["-n", namespace, "get", "verifytask", taskID, "-o", "jsonpath={.status.tempImage}"]);
	return stdout.trim();
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
    - name: cleanup
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
	await kubectl(["-n", namespace, "wait", "--for=condition=Ready", `pod/${name}`, "--timeout=2m"]);
}

async function removeDataFiles(challengeID?: string, submissionID?: string) {
	const suffix = randomBytes(6).toString("hex");
	const pod = `agent-cleanup-${suffix}`;
	const temp = await mkdtemp(join(tmpdir(), "breakfix-agent-cleanup-"));
	const paths = [
		challengeID ? `/data/challenges/${challengeID}` : "",
		submissionID ? `/data/submissions/${submissionID}` : "",
	].filter(Boolean);
	try {
		await createStagingPod(pod, join(temp, "cleanup-pod.yaml"));
		await kubectl([
			"-n",
			namespace,
			"exec",
			pod,
			"--",
			"rm",
			"-rf",
			...paths,
		]);
	} finally {
		await kubectl(["-n", namespace, "delete", "pod", pod, "--ignore-not-found=true", "--wait=true", "--timeout=2m"]);
		await rm(temp, { recursive: true, force: true });
	}
}

async function deletePublishedImage(reference: string) {
	if (!reference) return;
	const firstSlash = reference.indexOf("/");
	const lastColon = reference.lastIndexOf(":");
	if (firstSlash <= 0 || lastColon <= firstSlash) throw new Error(`invalid published image reference ${reference}`);
	const registry = reference.slice(0, firstSlash);
	const repository = reference.slice(firstSlash + 1, lastColon);
	const tag = reference.slice(lastColon + 1);
	const manifestURL = `http://${registry}/v2/${repository}/manifests/${tag}`;
	const manifest = await fetch(manifestURL, {
		headers: { Accept: "application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json" },
	});
	if (manifest.status === 404) return;
	if (!manifest.ok) throw new Error(`read published image manifest: ${manifest.status} ${await manifest.text()}`);
	const digest = manifest.headers.get("docker-content-digest");
	if (!digest) throw new Error("published image manifest did not provide docker-content-digest");
	const deleted = await fetch(`http://${registry}/v2/${repository}/manifests/${digest}`, { method: "DELETE" });
	if (deleted.status !== 202 && deleted.status !== 404) {
		throw new Error(`delete published image: ${deleted.status} ${await deleted.text()}`);
	}
}

async function removeTaxonomyWorkItem(challengeID: string) {
	await kubectl([
		"-n",
		namespace,
		"exec",
		"breakfix-postgresql-0",
		"--",
		"sh",
		"-c",
		`PGPASSWORD="$POSTGRES_PASSWORD" psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -v ON_ERROR_STOP=1 -c "DELETE FROM taxonomy_work_items WHERE challenge_id = '${challengeID}'"`,
	]);
}

export async function cleanupVerifyTask({ submissionID, verifyTaskID }: VerifyTaskCleanup) {
	requireGeneratedID(submissionID, "submission id");
	requireGeneratedID(verifyTaskID, "verify task id");
	const image = await verifiedImage(verifyTaskID).catch(() => "");
	if (await resourceExists("verifytask", verifyTaskID)) {
		await kubectl(["-n", namespace, "delete", "verifytask", verifyTaskID, "--wait=true", "--timeout=5m"]);
	}
	await waitForResourceDeletion("verifytask", verifyTaskID);
	await removeDataFiles(undefined, submissionID);
	await deletePublishedImage(image);
}

export async function cleanupPublishedChallenge({ challengeID, submissionID, verifyTaskID }: PublishedChallengeCleanup) {
	requireGeneratedID(challengeID, "challenge id");
	await cleanupVerifyTask({ submissionID, verifyTaskID });
	await removeDataFiles(challengeID);
	await removeTaxonomyWorkItem(challengeID);
}

export async function waitForEnvironmentDeletion(kind: "containerenvironment" | "vclusterenvironment", name: string) {
	await waitForResourceDeletion(kind, name);
}
