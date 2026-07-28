import { execFile as execFileCallback } from "node:child_process";
import { mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { promisify } from "node:util";
import { expect, test } from "@playwright/test";

const runtimeTest = process.env.RUN_NETWORK_POLICY_E2E === "1" ? test : test.skip;
const execFile = promisify(execFileCallback);
const projectRoot = resolve(dirname(fileURLToPath(import.meta.url)), "../..");
const namespace = "breakfix-builder-boundary-e2e";
const kubectlContext = process.env.BREAKFIX_RUNTIME_KUBECTL_CONTEXT?.trim();

async function kubectl(args: string[]) {
	const contextArgs = kubectlContext ? ["--context", kubectlContext] : [];
	return execFile("kubectl", [...contextArgs, ...args], { cwd: projectRoot, encoding: "utf8", maxBuffer: 4 * 1024 * 1024 });
}

async function reaches(host: string, port: number) {
	try {
		await kubectl(["-n", namespace, "exec", "builder", "--", "nc", "-z", "-w", "5", host, String(port)]);
		return true;
	} catch {
		return false;
	}
}

runtimeTest("Builder egress boundary allows only Server and DNS", async () => {
	test.setTimeout(10 * 60_000);
	const temp = await mkdtemp(join(tmpdir(), "breakfix-builder-boundary-"));
	const manifestPath = join(temp, "boundary.yaml");
	try {
		await kubectl(["delete", "namespace", namespace, "--ignore-not-found=true", "--wait=true", "--timeout=2m"]);
		await kubectl(["create", "namespace", namespace]);
		const manifest = `apiVersion: v1
kind: Pod
metadata:
  name: server
  namespace: ${namespace}
  labels:
    app.kubernetes.io/name: breakfix-server
spec:
  automountServiceAccountToken: false
  containers:
    - name: server
      image: breakfix-builder:latest
      imagePullPolicy: IfNotPresent
      command: ["sh", "-c", "nc -lk -p 9090"]
---
apiVersion: v1
kind: Service
metadata:
  name: breakfix-server
  namespace: ${namespace}
spec:
  selector:
    app.kubernetes.io/name: breakfix-server
  ports:
    - port: 9090
      targetPort: 9090
---
apiVersion: v1
kind: Pod
metadata:
  name: registry
  namespace: ${namespace}
  labels:
    app.kubernetes.io/name: breakfix-registry
spec:
  automountServiceAccountToken: false
  containers:
    - name: registry
      image: breakfix-builder:latest
      imagePullPolicy: IfNotPresent
      command: ["sh", "-c", "nc -lk -p 443"]
---
apiVersion: v1
kind: Service
metadata:
  name: breakfix-registry
  namespace: ${namespace}
spec:
  selector:
    app.kubernetes.io/name: breakfix-registry
  ports:
    - port: 443
      targetPort: 443
---
apiVersion: v1
kind: Pod
metadata:
  name: builder
  namespace: ${namespace}
  labels:
    breakfix.dev/role: build
spec:
  automountServiceAccountToken: false
  restartPolicy: Never
  securityContext:
    runAsNonRoot: true
    runAsUser: 1000
    runAsGroup: 1000
  containers:
    - name: builder
      image: breakfix-builder:latest
      imagePullPolicy: IfNotPresent
      command: ["sh", "-c", "sleep 300"]
      securityContext:
        privileged: false
        runAsNonRoot: true
        runAsUser: 1000
        runAsGroup: 1000
        seccompProfile:
          type: Unconfined
        appArmorProfile:
          type: Unconfined
`;
		await writeFile(manifestPath, manifest, "utf8");
		await kubectl(["apply", "-f", manifestPath]);
		await kubectl(["-n", namespace, "apply", "-f", join(projectRoot, "deploy", "runtime", "build-network-policy.yaml")]);
		for (const name of ["server", "registry", "builder"]) {
			await kubectl(["-n", namespace, "wait", `--for=condition=Ready`, `pod/${name}`, "--timeout=2m"]);
		}

		const { stdout } = await kubectl(["-n", namespace, "get", "pod", "builder", "-o", "json"]);
		const builder = JSON.parse(stdout) as {
			spec: {
				automountServiceAccountToken?: boolean;
				imagePullSecrets?: unknown[];
				volumes?: unknown[];
				containers: Array<{
					env?: Array<{ name?: string }>;
					envFrom?: unknown[];
					securityContext?: { privileged?: boolean; runAsUser?: number; runAsNonRoot?: boolean };
				}>;
			};
		};
		const container = builder.spec.containers[0];
		expect(builder.spec.automountServiceAccountToken).toBe(false);
		expect(builder.spec.imagePullSecrets ?? []).toHaveLength(0);
		expect(builder.spec.volumes ?? []).toHaveLength(0);
		expect(container.envFrom ?? []).toHaveLength(0);
		expect(container.env ?? []).not.toContainEqual(expect.objectContaining({ name: expect.stringMatching(/REGISTRY|INTERNAL_API_KEY/) }));
		expect(container.securityContext?.privileged).toBe(false);
		expect(container.securityContext?.runAsNonRoot).toBe(true);
		expect(container.securityContext?.runAsUser).toBe(1000);
		await kubectl(["-n", namespace, "exec", "builder", "--", "sh", "-ec", "test ! -e /var/run/secrets/kubernetes.io/serviceaccount/token"]);

		expect(await reaches("breakfix-server", 9090)).toBe(true);
		expect(await reaches("breakfix-registry", 443)).toBe(false);
		expect(await reaches("kubernetes.default", 443)).toBe(false);
	} finally {
		await kubectl(["delete", "namespace", namespace, "--ignore-not-found=true", "--wait=true", "--timeout=3m"]);
		await rm(temp, { recursive: true, force: true });
	}
});
