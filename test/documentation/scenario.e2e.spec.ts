import { expect, test, type APIRequestContext, type Page } from "@playwright/test";
import { totpCode } from "../support/live-helpers";

const apiBase = process.env.BREAKFIX_E2E_BASE_URL;
const source = "kubernetes";
const version = "snapshot-ce98a43";
const readerUrl = `/documentation?source=${source}&version=${version}&path=%2Fdocs%2Fconcepts%2Fworkloads%2Fpods%2Fpod-lifecycle%2F`;

type ScenarioState = { state: string; environment_id?: string; runtime?: string };

// The suite drives the prepared Kind target through absolute URLs: unlike
// the agent-live configs, this playwright config sets no baseURL.
async function registerAndLogin(page: Page): Promise<string> {
  if (!apiBase) throw new Error("BREAKFIX_E2E_BASE_URL is required for the blank scenario suite");
  const username = `doc-blank-${Date.now()}-${Math.random().toString(36).slice(2, 7)}`;
  const password = "test-password-123";
  await page.goto(`${apiBase}/`);
  await page.getByRole("button", { name: "Register", exact: true }).click();
  await page.locator('input[autocomplete="username"]').fill(username);
  await page.locator('input[autocomplete="new-password"]').fill(password);
  await page.getByRole("button", { name: "Continue", exact: true }).click();
  const secret = await page.locator(".totp-setup code").textContent();
  await page.getByRole("button", { name: "Continue to sign in", exact: true }).click();
  await page.locator('input[autocomplete="username"]').fill(username);
  await page.locator('input[autocomplete="current-password"]').fill(password);
  await page.locator('input[autocomplete="one-time-code"]').fill(await totpCode(page, secret ?? ""));
  await page.getByRole("dialog").getByRole("button", { name: "Sign in", exact: true }).click();
  // The topbar's "My space" entry appears only for a signed-in reader.
  await expect(page.getByRole("navigation", { name: "Primary" }).getByRole("button", { name: "My space", exact: true })).toBeVisible({ timeout: 30_000 });
  return username;
}

async function scenarioState(request: APIRequestContext, token: string): Promise<ScenarioState> {
  const response = await request.get(`${apiBase}/api/documentation/scenario`, {
    headers: { Authorization: `Bearer ${token}` },
  });
  expect(response.status(), await response.text()).toBe(200);
  return (await response.json()) as ScenarioState;
}

async function openReader(page: Page): Promise<string> {
  await registerAndLogin(page);
  const token = await page.evaluate(() => localStorage.getItem("token") ?? "");
  expect(token).not.toBe("");
  await page.goto(`${apiBase}${readerUrl}`);
  return token;
}

async function expectScenarioReady(page: Page) {
  await expect(page.locator(".scenario-toolbar-badge")).toHaveText(/Ready/, { timeout: 15 * 60_000 });
}

// The blank practice scenario is the reader's practice ground: one vk8s
// environment per user, created from the toolbar, worked in through the
// terminal, resettable to a fresh state, and closable. Lifecycle reclamation
// (idle TTL and maximum lifetime) stays unmeasured: only the verbs are under
// test, never the clock.
test("the reader drives one blank scenario through create, terminal, reset, and close", async ({ page, request }) => {
  test.setTimeout(30 * 60_000);

  const token = await openReader(page);

  // The toolbar reflects the shared session immediately after login.
  await expect(page.locator(".scenario-toolbar-badge")).toHaveText("No scenario");

  // Create returns without blocking; readiness arrives by polling. The vk8s
  // provisioner (namespace, vcluster, terminal pod) takes minutes.
  await page.getByRole("button", { name: "Create blank scenario" }).click();
  await expect(page.locator(".scenario-toolbar-badge")).toHaveText(/Preparing|Ready/, { timeout: 60_000 });
  await expectScenarioReady(page);
  expect((await scenarioState(request, token)).state).toBe("ready");

  // The panel is the terminal: it attaches to the fresh vcluster and accepts
  // input; the echoed marker proves the session is interactive.
  await expect(page.locator(".terminal-status").getByText("Connected", { exact: true })).toBeVisible({ timeout: 90_000 });
  const marker = `BREAKFIX_BLANK_${Date.now()}`;
  await page.getByRole("textbox", { name: "Terminal input" }).focus();
  await page.keyboard.type(`printf '%s\\n' '${marker}' && kubectl get ns | wc -l`);
  await page.keyboard.press("Enter");
  await expect(page.locator(".xterm-rows")).toContainText(marker, { timeout: 60_000 });

  // Reset wipes to a fresh state and returns through creating to ready.
  await page.getByRole("button", { name: "Reset blank scenario" }).click();
  await expect(page.locator(".scenario-toolbar-badge")).toHaveText(/Preparing|Ready/, { timeout: 60_000 });
  await expectScenarioReady(page);
  await expect(page.locator(".terminal-status").getByText("Connected", { exact: true })).toBeVisible({ timeout: 90_000 });

  // Close releases the environment; the toolbar returns to none and the API
  // agrees. The same reader may start a fresh session afterwards.
  await page.getByRole("button", { name: "Close blank scenario" }).click();
  await expect(page.locator(".scenario-toolbar-badge")).toHaveText("No scenario", { timeout: 5 * 60_000 });
  await expect.poll(async () => (await scenarioState(request, token)).state, { timeout: 60_000 }).toBe("none");
});
