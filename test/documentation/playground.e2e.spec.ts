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

  // Close releases the environment; the ball returns to none and the API
  // agrees. The same user may start a fresh session afterwards.
  await panel.getByRole("button", { name: "Close", exact: true }).click();
  await expectBallState(page, "none", 5 * 60_000);
  await expect.poll(async () => (await playgroundState(request, token)).state, { timeout: 60_000 }).toBe("none");
});
