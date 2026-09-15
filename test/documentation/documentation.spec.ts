import { createHmac } from "node:crypto";
import { execFile as execFileCallback } from "node:child_process";
import { promisify } from "node:util";
import { expect, test } from "@playwright/test";

const execFile = promisify(execFileCallback);

const entryUrl = "/documentation?source=kubernetes&version=snapshot-ce98a43&path=%2Fdocs%2F";

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

test("guest can read fixture documentation and retain its location", async ({ page }) => {
  await page.goto("/");
  await expect(page.getByRole("button", { name: "Documentation", exact: true })).toBeVisible();
  await page.getByRole("button", { name: "Documentation", exact: true }).click();
  await expect(page).toHaveURL(/\/documentation\?source=kubernetes/);
  const frame = page.frameLocator('iframe[title="Kubernetes documentation"]');
  await expect(frame.getByRole("heading", { name: "Fixture documentation" })).toBeVisible();
  await expect(page).toHaveURL(/path=%2Fdocs%2F(?:&|$)/);

  await frame.getByRole("link", { name: "Open the next page" }).click();
  await expect(frame.getByRole("heading", { name: "Fixture page" })).toBeVisible();
  await expect(page).toHaveURL(/path=%2Fdocs%2Fpage%2F/);

  await frame.locator("body").evaluate(() => window.history.back());
  await expect(frame.getByRole("heading", { name: "Fixture documentation" })).toBeVisible();
  await expect(page).toHaveURL(/path=%2Fdocs%2F(?:&|$)/);

  await frame.getByRole("link", { name: "Jump to topic" }).click();
  await expect(page).toHaveURL(/hash=topic/);
});

test("documentation reader ignores forged messages", async ({ page }) => {
  await page.goto(entryUrl);
  const frame = page.frameLocator('iframe[title="Kubernetes documentation"]');
  await expect(page).toHaveURL(/path=%2Fdocs%2F(?:&|$)/);
  await frame.locator("body").evaluate(() => {
    window.parent.postMessage({
      type: "breakfix:unknown",
      source: "kubernetes",
      version: "snapshot-ce98a43",
      locale: "en",
      path: "/docs/page/",
      hash: "#forged",
    }, "http://localhost:5173");
    window.parent.postMessage({
      type: "breakfix:document-location",
      source: "kubernetes",
      version: "snapshot-ce98a43",
      locale: "en",
      path: "/docs/page/",
      hash: "not-a-hash",
    }, "http://localhost:5173");
  });
  await page.waitForTimeout(100);
  await expect(page).toHaveURL(/path=%2Fdocs%2F(?:&|$)/);

  const sameOriginPopupPromise = page.waitForEvent("popup");
  await page.evaluate(() => window.open("http://localhost:1314/docs/sender/"));
  const sameOriginPopup = await sameOriginPopupPromise;
  await sameOriginPopup.waitForLoadState();
  await expect(page).toHaveURL(/path=%2Fdocs%2F(?:&|$)/);
  await sameOriginPopup.close();

  const otherOriginPopupPromise = page.waitForEvent("popup");
  await page.evaluate(() => window.open("http://localhost:1315/docs/sender/"));
  const otherOriginPopup = await otherOriginPopupPromise;
  await otherOriginPopup.waitForLoadState();
  await expect(page).toHaveURL(/path=%2Fdocs%2F(?:&|$)/);
  await otherOriginPopup.close();
});

test("documentation navigation is available from the mobile menu", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto("/");
  await page.getByRole("button", { name: "Navigation", exact: true }).click();
  await expect(page.getByRole("navigation", { name: "Mobile primary" }).getByRole("button", { name: "Documentation", exact: true })).toBeVisible();
});

test("documentation reader reports load failures and retries", async ({ page }) => {
  await page.route("http://localhost:1314/**", (route) => route.abort());
  await page.goto(entryUrl);
  // Chromium does not dispatch iframe error for every aborted navigation;
  // exercise the component's error handler explicitly after the failed load.
  await page.locator('iframe[title="Kubernetes documentation"]').evaluate((iframe) => iframe.dispatchEvent(new Event("error")));
  await expect(page.getByText("Documentation is unavailable.", { exact: true })).toBeVisible();
  await page.unroute("http://localhost:1314/**");
  await page.getByRole("button", { name: "Retry", exact: true }).click();
  await expect(page.frameLocator('iframe[title="Kubernetes documentation"]').getByRole("heading", { name: "Fixture documentation" })).toBeVisible();
});

test("documentation reader falls back from a non-document path", async ({ page }) => {
  await page.goto("/documentation?source=kubernetes&version=snapshot-ce98a43&path=%2Fblog%2F");
  await expect(page).toHaveURL(/path=%2Fdocs%2F(?:&|$)/);
  await expect(page.frameLocator('iframe[title="Kubernetes documentation"]').getByRole("heading", { name: "Fixture documentation" })).toBeVisible();
});

test("fixed documentation practice runs through publication", async ({ request }) => {
  test.setTimeout(12 * 60_000);
  const apiBase = process.env.BREAKFIX_E2E_BASE_URL;
  if (!apiBase) throw new Error("BREAKFIX_E2E_BASE_URL is required for the documentation workflow test");
  const username = `documentation-${Date.now()}`;
  const register = await request.post(`${apiBase}/api/auth/register`, { data: { username, password: "documentation-test-password" } });
  expect(register.status()).toBe(201);
  const registration = await register.json() as { totp_secret: string };
  const login = await request.post(`${apiBase}/api/auth/login`, { data: { username, password: "documentation-test-password", totp_code: totp(registration.totp_secret) } });
  expect(login.status()).toBe(200);
  const credentials = await login.json() as { token: string };

  const start = await request.post(`${apiBase}/api/documentation/practice`, { headers: { Authorization: `Bearer ${credentials.token}` } });
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

  await expect.poll(async () => postgres(`SELECT state FROM document_workflows WHERE id = '${started.workflow_id}'`), {
    timeout: 9 * 60_000,
    intervals: [1_000, 2_000, 5_000, 10_000],
  }).toBe("Published");
  expect(await postgres(`SELECT COUNT(*) FROM document_publication_manifests WHERE workflow_id = '${started.workflow_id}'`)).toBe("1");
  expect(await postgres("SELECT COUNT(*) FROM document_practice_index WHERE source_id = 'kubernetes' AND commit = 'ce98a43f24257385a9766003a6dadc95e962dc63'")).toBe("1");
  expect(await postgres(`SELECT COUNT(*) FROM document_artifact_ledger WHERE workflow_id = '${started.workflow_id}' AND kind IN ('document-context', 'learning-unit-plan', 'plan-gate', 'practice-candidate', 'artifact-gate', 'runnable-revision', 'verification-report', 'verification-review', 'publication-manifest')`)).toBe("9");
  expect(await postgres(`SELECT manifest->'document_context'->>'anchor' FROM document_publication_manifests WHERE workflow_id = '${started.workflow_id}'`)).toBe("pod-lifetime");
  expect(await postgres(`SELECT COUNT(*) FROM document_artifact_ledger WHERE workflow_id = '${started.workflow_id}' AND kind = 'learning-unit-plan' AND payload->'user_steps' @> '[{"id":"apply-pod","evidence_ids":["page"]}]'::jsonb`)).toBe("1");
  expect(await postgres(`SELECT COUNT(*) FROM document_artifact_ledger WHERE workflow_id = '${started.workflow_id}' AND kind = 'practice-candidate' AND payload->'user_steps' @> '[{"id":"apply-pod"}]'::jsonb AND payload #> '{spec,validation_plan,phases,0,actions}' @> '[{"id":"apply-pod"}]'::jsonb`)).toBe("1");

  const replay = await request.post(`${apiBase}/api/documentation/practice`, { headers: { Authorization: `Bearer ${credentials.token}` } });
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
});
