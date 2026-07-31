import { execFile as execFileCallback, spawn } from "node:child_process";
import { createHash, randomBytes } from "node:crypto";
import { mkdtemp, readFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { promisify } from "node:util";
import { expect, test } from "@playwright/test";
import { parse } from "yaml";

const runtimeTest = process.env.RUN_RUNTIME_E2E === "1" ? test : test.skip;
const execFile = promisify(execFileCallback);
const projectRoot = resolve(dirname(fileURLToPath(import.meta.url)), "../..");
const namespace = process.env.BREAKFIX_RUNTIME_NAMESPACE ?? "breakfix-system";
const postgres = "statefulset/breakfix-postgresql";
const serverDataPVC = "breakfix-server-data";

type Runtime = "node" | "k8s";

type ChallengeManifest = {
	runtime: Runtime;
	title: string;
	difficulty: string;
	description: string;
	nodes?: Array<{ name: string; title: string }>;
	checkpoints: Array<{ id: string; node?: string }>;
};

type CandidateStatus = {
	state: string;
	artifact?: unknown;
	verification?: { passed?: boolean; checkpoints?: Array<{ id?: string; passed?: boolean }> };
	failure?: { class?: string; code?: string; summary?: string };
};

type WorkItemStatus = {
	kind: string;
	state: string;
	attempt: number;
	error_code: string;
	error_summary: string;
};

type Seed = {
	candidateID: string;
	userID: string;
	authoringSessionID: string;
	generatorSessionID: string;
	generatorRunID: string;
	buildWorkID: string;
	cleanupWorkID: string;
	archivePath: string;
	dataPod: string;
};

function name(prefix: string) {
	return `${prefix}-${randomBytes(8).toString("hex")}`;
}

async function kubectl(args: string[]) {
	return execFile("kubectl", args, {
		cwd: projectRoot,
		encoding: "utf8",
		maxBuffer: 8 * 1024 * 1024,
	});
}

async function kubectlInput(args: string[], input: string | Buffer) {
	return new Promise<{ stdout: string; stderr: string }>((resolvePromise, reject) => {
		const child = spawn("kubectl", args, { cwd: projectRoot, stdio: ["pipe", "pipe", "pipe"] });
		const stdout: Buffer[] = [];
		const stderr: Buffer[] = [];
		child.stdout.on("data", (chunk: Buffer) => stdout.push(chunk));
		child.stderr.on("data", (chunk: Buffer) => stderr.push(chunk));
		child.once("error", reject);
		child.once("close", (code) => {
			const output = Buffer.concat(stdout).toString("utf8");
			const error = Buffer.concat(stderr).toString("utf8");
			if (code === 0) {
				resolvePromise({ stdout: output, stderr: error });
				return;
			}
			reject(new Error(`kubectl ${args.join(" ")} exited with ${code}: ${error || output}`));
		});
		child.stdin.end(input);
	});
}

async function psql(statement: string) {
	const { stdout } = await kubectl([
		"-n",
		namespace,
		"exec",
		postgres,
		"--",
		"psql",
		"-X",
		"-v",
		"ON_ERROR_STOP=1",
		"-U",
		"breakfix",
		"-d",
		"breakfix",
		"-t",
		"-A",
		"-c",
		statement,
	]);
	return stdout.trim();
}

function quote(value: string) {
	return `'${value.replaceAll("'", "''")}'`;
}

function json(value: unknown) {
	return `${quote(JSON.stringify(value))}::jsonb`;
}

function digest(value: Buffer) {
	return `sha256:${createHash("sha256").update(value).digest("hex")}`;
}

function requiredString(value: unknown, label: string) {
	if (typeof value !== "string" || !value.trim()) throw new Error(`${label} is required in the deployed runtime configuration`);
	return value.trim();
}

function requiredNumber(value: unknown, label: string) {
	if (typeof value !== "number" || !Number.isFinite(value)) throw new Error(`${label} is required in the deployed runtime configuration`);
	return value;
}

async function deployedConfiguration() {
	const { stdout: deploymentRaw } = await kubectl(["-n", namespace, "get", "deployment", "breakfix-server", "-o", "json"]);
	const deployment = JSON.parse(deploymentRaw) as {
		spec?: { template?: { spec?: { volumes?: Array<{ name?: string; configMap?: { name?: string } }> } } };
	};
	const configMapName = deployment.spec?.template?.spec?.volumes?.find((volume) => volume.name === "config")?.configMap?.name;
	if (!configMapName) throw new Error("breakfix-server does not mount a runtime configuration ConfigMap");
	const { stdout } = await kubectl(["-n", namespace, "get", "configmap", configMapName, "-o", "json"]);
	const configMap = JSON.parse(stdout) as { data?: { [key: string]: string | undefined } };
	const source = configMap.data?.["config.yaml"];
	if (!source) throw new Error("breakfix-config does not contain config.yaml");
	return parse(source) as Record<string, unknown>;
}

async function secretValue(key: string) {
	const { stdout } = await kubectl(["-n", namespace, "get", "secret", "breakfix-runtime", "-o", "json"]);
	const secret = JSON.parse(stdout) as { data?: Record<string, string | undefined> };
	const encoded = secret.data?.[key];
	if (!encoded) throw new Error(`breakfix-runtime does not contain ${key}`);
	return Buffer.from(encoded, "base64").toString("utf8").trim();
}

async function createDataPod(pod: string) {
	const manifest = `apiVersion: v1
kind: Pod
metadata:
  name: ${pod}
  namespace: ${namespace}
  labels:
    app.kubernetes.io/name: breakfix-runtime-candidate-data
spec:
  restartPolicy: Never
  securityContext:
    runAsNonRoot: true
    runAsUser: 65532
    runAsGroup: 65532
    fsGroup: 65532
  containers:
    - name: data
      image: curlimages/curl:8.12.1
      imagePullPolicy: IfNotPresent
      command: ["/bin/sh", "-ec", "sleep 3600"]
      securityContext:
        allowPrivilegeEscalation: false
        capabilities:
          drop: ["ALL"]
      volumeMounts:
        - name: data
          mountPath: /var/lib/breakfix
  volumes:
    - name: data
      persistentVolumeClaim:
        claimName: ${serverDataPVC}
`;
	await kubectlInput(["apply", "-f", "-"], manifest);
	await kubectl(["-n", namespace, "wait", "--for=condition=Ready", `pod/${pod}`, "--timeout=2m"]);
}

async function removeDataPod(pod: string) {
	await kubectl(["-n", namespace, "delete", "pod", pod, "--ignore-not-found=true", "--wait=true", "--timeout=2m"])
		.catch(() => undefined);
}

async function writeArchive(pod: string, path: string, archive: Buffer) {
	await kubectlInput(
		[
			"-n",
			namespace,
			"exec",
			"-i",
			pod,
			"--",
			"/bin/sh",
			"-ec",
			`umask 007; mkdir -p ${quote(dirname(path))}; cat >${quote(path)}; chmod 0440 ${quote(path)}`,
		],
		archive,
	);
}

async function removeArchive(pod: string, candidateID: string) {
	await kubectl([
		"-n",
		namespace,
		"exec",
		pod,
		"--",
		"/bin/sh",
		"-ec",
		`rm -rf ${quote(`/var/lib/breakfix/candidates/${candidateID}`)}`,
	]).catch(() => undefined);
}

async function createArchive(directory: string) {
	const root = await mkdtemp(join(tmpdir(), "breakfix-runtime-candidate-"));
	const path = join(root, "candidate.tar.gz");
	try {
		await execFile("tar", ["-czf", path, "-C", directory, "."], { cwd: projectRoot });
		return { archive: await readFile(path), root };
	} catch (error) {
		await rm(root, { recursive: true, force: true });
		throw error;
	}
}

function snapshotFor(manifest: ChallengeManifest, config: Record<string, unknown>, nodeFingerprint: string, k8sBaseDigest: string) {
	const runtime = config.runtime as Record<string, unknown> | undefined;
	const incus = config.incus as Record<string, unknown> | undefined;
	if (!runtime || !incus) throw new Error("deployed runtime configuration is incomplete");
	const checkpoints = manifest.checkpoints.map((checkpoint) => ({ id: checkpoint.id, node: checkpoint.node ?? "" }));
	if (manifest.runtime === "node") {
		const node = runtime.node as Record<string, unknown> | undefined;
		if (!node || !manifest.nodes?.length) throw new Error("node fixture or runtime configuration is incomplete");
		return {
			runtime: "node",
			checkpoints,
			node: {
				base_image_fingerprint: nodeFingerprint,
				profile_revision: requiredString(node.profile_revision, "runtime.node.profile_revision"),
				network_policy_revision: requiredString(node.network_policy_revision, "runtime.node.network_policy_revision"),
				nodes: manifest.nodes,
				resources: {
					cpu: requiredString(incus.node_cpu, "incus.node_cpu"),
					memory: requiredString(incus.node_memory, "incus.node_memory"),
					processes: requiredNumber(incus.node_processes, "incus.node_processes"),
					root_disk: requiredString(incus.node_root_disk, "incus.node_root_disk"),
				},
			},
		};
	}
	const k8s = runtime.k8s as Record<string, unknown> | undefined;
	const resources = k8s?.resources as Record<string, unknown> | undefined;
	if (!k8s || !resources) throw new Error("runtime.k8s configuration is incomplete");
	return {
		runtime: "k8s",
		checkpoints,
		k8s: {
			base_image_digest: k8sBaseDigest,
			profile_revision: requiredString(k8s.profile_revision, "runtime.k8s.profile_revision"),
			version: requiredString(k8s.version, "runtime.k8s.version"),
			management_terminal_image: k8sBaseDigest,
			resources: {
				control_plane_cpu: requiredString(resources.control_plane_cpu, "runtime.k8s.resources.control_plane_cpu"),
				control_plane_memory: requiredString(resources.control_plane_memory, "runtime.k8s.resources.control_plane_memory"),
				control_plane_ephemeral_storage: requiredString(resources.control_plane_ephemeral_storage, "runtime.k8s.resources.control_plane_ephemeral_storage"),
				workload_cpu: requiredString(resources.workload_cpu, "runtime.k8s.resources.workload_cpu"),
				workload_memory: requiredString(resources.workload_memory, "runtime.k8s.resources.workload_memory"),
				workload_ephemeral_storage: requiredString(resources.workload_ephemeral_storage, "runtime.k8s.resources.workload_ephemeral_storage"),
				quota_cpu: requiredString(resources.quota_cpu, "runtime.k8s.resources.quota_cpu"),
				quota_memory: requiredString(resources.quota_memory, "runtime.k8s.resources.quota_memory"),
				quota_ephemeral_storage: requiredString(resources.quota_ephemeral_storage, "runtime.k8s.resources.quota_ephemeral_storage"),
			},
		},
	};
}

async function seedCandidate(runtime: Runtime, fixtureDirectory: string): Promise<Seed> {
	const suffix = randomBytes(8).toString("hex");
	const candidateID = `candidate-runtime-${suffix}`;
	const seed: Seed = {
		candidateID,
		userID: `runtime-user-${suffix}`,
		authoringSessionID: `runtime-authoring-${suffix}`,
		generatorSessionID: `runtime-generator-session-${suffix}`,
		generatorRunID: `runtime-generator-run-${suffix}`,
		buildWorkID: `work-runtime-build-${suffix}`,
		cleanupWorkID: `work-runtime-cleanup-${suffix}`,
		archivePath: `/var/lib/breakfix/candidates/${candidateID}/candidate.tar.gz`,
		dataPod: `runtime-candidate-data-${suffix}`,
	};
	const manifest = parse(await readFile(join(fixtureDirectory, "challenge.yaml"), "utf8")) as ChallengeManifest;
	if (manifest.runtime !== runtime) throw new Error(`fixture runtime ${manifest.runtime} does not match ${runtime}`);
	const { archive, root } = await createArchive(fixtureDirectory);
	try {
		await createDataPod(seed.dataPod);
		await writeArchive(seed.dataPod, seed.archivePath, archive);
		const [config, nodeFingerprint, k8sBaseDigest] = await Promise.all([
			deployedConfiguration(),
			secretValue("incus_base_image_fingerprint"),
			secretValue("k8s_base_image_digest"),
		]);
		const snapshot = snapshotFor(manifest, config, nodeFingerprint, k8sBaseDigest);
		const archiveDigest = digest(archive);
		const authoringPlan = {
			metadata: { title: manifest.title, difficulty: manifest.difficulty, description: manifest.description, runtime },
			overview: "Runtime workflow fixture.",
			checkpoints: manifest.checkpoints.map((checkpoint, index) => ({
				id: checkpoint.id,
				title: checkpoint.id,
				markdown: "Runtime workflow fixture checkpoint.",
				position: index + 1,
			})),
		};
		const now = new Date().toISOString();
		const sql = `
BEGIN;
INSERT INTO users (id, subject, name) VALUES (${quote(seed.userID)}, ${quote(seed.userID)}, ${quote("Runtime workflow fixture")});
INSERT INTO agent_sessions (id, purpose, owner_kind, owner_ref, user_ref, status, created_at, updated_at)
  VALUES (${quote(seed.generatorSessionID)}, 'generator', 'authoring-generator', ${quote(`${seed.authoringSessionID}:1`)}, ${quote(seed.userID)}, 'active', now(), now());
INSERT INTO authoring_sessions (id, user_id, runtime_session_id, generator_session_id, generator_run_id, candidate_revision_id, state, current_revision, visible_revision, publish_challenge_id, last_error, created_at, updated_at)
  VALUES (${quote(seed.authoringSessionID)}, ${quote(seed.userID)}, ${quote(`runtime-authoring-session-${suffix}`)}, ${quote(seed.generatorSessionID)}, ${quote(seed.generatorRunID)}, ${quote(seed.candidateID)}, 'GeneratingAndVerifying', 1, 0, '', '', ${quote(now)}, ${quote(now)});
INSERT INTO authoring_revisions (session_id, revision, plan_json, candidate_revision_id, created_at)
  VALUES (${quote(seed.authoringSessionID)}, 0, ${json(authoringPlan)}, '', ${quote(now)}),
         (${quote(seed.authoringSessionID)}, 1, ${json(authoringPlan)}, '', ${quote(now)});
INSERT INTO agent_runs (id, session_id, purpose, owner_kind, owner_ref, input_revision, input_json, status, model, prompt_version, deadline_at, last_error, created_at, updated_at, completed_at)
  VALUES (${quote(seed.generatorRunID)}, ${quote(seed.generatorSessionID)}, 'generator', 'authoring-session', ${quote(seed.authoringSessionID)}, '1', '{}'::jsonb, 'succeeded', 'runtime-fixture', 'runtime-fixture-v1', now() + interval '1 hour', '', now(), now(), now());
INSERT INTO generator_runs (run_id, generator_session_id, authoring_session_id, authoring_revision, seed_candidate_revision_id, candidate_revision_id, created_at, updated_at)
  VALUES (${quote(seed.generatorRunID)}, ${quote(seed.generatorSessionID)}, ${quote(seed.authoringSessionID)}, 1, '', ${quote(seed.candidateID)}, now(), now());
INSERT INTO candidate_revisions (id, authoring_session_id, authoring_revision, generator_session_id, generator_run_id, judge_run_id, archive_path, archive_sha256, execution_snapshot, state, created_at, updated_at)
  VALUES (${quote(seed.candidateID)}, ${quote(seed.authoringSessionID)}, 1, ${quote(seed.generatorSessionID)}, ${quote(seed.generatorRunID)}, ${quote(seed.generatorRunID)}, ${quote(seed.archivePath)}, ${quote(archiveDigest)}, ${json(snapshot)}, 'Building', now(), now());
INSERT INTO work_items (id, kind, subject_type, subject_id, state, next_run_at, execution_timeout_millis, deadline_at, created_at, updated_at)
  VALUES (${quote(seed.buildWorkID)}, 'build', 'candidate_revision', ${quote(seed.candidateID)}, 'pending', now(), 3600000, NULL, now(), now());
COMMIT;`;
		await psql(sql);
		return seed;
	} catch (error) {
		await removeArchive(seed.dataPod, seed.candidateID);
		await removeDataPod(seed.dataPod);
		throw error;
	} finally {
		await rm(root, { recursive: true, force: true });
	}
}

async function candidateStatus(candidateID: string): Promise<CandidateStatus | undefined> {
	const raw = await psql(`SELECT json_build_object('state', state, 'artifact', artifact_reference, 'verification', verification_report, 'failure', failure)::text FROM candidate_revisions WHERE id = ${quote(candidateID)};`);
	return raw ? JSON.parse(raw) as CandidateStatus : undefined;
}

async function workItemStatuses(candidateID: string): Promise<WorkItemStatus[]> {
	const raw = await psql(`SELECT COALESCE(json_agg(json_build_object('kind', kind, 'state', state, 'attempt', attempt, 'error_code', error_code, 'error_summary', error_summary) ORDER BY created_at, id), '[]'::json)::text FROM work_items WHERE subject_type = 'candidate_revision' AND subject_id = ${quote(candidateID)};`);
	return JSON.parse(raw || "[]") as WorkItemStatus[];
}

async function diagnostics(candidateID: string) {
	const values = await Promise.all([
		candidateStatus(candidateID).catch((error) => ({ error: String(error) })),
		workItemStatuses(candidateID).catch((error) => ({ error: String(error) })),
		kubectl(["-n", namespace, "get", "nodeenvironment,vk8senvironment", "-o", "json"]).then(({ stdout }) => stdout).catch((error) => String(error)),
		kubectl(["-n", namespace, "logs", "deploy/breakfix-builder", "--tail=100"]).then(({ stdout }) => stdout).catch((error) => String(error)),
		kubectl(["-n", namespace, "logs", "deploy/breakfix-publisher", "--tail=100"]).then(({ stdout }) => stdout).catch((error) => String(error)),
		kubectl(["-n", namespace, "logs", "deploy/breakfix-verifier", "--tail=100"]).then(({ stdout }) => stdout).catch((error) => String(error)),
	]);
	return JSON.stringify({ candidate: values[0], work_items: values[1], environments: values[2], builder_logs: values[3], publisher_logs: values[4], verifier_logs: values[5] }, null, 2);
}

async function waitForVerified(seed: Seed, timeout: number) {
	const deadline = Date.now() + timeout;
	while (Date.now() < deadline) {
		const status = await candidateStatus(seed.candidateID);
		if (!status) throw new Error(`candidate ${seed.candidateID} disappeared`);
		if (status.state === "Verified") {
			expect(status.artifact).toBeTruthy();
			expect(status.verification?.passed).toBe(true);
			for (const checkpoint of status.verification?.checkpoints ?? []) expect(checkpoint.passed).toBe(true);
			const stages = await workItemStatuses(seed.candidateID);
			for (const kind of ["build", "artifact_publish", "verify"]) {
				expect(stages).toContainEqual(expect.objectContaining({ kind, state: "succeeded", attempt: 1 }));
			}
			return;
		}
		if (["ArtifactFailed", "InfrastructureFailed", "Cancelled", "Superseded", "Published"].includes(status.state)) {
			throw new Error(`candidate ${seed.candidateID} reached ${status.state}: ${JSON.stringify(status.failure ?? {})}`);
		}
		await new Promise((resolvePromise) => setTimeout(resolvePromise, 1_000));
	}
	throw new Error(`candidate ${seed.candidateID} did not reach Verified before timeout`);
}

async function candidateEnvironmentNames(candidateID: string) {
	const { stdout } = await kubectl(["-n", namespace, "get", "nodeenvironment,vk8senvironment", "-o", "json"]);
	const list = JSON.parse(stdout) as { items?: Array<{ kind?: string; metadata?: { name?: string; annotations?: Record<string, string> } }> };
	return (list.items ?? []).flatMap((item) => {
		if (item.metadata?.annotations?.["breakfix.dev/candidate-revision"] !== candidateID || !item.metadata.name || !item.kind) return [];
		return [{ kind: item.kind.toLowerCase(), name: item.metadata.name }];
	});
}

async function waitForVK8sRegistryPullBinding(seed: Seed, timeout: number) {
	const deadline = Date.now() + timeout;
	const pullSecret = requiredString(await secretValue("registry_pull_secret"), "registry_pull_secret");
	const { stdout: sourceSecretRaw } = await kubectl(["-n", namespace, "get", "secret", pullSecret, "-o", "json"]);
	const sourceSecret = JSON.parse(sourceSecretRaw) as {
		type?: string;
		data?: Record<string, string | undefined>;
	};
	const sourceDockerConfig = sourceSecret.data?.[".dockerconfigjson"];
	if (sourceSecret.type !== "kubernetes.io/dockerconfigjson" || !sourceDockerConfig) {
		throw new Error(`control-plane pull Secret ${pullSecret} is not a Docker config Secret`);
	}
	let lastTransientError = "";
	while (Date.now() < deadline) {
		const environments = await candidateEnvironmentNames(seed.candidateID);
		const environment = environments.find((item) => item.kind === "vk8senvironment");
		if (environment) {
			const { stdout } = await kubectl(["-n", namespace, "get", "vk8senvironment", environment.name, "-o", "json"]);
			const resource = JSON.parse(stdout) as {
				status?: { runtime?: { namespace?: string; terminalPodName?: string } };
			};
			const runtimeNamespace = resource.status?.runtime?.namespace;
			const terminalPodName = resource.status?.runtime?.terminalPodName;
			if (runtimeNamespace && terminalPodName) {
				let serviceAccountResult: { stdout: string };
				let copiedSecretResult: { stdout: string };
				let podResult: { stdout: string };
				try {
					[serviceAccountResult, copiedSecretResult, podResult] = await Promise.all([
						kubectl(["-n", runtimeNamespace, "get", "serviceaccount", "breakfix-runtime", "-o", "json"]),
						kubectl(["-n", runtimeNamespace, "get", "secret", pullSecret, "-o", "json"]),
						kubectl(["-n", runtimeNamespace, "get", "pod", terminalPodName, "-o", "json"]),
					]);
				} catch (error) {
					lastTransientError = String(error);
					await new Promise((resolvePromise) => setTimeout(resolvePromise, 1_000));
					continue;
				}
				const serviceAccount = JSON.parse(serviceAccountResult.stdout) as {
					automountServiceAccountToken?: boolean;
					imagePullSecrets?: Array<{ name?: string }>;
				};
				const copiedSecret = JSON.parse(copiedSecretResult.stdout) as {
					type?: string;
					data?: Record<string, string | undefined>;
				};
				const pod = JSON.parse(podResult.stdout) as {
					spec?: {
						serviceAccountName?: string;
						automountServiceAccountToken?: boolean;
						imagePullSecrets?: Array<{ name?: string }>;
						volumes?: Array<{ secret?: { secretName?: string } }>;
					};
				};
				expect(copiedSecret.type).toBe("kubernetes.io/dockerconfigjson");
				expect(copiedSecret.data?.[".dockerconfigjson"]).toBe(sourceDockerConfig);
				expect(serviceAccount.automountServiceAccountToken).toBe(false);
				expect(serviceAccount.imagePullSecrets).toEqual([{ name: pullSecret }]);
				expect(pod.spec?.serviceAccountName).toBe("breakfix-runtime");
				expect(pod.spec?.automountServiceAccountToken).toBe(false);
				// The ServiceAccount admission plugin copies this reference into the
				// stored Pod. It remains a kubelet-only pull credential, not a volume.
				expect(pod.spec?.imagePullSecrets).toEqual(serviceAccount.imagePullSecrets);
				expect(pod.spec?.volumes?.some((volume) => volume.secret?.secretName === pullSecret)).toBe(false);
				return;
			}
		}
		await new Promise((resolvePromise) => setTimeout(resolvePromise, 1_000));
	}
	throw new Error(`VK8s environment for ${seed.candidateID} never exposed its Registry pull binding${lastTransientError ? `: ${lastTransientError}` : ""}`);
}

async function cancelAndEnqueueCleanup(seed: Seed) {
	const failure = { class: "cancelled", code: "RUNTIME_TEST_CLEANUP", summary: "runtime workflow test cleanup" };
	await psql(`
BEGIN;
UPDATE work_items SET state = 'cancelled', lease_owner = '', lease_expires_at = NULL, error_code = 'runtime_test_cleanup', error_summary = 'runtime workflow test cleanup', updated_at = now()
  WHERE subject_type = 'candidate_revision' AND subject_id = ${quote(seed.candidateID)} AND kind <> 'artifact_cleanup' AND state IN ('pending', 'running');
UPDATE candidate_revisions SET state = 'Cancelled', failure = ${json(failure)}, updated_at = now() WHERE id = ${quote(seed.candidateID)};
INSERT INTO work_items (id, kind, subject_type, subject_id, state, next_run_at, execution_timeout_millis, deadline_at, created_at, updated_at)
  VALUES (${quote(seed.cleanupWorkID)}, 'artifact_cleanup', 'candidate_revision', ${quote(seed.candidateID)}, 'pending', now(), 0, NULL, now(), now())
  ON CONFLICT (kind, subject_type, subject_id) DO NOTHING;
COMMIT;`);
	for (const environment of await candidateEnvironmentNames(seed.candidateID)) {
		await kubectl(["-n", namespace, "delete", environment.kind, environment.name, "--ignore-not-found=true", "--wait=false"])
			.catch(() => undefined);
	}
}

async function waitForCleanup(seed: Seed) {
	const deadline = Date.now() + 10 * 60_000;
	while (Date.now() < deadline) {
		const work = (await workItemStatuses(seed.candidateID)).find((item) => item.kind === "artifact_cleanup");
		if (work?.state === "succeeded") return;
		if (work?.state === "failed" || work?.state === "cancelled") {
			throw new Error(`candidate cleanup ended in ${work.state}: ${work.error_code} ${work.error_summary}`);
		}
		await new Promise((resolvePromise) => setTimeout(resolvePromise, 1_000));
	}
	throw new Error(`candidate cleanup for ${seed.candidateID} did not finish before timeout`);
}

async function deleteSeedRecords(seed: Seed) {
	await psql(`
BEGIN;
DELETE FROM work_items WHERE (subject_type = 'candidate_revision' AND subject_id = ${quote(seed.candidateID)}) OR (subject_type = 'agent_run' AND subject_id = ${quote(seed.generatorRunID)});
DELETE FROM candidate_revisions WHERE id = ${quote(seed.candidateID)};
DELETE FROM generator_runs WHERE run_id = ${quote(seed.generatorRunID)};
DELETE FROM agent_runs WHERE id = ${quote(seed.generatorRunID)};
DELETE FROM authoring_revisions WHERE session_id = ${quote(seed.authoringSessionID)};
DELETE FROM authoring_sessions WHERE id = ${quote(seed.authoringSessionID)};
DELETE FROM agent_sessions WHERE id = ${quote(seed.generatorSessionID)};
DELETE FROM users WHERE id = ${quote(seed.userID)};
COMMIT;`);
}

async function cleanupSeed(seed: Seed) {
	try {
		await cancelAndEnqueueCleanup(seed);
		await waitForCleanup(seed);
	} finally {
		await removeArchive(seed.dataPod, seed.candidateID);
		await deleteSeedRecords(seed).catch(() => undefined);
		await removeDataPod(seed.dataPod);
	}
}

async function verifyFixture(runtime: Runtime, fixture: string, timeout: number) {
	let seed: Seed | undefined;
	try {
		seed = await seedCandidate(runtime, join(projectRoot, "test", "fixtures", "challenges", fixture));
		if (runtime === "k8s") {
			await Promise.all([waitForVerified(seed, timeout), waitForVK8sRegistryPullBinding(seed, timeout)]);
		} else {
			await waitForVerified(seed, timeout);
		}
	} catch (error) {
		const detail = seed ? await diagnostics(seed.candidateID) : "candidate was not seeded";
		throw new Error(`${String(error)}\n\nRuntime workflow diagnostics:\n${detail}`);
	} finally {
		if (seed) await cleanupSeed(seed);
	}
}

runtimeTest.describe.configure({ mode: "serial" });

runtimeTest("fixed Node candidate completes real Build, ArtifactPublish, and Verify", async () => {
	test.setTimeout(30 * 60_000);
	await verifyFixture("node", "node-reverse-proxy", 25 * 60_000);
});

runtimeTest("fixed k8s candidate completes real Build, ArtifactPublish, and Verify", async () => {
	test.setTimeout(30 * 60_000);
	await verifyFixture("k8s", "k8s-web-service", 25 * 60_000);
});
