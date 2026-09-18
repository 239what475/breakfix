import { createHmac } from "node:crypto";
import { execFile as execFileCallback } from "node:child_process";
import { promisify } from "node:util";
import { expect, test, type APIRequestContext, type Page } from "@playwright/test";

const execFile = promisify(execFileCallback);

const apiBase = process.env.BREAKFIX_E2E_BASE_URL;
const source = "kubernetes";
const version = "snapshot-ce98a43";
const entryUrl = `/documentation?source=${source}&version=${version}&path=%2Fdocs%2F`;
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

async function postAfterServerRestart(request: APIRequestContext, url: string, token: string) {
  let lastError: unknown;
  for (let attempt = 0; attempt < 8; attempt += 1) {
    try {
      return await request.post(url, { headers: { Authorization: `Bearer ${token}` } });
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

test("guest can read parsed documentation and retain its location", async ({ page }) => {
  await gotoReader(page, entryUrl);
  const outline = page.getByRole("navigation", { name: "Documentation outline" });
  await expect(outline).toBeVisible();
  // A section root is not a page: the outline is the entry experience.
  await expect(page.getByText("Choose a page from the outline to start reading.")).toBeVisible();
  await expect(page).toHaveURL(/path=%2Fdocs%2F(?:&|$)/);

  // Walk the outline to the pinned practice page.
  await page.getByRole("button", { name: "Toggle Concepts section" }).click();
  await page.getByRole("button", { name: "Toggle Workloads section" }).click();
  await page.getByRole("button", { name: "Toggle Pods section" }).click();
  await page.getByRole("button", { name: "Pod Lifecycle", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Pod Lifecycle", exact: true })).toBeVisible();
  await expect(page).toHaveURL(/path=%2Fdocs%2Fconcepts%2Fworkloads%2Fpods%2Fpod-lifecycle%2F(?:&|$)/);

  // Parsed markdown keeps structure: the pinned anchor is an addressable
  // heading with its library identity.
  await expect(page.locator("#pod-lifetime")).toBeVisible();

  // Back returns to the section root; forward restores the page.
  await page.goBack();
  await expect(page).toHaveURL(/path=%2Fdocs%2F(?:&|$)/);
  await expect(page.getByText("Choose a page from the outline to start reading.")).toBeVisible();
  await page.goForward();
  await expect(page.getByRole("heading", { name: "Pod Lifecycle", exact: true })).toBeVisible();

  // Scrolling keeps the URL hash in sync with the current anchor.
  await page.locator("#pod-lifetime").evaluate((heading) => heading.scrollIntoView({ block: "start" }));
  await expect(page).toHaveURL(/hash=pod-lifetime/, { timeout: 10_000 });
});

test("parsed pages carry alerts, code, and headings", async ({ page }) => {
  await gotoReader(page, podLifecycleUrl);
  const article = page.locator(".documentation-article");
  await expect(article).toBeVisible();
  // The generator flattens interactive chrome; markdown structure survives.
  // Tables on this page are degraded to text rows by the offline generator,
  // so the reader assertions cover headings, alerts, and code blocks.
  await expect(article.locator("h2#pod-lifetime")).toBeVisible();
  await expect(article.locator("blockquote.doc-alert").first()).toBeVisible();
  await expect(article.locator("blockquote.doc-alert-note").first()).toBeVisible();
  await expect(article.locator("blockquote.doc-alert-caution").first()).toBeVisible();
  await expect(article.locator("pre").first()).toBeVisible();
  await expect(article.locator("pre.shiki").first()).toBeVisible();
});

test("documentation reader reports load failures and retries", async ({ page }) => {
  await page.route("**/api/documentation/page**", (route) => route.abort());
  await gotoReader(page, podLifecycleUrl);
  await expect(page.getByText("Documentation is unavailable.", { exact: true })).toBeVisible();
  await page.unroute("**/api/documentation/page**");
  await page.getByRole("button", { name: "Retry", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Pod Lifecycle", exact: true })).toBeVisible();
});

test("documentation reader falls back from a non-document path", async ({ page }) => {
  await gotoReader(page, `/documentation?source=${source}&version=${version}&path=%2Fblog%2F`);
  await expect(page).toHaveURL(/path=%2Fdocs%2F(?:&|$)/);
  await expect(page.getByText("Choose a page from the outline to start reading.")).toBeVisible();
});

test("documentation navigation is available from the mobile menu and drawer", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await gotoReader(page, entryUrl);
  await expect(page.getByRole("button", { name: "Contents", exact: true })).toBeVisible();
  await page.getByRole("button", { name: "Contents", exact: true }).click();
  await expect(page.getByRole("navigation", { name: "Documentation outline" })).toBeVisible();
  await page.getByRole("button", { name: "Close documentation outline" }).click();
  await expect(page.getByRole("navigation", { name: "Documentation outline" })).not.toBeVisible();

  await page.goto(`${apiBase}/`);
  await page.getByRole("button", { name: "Navigation", exact: true }).click();
  await expect(page.getByRole("navigation", { name: "Mobile primary" }).getByRole("button", { name: "Documentation", exact: true })).toBeVisible();
});

test("fixed documentation practice runs through publication", async ({ request }) => {
  test.setTimeout(12 * 60_000);
  if (!apiBase) throw new Error("BREAKFIX_E2E_BASE_URL is required for the documentation workflow test");
  const username = `documentation-${Date.now()}`;
  const register = await request.post(`${apiBase}/api/auth/register`, { data: { username, password: "documentation-test-password" } });
  expect(register.status(), await register.text()).toBe(201);
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
});
