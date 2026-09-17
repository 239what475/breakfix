import { createHmac } from "node:crypto";
import { execFile as execFileCallback } from "node:child_process";
import { promisify } from "node:util";
import { expect, test, type APIRequestContext } from "@playwright/test";

const execFile = promisify(execFileCallback);

const apiBase = process.env.BREAKFIX_E2E_BASE_URL;
if (!apiBase) throw new Error("BREAKFIX_E2E_BASE_URL is required for the admin E2E suite");
const password = "admin-e2e-password";

function decodeBase32(value: string) {
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

function totp(secret: string, now = Date.now()) {
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

async function postgres(sql: string) {
  const { stdout } = await execFile("kubectl", [
    "-n", "breakfix-system", "exec", "breakfix-postgresql-0", "--", "sh", "-ec",
    `psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -Atc ${JSON.stringify(sql)}`,
  ]);
  return stdout.trim();
}

async function scaleRuntimeWorker(replicas: number) {
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

async function registerAndLogin(request: APIRequestContext, username: string) {
  const registerResponse = await request.post(`${apiBase}/api/auth/register`, { data: { username, password } });
  expect(registerResponse.status()).toBe(201);
  const registration = await registerResponse.json() as { totp_secret: string };
  const loginResponse = await request.post(`${apiBase}/api/auth/login`, {
    data: { username, password, totp_code: totp(registration.totp_secret) },
  });
  expect(loginResponse.status()).toBe(200);
  const credentials = await loginResponse.json() as { token: string };
  const payload = JSON.parse(Buffer.from(credentials.token.split(".")[1].replace(/-/g, "+").replace(/_/g, "/"), "base64").toString()) as { role?: string };
  return { username, token: credentials.token, totpSecret: registration.totp_secret, role: payload.role ?? "" };
}

test("admin console rescues a stuck documentation workflow end to end", async ({ page, request }) => {
  test.setTimeout(15 * 60_000);

  // The prepare script reset the database, so the first registration elects
  // the bootstrap admin and the second stays an ordinary user.
  const adminUsername = `admin-${Date.now()}`;
  const memberUsername = `member-${Date.now()}`;
  const admin = await registerAndLogin(request, adminUsername);
  expect(admin.role).toBe("admin");
  const member = await registerAndLogin(request, memberUsername);
  expect(member.role).toBe("user");

  // The plain user is locked out of the admin surface by the backend.
  const denied = await request.get(`${apiBase}/api/admin/users`, { headers: { Authorization: `Bearer ${member.token}` } });
  expect(denied.status()).toBe(403);
  const deniedIgnition = await request.post(`${apiBase}/api/documentation/practice`, { headers: { Authorization: `Bearer ${member.token}` } });
  expect(deniedIgnition.status()).toBe(403);

  // The embedded console shows the admin entry only after the admin signs in.
  await page.goto(apiBase);
  await page.getByRole("button", { name: "Sign in" }).click();
  const signInDialog = page.getByRole("dialog");
  await signInDialog.getByLabel("Username").fill(adminUsername);
  await signInDialog.getByLabel("Password").fill(password);
  await signInDialog.getByLabel("Authenticator code").fill(totp(admin.totpSecret));
  await signInDialog.getByRole("button", { name: "Sign in" }).click();
  await expect(page.getByRole("button", { name: "管理" })).toBeVisible();
  await page.getByRole("button", { name: "管理" }).click();
  await expect(page.getByText("队列积压摘要")).toBeVisible();

  // Removing the worker first simulates a permanently stalled provider: the
  // Agent phases run against the in-cluster fixture, then the workflow parks
  // in MaterializingArtifact with a queued action and no Worker to claim it.
  await scaleRuntimeWorker(0);
  const start = await request.post(`${apiBase}/api/documentation/practice`, { headers: { Authorization: `Bearer ${admin.token}` } });
  expect(start.status(), await start.text()).toBe(202);
  const started = await start.json() as { workflow_id: string };
  const workflowId = started.workflow_id;
  await expect.poll(async () => postgres(`SELECT state FROM document_workflows WHERE id = '${workflowId}'`), {
    timeout: 90_000,
    intervals: [1_000, 2_000],
  }).toBe("MaterializingArtifact");

  // The workflow list shows the stalled workflow; force-fail needs a reason
  // and runs through the console dialog with its audit summary.
  await page.getByRole("button", { name: "Refresh", exact: true }).first().click();
  const workflowRow = page.locator(".admin-workflow-row", { hasText: workflowId });
  await expect(workflowRow).toBeVisible();
  await expect(workflowRow).toContainText("MaterializingArtifact");
  await workflowRow.getByRole("button", { name: "Force-fail" }).click();
  // The confirm button stays disabled until the required reason is typed;
  // the API's own reason validation is covered by the handler tests.
  await page.getByLabel(/Reason/).fill("E2E: worker removed, materialization stalled");
  await page.getByRole("button", { name: "确认执行" }).click();
  await expect(workflowRow).toContainText("Failed");

  expect(await postgres(`SELECT state FROM document_workflows WHERE id = '${workflowId}'`)).toBe("Failed");
  expect(await postgres(`SELECT COUNT(*) FROM document_artifact_ledger WHERE workflow_id = '${workflowId}' AND kind = 'admin.force_fail' AND owner_role = 'admin'`)).toBe("1");
  expect(await postgres(`SELECT COUNT(*) FROM human_action_audits WHERE action = 'documentation.workflow.force_fail' AND target_id = '${workflowId}'`)).toBe("1");
  const repeatForceFail = await request.post(`${apiBase}/api/admin/documentation/workflows/${workflowId}/force-fail`, {
    headers: { Authorization: `Bearer ${admin.token}` },
    data: { reason: "again" },
  });
  expect(repeatForceFail.status()).toBe(409);

  // Restart resets the failed workflow to Planning; a second restart on the
  // non-terminal Planning state conflicts.
  await workflowRow.getByRole("button", { name: "Restart" }).click();
  await page.getByLabel(/Reason/).fill("E2E: retry after worker recovery");
  await page.getByRole("button", { name: "确认执行" }).click();
  await expect(workflowRow).toContainText("Planning");
  expect(await postgres(`SELECT revision FROM document_workflows WHERE id = '${workflowId}'`)).toBe("2");
  const repeatRestart = await request.post(`${apiBase}/api/admin/documentation/workflows/${workflowId}/restart`, {
    headers: { Authorization: `Bearer ${admin.token}` },
    data: { reason: "again" },
  });
  expect(repeatRestart.status()).toBe(409);

  // The ordinary ignition endpoint re-drives the restarted workflow. The
  // Worker comes back only after the re-run has parked again, so the console
  // actions are what rescue the workflow, then publication completes.
  const reignite = await request.post(`${apiBase}/api/documentation/practice`, { headers: { Authorization: `Bearer ${admin.token}` } });
  expect(reignite.status(), await reignite.text()).toBe(202);
  await expect.poll(async () => postgres(`SELECT state FROM document_workflows WHERE id = '${workflowId}'`), {
    timeout: 90_000,
    intervals: [1_000, 2_000],
  }).toBe("MaterializingArtifact");
  await scaleRuntimeWorker(1);
  await expect.poll(async () => postgres(`SELECT state FROM document_workflows WHERE id = '${workflowId}'`), {
    timeout: 10 * 60_000,
    intervals: [2_000, 5_000, 10_000],
  }).toBe("Published");

  // The audit ledger lists every verb with its actor and transitions.
  const audit = await request.get(`${apiBase}/api/admin/audit?limit=50`, { headers: { Authorization: `Bearer ${admin.token}` } });
  expect(audit.status()).toBe(200);
  const auditPage = await audit.json() as { items: Array<{ action: string; detail: Record<string, string> }> };
  const auditActions = auditPage.items.map((item) => item.action);
  expect(auditActions).toContain("documentation.practice.start");
  expect(auditActions).toContain("documentation.workflow.force_fail");
  expect(auditActions).toContain("documentation.workflow.restart");
  const forceFailAudit = auditPage.items.find((item) => item.action === "documentation.workflow.force_fail");
  expect(forceFailAudit?.detail["from_state"]).toBe("MaterializingArtifact");
  expect(forceFailAudit?.detail["to_state"]).toBe("Failed");

  // The queue endpoint answers with the whole-queue summary.
  const queue = await request.get(`${apiBase}/api/admin/runnable-actions`, { headers: { Authorization: `Bearer ${admin.token}` } });
  expect(queue.status()).toBe(200);
  const queuePage = await queue.json() as { summary: { by_state: Record<string, number> }; items: Array<{ flag: string }> };
  expect(queuePage.summary.by_state).toHaveProperty("completed");

  // The environment observation endpoint answers against the live cluster.
  // By publication time the verification environment has already been
  // destroyed through the ordinary drain path, so an empty list is expected.
  const environments = await request.get(`${apiBase}/api/admin/environments`, { headers: { Authorization: `Bearer ${admin.token}` } });
  expect(environments.status()).toBe(200);
  const environmentList = await environments.json() as { environments: Array<{ name: string; phase: string }> };
  expect(environmentList.environments.every((entry) => entry.phase !== "Released")).toBe(true);

  // TOTP reset through the console rotates the member's second factor once.
  const memberUserId = await resolveMemberUserId(request, memberUsername, admin.token);
  const badPassword = await request.post(`${apiBase}/api/admin/users/${memberUserId}/totp-reset`, {
    headers: { Authorization: `Bearer ${admin.token}` },
    data: { password: "wrong-password" },
  });
  expect(badPassword.status()).toBe(403);

  await page.getByRole("button", { name: "用户" }).click();
  const memberRow = page.locator(".admin-workflow-row", { hasText: memberUsername });
  await memberRow.getByRole("button", { name: "重置 TOTP" }).click();
  await page.getByLabel("操作者密码").fill(password);
  await page.getByRole("button", { name: "确认重置" }).click();
  const secretDialog = page.locator(".dialog", { hasText: "新 TOTP 已生效" });
  await expect(secretDialog).toBeVisible();
  const rotatedSecret = (await secretDialog.locator("code").first().textContent()) ?? "";
  await secretDialog.getByRole("button", { name: "我已保存" }).click();
  expect(rotatedSecret).not.toBe(member.totpSecret);

  const oldCodeLogin = await request.post(`${apiBase}/api/auth/login`, {
    data: { username: memberUsername, password, totp_code: totp(member.totpSecret) },
  });
  expect(oldCodeLogin.status()).toBe(401);
  const newCodeLogin = await request.post(`${apiBase}/api/auth/login`, {
    data: { username: memberUsername, password, totp_code: totp(rotatedSecret) },
  });
  expect(newCodeLogin.status()).toBe(200);
});

// Resolve the member's durable identifier through the admin user list.
async function resolveMemberUserId(request: APIRequestContext, memberUsername: string, adminToken: string) {
  const response = await request.get(`${apiBase}/api/admin/users`, { headers: { Authorization: `Bearer ${adminToken}` } });
  expect(response.status()).toBe(200);
  const users = await response.json() as { users: Array<{ id: string; subject: string; role: string }> };
  const found = users.users.find((entry) => entry.subject === memberUsername && entry.role === "user");
  expect(found, "member account missing from the admin user list").toBeDefined();
  return found!.id;
}
