import { createHmac } from "node:crypto";
import { execFile as execFileCallback } from "node:child_process";
import { promisify } from "node:util";
import { expect, test, type APIRequestContext, type Page } from "@playwright/test";

const execFile = promisify(execFileCallback);

const apiBase = process.env.BREAKFIX_E2E_BASE_URL;
const source = "kubernetes";
const version = "snapshot-ce98a43";
const podLifecycleUrl = `/documentation?source=${source}&version=${version}&path=%2Fdocs%2Fconcepts%2Fworkloads%2Fpods%2Fpod-lifecycle%2F`;

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

async function restartDeployment(name: string) {
  await execFile("kubectl", ["-n", "breakfix-system", "rollout", "restart", `deployment/${name}`]);
  await execFile("kubectl", ["-n", "breakfix-system", "rollout", "status", `deployment/${name}`, "--timeout=3m"]);
}

async function runtimeEnvironmentExists(uid: string) {
  const { stdout } = await execFile("kubectl", ["-n", "breakfix-system", "get", "runtimeenvironments.breakfix.dev", "-o", "json"]);
  const resources = JSON.parse(stdout) as { items?: Array<{ metadata?: { uid?: string } }> };
  return resources.items?.some((item) => item.metadata?.uid === uid) ?? false;
}

// The deployment pins the library identity, not a page: ignition addresses
// one page of the opened corpus.
const practiceStartBody = { page_path: "docs/concepts/workloads/pods/pod-lifecycle", anchor: "pod-lifetime" };

async function postAfterServerRestart(request: APIRequestContext, url: string, token: string) {
  let lastError: unknown;
  for (let attempt = 0; attempt < 8; attempt += 1) {
    try {
      return await request.post(url, { headers: { Authorization: `Bearer ${token}` }, data: practiceStartBody });
    } catch (error) {
      lastError = error;
      await new Promise((resolve) => setTimeout(resolve, 250 * (attempt + 1)));
    }
  }
  throw lastError;
}

async function gotoReader(page: Page, readerUrl: string) {
  if (!apiBase) throw new Error("BREAKFIX_E2E_BASE_URL is required for the documentation reader tests");
  await page.goto(`${apiBase}${readerUrl}`);
}

test("fixed documentation practice runs through publication", async ({ page, request }) => {
  test.setTimeout(12 * 60_000);
  if (!apiBase) throw new Error("BREAKFIX_E2E_BASE_URL is required for the documentation workflow test");
  const username = `documentation-${Date.now()}`;
  const register = await request.post(`${apiBase}/api/auth/register`, { data: { username, password: "documentation-test-password" } });
  expect(register.status(), await register.text()).toBe(201);
  const registration = await register.json() as { totp_secret: string };
  const login = await request.post(`${apiBase}/api/auth/login`, { data: { username, password: "documentation-test-password", totp_code: totp(registration.totp_secret) } });
  expect(login.status(), await login.text()).toBe(200);
  const credentials = await login.json() as { token: string };

  const start = await request.post(`${apiBase}/api/documentation/practice`, { headers: { Authorization: `Bearer ${credentials.token}` }, data: practiceStartBody });
  expect(start.status(), await start.text()).toBe(202);
  const started = await start.json() as { workflow_id: string; state: string };
  expect(started.workflow_id).toMatch(/^document-workflow-/);
  await expect.poll(async () => postgres(`SELECT state FROM document_workflows WHERE id = '${started.workflow_id}'`), {
    timeout: 60_000,
    intervals: [500, 1_000, 2_000],
  }).toMatch(/^(MaterializingArtifact|Verifying)$/);
  await expect.poll(async () => postgres(`SELECT state FROM runnable_actions WHERE content_kind = 'documentation-practice' AND phase = 'materialize-artifact'`), {
    timeout: 60_000,
    intervals: [500, 1_000, 2_000],
  }).toBe("running");
  await restartDeployment("breakfix-runtime-worker");
  await restartDeployment("breakfix-server");

  await expect.poll(async () => postgres(`SELECT state FROM document_workflows WHERE id = '${started.workflow_id}'`), {
    timeout: 9 * 60_000,
    intervals: [1_000, 2_000, 5_000, 10_000],
  }).toBe("Published");
  expect(await postgres(`SELECT COUNT(*) FROM document_publication_manifests WHERE workflow_id = '${started.workflow_id}'`)).toBe("1");
  expect(await postgres("SELECT COUNT(*) FROM document_practice_index WHERE source_id = 'kubernetes' AND commit = 'ce98a43f24257385a9766003a6dadc95e962dc63'")).toBe("1");
  expect(await postgres(`SELECT COUNT(*) FROM document_artifact_ledger WHERE workflow_id = '${started.workflow_id}' AND kind IN ('document-context', 'learning-unit-plan', 'plan-gate', 'practice-candidate', 'artifact-gate', 'runnable-revision', 'verification-report', 'verification-review', 'publication-manifest')`)).toBe("9");
  expect(await postgres(`SELECT manifest->'document_context'->>'anchor' FROM document_publication_manifests WHERE workflow_id = '${started.workflow_id}'`)).toBe("pod-lifetime");
  // The evidence triple binds the publication to the offline library: the
  // generator version and the digest of the parsed page (deterministic for the
  // fixture page, identical to the full docs-site/documents library).
  expect(await postgres(`SELECT manifest->'document_context'->>'parser_version' FROM document_publication_manifests WHERE workflow_id = '${started.workflow_id}'`)).toBe("docs-project-v10");
  expect(await postgres(`SELECT manifest->'document_context'->>'page_digest' FROM document_publication_manifests WHERE workflow_id = '${started.workflow_id}'`)).toBe("sha256:587884599e33084ccd9ff37feeb38fc553414e1b6273a1f55dfdf3aefa61e5c0");
  expect(await postgres(`SELECT COUNT(*) FROM document_artifact_ledger WHERE workflow_id = '${started.workflow_id}' AND kind = 'learning-unit-plan' AND payload->'user_steps' @> '[{"id":"apply-pod","evidence_ids":["page"]}]'::jsonb`)).toBe("1");
  expect(await postgres(`SELECT COUNT(*) FROM document_artifact_ledger WHERE workflow_id = '${started.workflow_id}' AND kind = 'practice-candidate' AND payload->'user_steps' @> '[{"id":"apply-pod"}]'::jsonb AND payload #> '{spec,validation_plan,phases,0,actions}' @> '[{"id":"apply-pod"}]'::jsonb`)).toBe("1");

  const replay = await postAfterServerRestart(request, `${apiBase}/api/documentation/practice`, credentials.token);
  expect(replay.status(), await replay.text()).toBe(202);
  const replayed = await replay.json() as { workflow_id: string; state: string };
  expect(replayed).toEqual({ workflow_id: started.workflow_id, state: "Published" });
  expect(await postgres(`SELECT COUNT(*) FROM document_artifact_ledger WHERE workflow_id = '${started.workflow_id}'`)).toBe("9");
  expect(Number(await postgres(`SELECT attempt FROM runnable_actions WHERE content_kind = 'documentation-practice' AND phase = 'materialize-artifact'`))).toBeGreaterThanOrEqual(2);
  const environmentUID = await postgres(`SELECT reports.report #>> '{environment,id}' FROM runnable_verification_reports reports JOIN document_practice_revisions revisions ON revisions.verification_report_id = reports.id WHERE revisions.workflow_id = '${started.workflow_id}'`);
  expect(environmentUID).toMatch(/^[0-9a-f-]{36}$/);
  await expect.poll(async () => runtimeEnvironmentExists(environmentUID), {
    timeout: 2 * 60_000,
    intervals: [500, 1_000, 2_000, 5_000],
  }).toBe(false);

  // The published practice leaves its reader entry on the anchor - the one
  // button-existence line of the practice UI contract kept at chain level.
  await gotoReader(page, podLifecycleUrl);
  await expect(page.locator("h2#pod-lifetime .practice-anchor-button")).toBeVisible();
});

// The reader tests below need the published practice on the pinned page. The
// publication test above creates it on a fresh target; this helper only
// ignites the fixed workflow when the index is still empty.
async function ensurePracticePublished(request: APIRequestContext) {
  const count = await postgres("SELECT COUNT(*) FROM document_practice_index WHERE anchor = 'pod-lifetime'");
  if (Number(count) >= 1) return;
  const username = `practice-reader-${Date.now()}`;
  const register = await request.post(`${apiBase}/api/auth/register`, { data: { username, password: "documentation-test-password" } });
  expect(register.status(), await register.text()).toBe(201);
  const registration = await register.json() as { totp_secret: string };
  const login = await request.post(`${apiBase}/api/auth/login`, { data: { username, password: "documentation-test-password", totp_code: totp(registration.totp_secret) } });
  expect(login.status(), await login.text()).toBe(200);
  const credentials = await login.json() as { token: string };
  const start = await request.post(`${apiBase}/api/documentation/practice`, { headers: { Authorization: `Bearer ${credentials.token}` }, data: practiceStartBody });
  expect(start.status(), await start.text()).toBe(202);
  const started = await start.json() as { workflow_id: string };
  await expect.poll(async () => postgres(`SELECT state FROM document_workflows WHERE id = '${started.workflow_id}'`), {
    timeout: 10 * 60_000,
    intervals: [1_000, 2_000, 5_000, 10_000],
  }).toBe("Published");
}

test("the practice session prepares, attaches, and stops in the panel", async ({ page, request }) => {
  test.setTimeout(15 * 60_000);
  await ensurePracticePublished(request);

  // A logged-in reader: the session verbs all require the JWT.
  const username = `practice-session-${Date.now()}`;
  const register = await request.post(`${apiBase}/api/auth/register`, { data: { username, password: "documentation-test-password" } });
  expect(register.status(), await register.text()).toBe(201);
  const registration = await register.json() as { totp_secret: string };
  const login = await request.post(`${apiBase}/api/auth/login`, { data: { username, password: "documentation-test-password", totp_code: totp(registration.totp_secret) } });
  expect(login.status(), await login.text()).toBe(200);
  const credentials = await login.json() as { token: string };
  await page.addInitScript((token) => window.localStorage.setItem("token", token), credentials.token);

  await gotoReader(page, podLifecycleUrl);
  await page.locator("#pod-lifetime .practice-anchor-button").click();
  const panel = page.locator(".practice-panel");
  await expect(panel.getByRole("heading", { name: "Observe Pod lifetime" })).toBeVisible();

  // Click-to-session: the temporary environment prepares in the panel, then
  // the terminal attaches once it is Ready.
  await expect(panel.locator(".practice-panel-terminal")).toBeVisible({ timeout: 10 * 60_000 });
  await expect(panel.locator(".terminal-status")).toContainText("Connected", { timeout: 90_000 });
  await page.keyboard.type("echo practice-ready");
  await page.keyboard.press("Enter");
  await expect(panel.locator(".practice-panel-terminal")).toContainText("practice-ready", { timeout: 30_000 });

  // Closing the panel only detaches; re-entering finds the environment again
  // and reattaches the terminal.
  await panel.getByRole("button", { name: "Close practice panel" }).click();
  await expect(page.locator(".practice-panel")).toHaveCount(0);
  await page.locator("#pod-lifetime .practice-anchor-button").click();
  await expect(panel.locator(".practice-panel-terminal")).toBeVisible({ timeout: 10 * 60_000 });
  await expect(panel.locator(".terminal-status")).toContainText("Connected", { timeout: 90_000 });

  // The explicit stop is optimistic: the panel leaves the ready state right
  // away and the server drains and deletes the environment in the background.
  await panel.getByRole("button", { name: "Stop", exact: true }).click();
  await expect(page.locator(".toast").getByText("Practice environment stop requested.")).toBeVisible();
  await expect(panel.locator(".practice-panel-terminal")).toHaveCount(0);
});
