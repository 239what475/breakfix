import { expect, test, type APIRequestContext, type Page } from "@playwright/test";
import { totpCode } from "../support/live-helpers";

const apiBase = process.env.BREAKFIX_E2E_BASE_URL;

type PlaygroundState = { state: string; environment_id?: string; runtime?: string };

// The suite drives the prepared Kind target through absolute URLs: unlike
// the agent-live configs, this playwright config sets no baseURL.
async function registerAndLogin(page: Page): Promise<string> {
  if (!apiBase) throw new Error("BREAKFIX_E2E_BASE_URL is required for the playground suite");
  const username = `playground-${Date.now()}-${Math.random().toString(36).slice(2, 7)}`;
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

async function playgroundState(request: APIRequestContext, token: string): Promise<PlaygroundState> {
  const response = await request.get(`${apiBase}/api/playground`, {
    headers: { Authorization: `Bearer ${token}` },
  });
  expect(response.status(), await response.text()).toBe(200);
  return (await response.json()) as PlaygroundState;
}

// Registers through the API alone: the capacity leg needs a second identity,
// not a second browser session. The TOTP code is derived in the page context
// to reuse the same in-browser generator the UI login uses.
async function registerApiUser(page: Page, request: APIRequestContext): Promise<string> {
  const username = `playground-second-${Date.now()}-${Math.random().toString(36).slice(2, 7)}`;
  const password = "test-password-123";
  const registerResponse = await request.post(`${apiBase}/api/auth/register`, { data: { username, password } });
  expect(registerResponse.status(), await registerResponse.text()).toBe(201);
  const registration = (await registerResponse.json()) as { totp_secret: string };
  const loginResponse = await request.post(`${apiBase}/api/auth/login`, {
    data: { username, password, totp_code: await totpCode(page, registration.totp_secret) },
  });
  expect(loginResponse.status(), await loginResponse.text()).toBe(200);
  return ((await loginResponse.json()) as { token: string }).token;
}

const ball = (page: Page) => page.locator("button.playground-fab");

async function expectBallState(page: Page, state: "none" | "ready", timeout = 15 * 60_000) {
  await expect(ball(page)).toHaveAttribute("data-state", state, { timeout });
}

// The playground is the user's own practice environment: one vk8s session per
// user, site-wide, entered from the floating ball on the home page — the
// placement proves the binding is the user, not any content page. It is
// created from the ball, worked in through the terminal, resettable to a
// fresh state, and closable. Lifecycle reclamation (idle TTL and maximum
// lifetime) stays unmeasured: only the verbs are under test, never the clock.
test("the user drives one playground through create, terminal, reset, and close", async ({ page, request }) => {
  test.setTimeout(30 * 60_000);

  await registerAndLogin(page);
  const token = await page.evaluate(() => localStorage.getItem("token") ?? "");
  expect(token).not.toBe("");

  // The ball reflects the shared session immediately after login.
  await expect(ball(page)).toBeVisible();
  await expectBallState(page, "none", 30_000);

  await ball(page).click();
  const panel = page.locator(".playground-panel");
  await expect(panel).toBeVisible();
  await panel.getByRole("button", { name: "Create", exact: true }).click();

  // Create returns without blocking; readiness arrives by polling. The vk8s
  // provisioner (namespace, vcluster, terminal pod) takes minutes.
  await expect(ball(page)).toHaveAttribute("data-state", /creating|ready/, { timeout: 60_000 });
  await expectBallState(page, "ready");
  expect((await playgroundState(request, token)).state).toBe("ready");

  // The panel is the terminal: it attaches to the fresh vcluster and accepts
  // input; the echoed marker proves the session is interactive.
  await expect(page.locator(".terminal-status").getByText("Connected", { exact: true })).toBeVisible({ timeout: 90_000 });
  const marker = `BREAKFIX_PLAYGROUND_${Date.now()}`;
  await page.getByRole("textbox", { name: "Terminal input" }).focus();
  await page.keyboard.type(`printf '%s\\n' '${marker}' && kubectl get ns | wc -l`);
  await page.keyboard.press("Enter");
  await expect(page.locator(".xterm-rows")).toContainText(marker, { timeout: 60_000 });

  // Reset wipes to a fresh state and returns through creating to ready.
  await panel.getByRole("button", { name: "Reset", exact: true }).click();
  await expect(ball(page)).toHaveAttribute("data-state", /creating|ready/, { timeout: 60_000 });
  await expectBallState(page, "ready");
  await expect(page.locator(".terminal-status").getByText("Connected", { exact: true })).toBeVisible({ timeout: 90_000 });

  // The prepared target pins playground.max_active to 1: while this session
  // occupies the only slot, a second user's create is rejected and their
  // session stays none. Reads stay ungated for everyone.
  const secondToken = await registerApiUser(page, request);
  const rejected = await request.post(`${apiBase}/api/playground`, {
    headers: { Authorization: `Bearer ${secondToken}` },
  });
  expect(rejected.status(), await rejected.text()).toBe(429);
  expect((await playgroundState(request, secondToken)).state).toBe("none");

  // Close releases the environment; the ball returns to none and the API
  // agrees. The same user may start a fresh session afterwards.
  await panel.getByRole("button", { name: "Close", exact: true }).click();
  await expectBallState(page, "none", 5 * 60_000);
  await expect.poll(async () => (await playgroundState(request, token)).state, { timeout: 60_000 }).toBe("none");

  // The prepare reset leaves a fresh database, so the suite's first account is
  // the bootstrap admin: the environment console renders against the live
  // endpoints, and the overview card shows the cap this target configured.
  const role = await page.evaluate(() => {
    const payload = (localStorage.getItem("token") ?? "").split(".")[1] ?? "";
    const normalized = payload.replace(/-/g, "+").replace(/_/g, "/");
    if (!normalized) return "";
    const padded = normalized.padEnd(Math.ceil(normalized.length / 4) * 4, "=");
    return (JSON.parse(atob(padded)) as { role?: string }).role ?? "";
  });
  expect(role).toBe("admin");
  await page.getByRole("button", { name: "管理", exact: true }).click();
  await page.getByRole("navigation", { name: "Admin sections" }).getByRole("button", { name: "环境", exact: true }).first().click();
  await expect(page.getByRole("heading", { name: "环境观测" }).first()).toBeVisible();
  await expect(page.locator(".admin-overview-card").first()).toContainText("/ 1", { timeout: 30_000 });
});
