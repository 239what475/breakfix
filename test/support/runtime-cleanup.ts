import { execFile as execFileCallback } from "node:child_process";
import { promisify } from "node:util";

const execFile = promisify(execFileCallback);
const namespace = process.env.BREAKFIX_RUNTIME_NAMESPACE ?? "breakfix-system";

async function resourceExists(kind: EnvironmentKind, name: string) {
	try {
		await execFile("kubectl", ["-n", namespace, "get", kind, name, "-o", "name"], { encoding: "utf8" });
		return true;
	} catch {
		return false;
	}
}

export type EnvironmentKind = "nodeenvironment" | "vk8senvironment";

export async function waitForEnvironmentDeletion(kind: EnvironmentKind, name: string) {
	const deadline = Date.now() + 5 * 60_000;
	while (Date.now() < deadline) {
		if (!(await resourceExists(kind, name))) return;
		await new Promise((resolve) => setTimeout(resolve, 1_000));
	}
	throw new Error(`${kind}/${name} was not deleted before timeout`);
}
