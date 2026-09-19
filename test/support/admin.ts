import { createHmac } from "node:crypto";
import { execFile as execFileCallback } from "node:child_process";
import { existsSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { promisify } from "node:util";
import { expect, type APIRequestContext, type Page } from "@playwright/test";

export const execFile = promisify(execFileCallback);

export const apiBase = process.env.BREAKFIX_E2E_BASE_URL;
export const adminPassword = "admin-e2e-password";

// Every chain scenario owns a distinct (page, anchor) pair so the specs stay
// independent on a fresh target and remain grep-able as subsets:
//   rescue            -> pod-lifecycle @ pod-lifetime
//   batch rollout     -> autoscale @ how-does-a-horizontalpodautoscaler-work (pre-published)
//                        ingress     @ terminology (scheduler chain)
//   watchdog+controls -> ingress    @ what-is-ingress (watchdog failure)
//                        autoscale @ algorithm-details (parked companion)
export const podLifecyclePage = "docs/concepts/workloads/pods/pod-lifecycle";
export const autoscalePage = "docs/concepts/workloads/autoscaling/horizontal-pod-autoscale";
export const ingressPage = "docs/concepts/services-networking/ingress";
export const podLifetimeAnchor = "pod-lifetime";
export const autoscaleAnchor = "how-does-a-horizontalpodautoscaler-work";
export const ingressTerminologyAnchor = "terminology";
export const ingressWhatIsAnchor = "what-is-ingress";
export const ingressPrerequisitesAnchor = "prerequisites";

export function decodeBase32(value: string) {
  const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567";
  const normalized = value.replace(/=+$/, "").toUpperCase();
  let bits = 0;
  let buffer = 0;
  const bytes: number[] = [];
  for (const character of normalized) {
    const index = alphabet.indexOf(character);
    if (index < 0) throw new Error(`invalid TOTP secret character: ${character}`);
    buffer = (buffer << 5) | index;
    bits += 5;
    if (bits >= 8) {
      bits -= 8;
      bytes.push((buffer >> bits) & 0xff);
    }
  }
  return Buffer.from(bytes);
}

export function totp(secret: string, now = Date.now()) {
  const counter = Math.floor(now / 30_000);
  const input = Buffer.alloc(8);
  input.writeBigInt64BE(BigInt(counter));
  const digest = createHmac("sha1", decodeBase32(secret)).update(input).digest();
  const offset = digest[digest.length - 1] & 0x0f;
  const value = ((digest[offset] & 0x7f) << 24) |
    ((digest[offset + 1] & 0xff) << 16) |
    ((digest[offset + 2] & 0xff) << 8) |
    (digest[offset + 3] & 0xff);
  return String(value % 1_000_000).padStart(6, "0");
}

export async function postgres(sql: string) {
  const { stdout } = await execFile("kubectl", [
    "-n", "breakfix-system", "exec", "breakfix-postgresql-0", "--", "sh", "-ec",
    `psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -Atc ${JSON.stringify(sql)}`,
  ]);
  return stdout.trim();
}

export async function scaleRuntimeWorker(replicas: number) {
  await execFile("kubectl", ["-n", "breakfix-system", "scale", "deployment/breakfix-runtime-worker", `--replicas=${String(replicas)}`]);
  if (replicas === 0) {
    // The terminating Pod still holds its claim loop for its grace period;
    // wait until it is really gone so the parked action is never touched.
    await expect.poll(async () => {
      const { stdout } = await execFile("kubectl", ["-n", "breakfix-system", "get", "pods", "-l", "app.kubernetes.io/name=breakfix-runtime-worker", "--no-headers"]);
      return stdout.trim().split("\n").filter((line) => line.trim()).length;
    }, { timeout: 120_000, intervals: [1_000, 2_000] }).toBe(0);
  } else {
    await execFile("kubectl", ["-n", "breakfix-system", "rollout", "status", "deployment/breakfix-runtime-worker", "--timeout=120s"]);
  }
}

export async function runtimeEnvironmentExists(uid: string) {
  const { stdout } = await execFile("kubectl", ["-n", "breakfix-system", "get", "runtimeenvironments.breakfix.dev", "-o", "json"]);
  const resources = JSON.parse(stdout) as { items?: Array<{ metadata?: { uid?: string } }> };
  return resources.items?.some((item) => item.metadata?.uid === uid) ?? false;
}

export async function registerAndLogin(request: APIRequestContext, username: string) {
  const registerResponse = await request.post(`${apiBase}/api/auth/register`, { data: { username, password: adminPassword } });
  expect(registerResponse.status(), await registerResponse.text()).toBe(201);
  const registration = await registerResponse.json() as { totp_secret: string };
  const loginResponse = await request.post(`${apiBase}/api/auth/login`, {
    data: { username, password: adminPassword, totp_code: totp(registration.totp_secret) },
  });
  expect(loginResponse.status(), await loginResponse.text()).toBe(200);
  const credentials = await loginResponse.json() as { token: string };
  const payload = JSON.parse(Buffer.from(credentials.token.split(".")[1].replace(/-/g, "+").replace(/_/g, "/"), "base64").toString()) as { role?: string };
  return { username, token: credentials.token, totpSecret: registration.totp_secret, role: payload.role ?? "" };
}

// The bootstrap-admin credentials are elected by the admin-setup project and
// survive worker restarts and Playwright worker recycling through this file.
const targetId = process.env.BREAKFIX_E2E_TARGET ?? "e2e";
export const sessionFile = join(tmpdir(), `breakfix-admin-e2e-session-${targetId}.json`);

export type AdminSession = { username: string; totpSecret: string };

export function saveBootstrapAdmin(admin: AdminSession) {
  writeFileSync(sessionFile, JSON.stringify(admin));
}

export function readBootstrapAdmin(): AdminSession {
  if (!existsSync(sessionFile)) throw new Error("bootstrap admin session missing; the admin-setup project must run first");
  return JSON.parse(readFileSync(sessionFile, "utf-8")) as AdminSession;
}

export async function loginBootstrapAdmin(request: APIRequestContext): Promise<string> {
  const session = readBootstrapAdmin();
  const login = await request.post(`${apiBase}/api/auth/login`, {
    data: { username: session.username, password: adminPassword, totp_code: totp(session.totpSecret) },
  });
  expect(login.status(), await login.text()).toBe(200);
  return ((await login.json()) as { token: string }).token;
}

// Sign in through the embedded console and open the admin surface. The admin
// entry appears only after the admin signs in.
export async function loginThroughConsole(page: Page, session: AdminSession) {
  await page.goto(apiBase);
  await expect(page.getByRole("button", { name: "管理" })).toHaveCount(0);
  await page.getByRole("button", { name: "Sign in" }).click();
  const signInDialog = page.getByRole("dialog");
  await signInDialog.getByLabel("Username").fill(session.username);
  await signInDialog.getByLabel("Password").fill(adminPassword);
  await signInDialog.getByLabel("Authenticator code").fill(totp(session.totpSecret));
  await signInDialog.getByRole("button", { name: "Sign in" }).click();
  await expect(page.getByRole("button", { name: "管理" })).toBeVisible();
  await page.getByRole("button", { name: "管理" }).click();
}

// Resolve a durable user identifier through the admin user list.
export async function resolveUserId(request: APIRequestContext, adminToken: string, username: string) {
  const response = await request.get(`${apiBase}/api/admin/users`, { headers: { Authorization: `Bearer ${adminToken}` } });
  expect(response.status(), await response.text()).toBe(200);
  const users = await response.json() as { users: Array<{ id: string; subject: string; role: string }> };
  const found = users.users.find((entry) => entry.subject === username);
  expect(found, `account ${username} missing from the admin user list`).toBeDefined();
  return found!.id;
}

if (!apiBase) throw new Error("BREAKFIX_E2E_BASE_URL is required for the admin E2E suite");
