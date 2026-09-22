import { expect, test } from "@playwright/test";
import { totpCode } from "../support/live-helpers";

const apiBase = process.env.BREAKFIX_E2E_BASE_URL;

type Link = { key: string; title: string; url: string; embed: boolean };

// This project runs before the playground suite (see the config's project
// dependencies): the prepared target has an empty identity table, so the
// first registered account is the bootstrap admin and can seed the shared
// documentation list through the admin API alone.
async function registerBootstrapAdmin(page: import("@playwright/test").Page): Promise<string> {
  // Land on the app origin first: the TOTP code is derived in the page
  // context, and crypto.subtle needs a real (non-about:blank) document.
  await page.goto(`${apiBase}/`);
  const username = `docs-admin-${Date.now()}`;
  const password = "test-password-123";
  const register = await page.request.post(`${apiBase}/api/auth/register`, { data: { username, password } });
  expect(register.status(), await register.text()).toBe(201);
  const registration = (await register.json()) as { totp_secret: string };
  const login = await page.request.post(`${apiBase}/api/auth/login`, {
    data: { username, password, totp_code: await totpCode(page, registration.totp_secret) },
  });
  expect(login.status(), await login.text()).toBe(200);
  return ((await login.json()) as { token: string }).token;
}

test("the aggregation page renders an admin-seeded link and frames its URL", async ({ page }) => {
  test.setTimeout(2 * 60_000);

  const token = await registerBootstrapAdmin(page);

  // Seed the shared list through the admin write trio and read it back
  // through the public endpoint.
  const created = await page.request.post(`${apiBase}/api/admin/documentation/links`, {
    headers: { Authorization: `Bearer ${token}` },
    data: { title: "Kubernetes documentation", url: "https://kubernetes.io/docs/home/", embed: true },
  });
  expect(created.status(), await created.text()).toBe(200);
  const link = (await created.json()) as Link;
  expect(link.key).toMatch(/^doc-/);
  const refused = await page.request.post(`${apiBase}/api/admin/documentation/links`, {
    headers: { Authorization: `Bearer ${token}` },
    data: { title: "Refused embedding", url: "https://docs.example.internal/registry", embed: false },
  });
  expect(refused.status(), await refused.text()).toBe(200);

  const list = await page.request.get(`${apiBase}/api/documentation/links`);
  expect(list.status()).toBe(200);
  expect(((await list.json()) as { links: Link[] }).links).toHaveLength(2);

  // The public page renders the list and mounts the embeddable entry with
  // the stored URL as its src; the URL card backs the entry without one.
  // The browser session is still anonymous here, so the admin add row and
  // per-entry settings stay hidden.
  await page.goto(`${apiBase}/documentation`);
  const items = page.locator(".documentation-list-item");
  await expect(items).toHaveCount(2);
  await expect(page.locator(".documentation-item-settings")).toHaveCount(0);
  await page.locator(".documentation-list-link", { hasText: "Kubernetes documentation" }).click();
  const frame = page.locator("iframe.documentation-frame");
  await expect(frame).toHaveCount(1);
  await expect(frame).toHaveAttribute("src", "https://kubernetes.io/docs/home/");
  await expect(frame).toHaveAttribute("referrerpolicy", "no-referrer");
  await expect(page.locator(".documentation-open-external")).toHaveAttribute("rel", "noopener noreferrer");

  // The embed=false entry renders the fallback card instead of a frame.
  await page.locator(".documentation-list-link", { hasText: "Refused embedding" }).click();
  await expect(page.locator("iframe.documentation-frame")).toHaveCount(0);
  const card = page.locator(".documentation-external-card");
  await expect(card).toHaveAttribute("href", "https://docs.example.internal/registry");
  await expect(card).toHaveAttribute("target", "_blank");
  await expect(card).toHaveAttribute("rel", "noopener noreferrer");

  // A reload restores the selection from the ?doc=<key> query.
  await page.goto(`${apiBase}/documentation?doc=${encodeURIComponent(link.key)}`);
  await expect(page.locator("iframe.documentation-frame")).toHaveAttribute("src", "https://kubernetes.io/docs/home/");

  // Signing the browser in as the bootstrap admin reveals the write surface:
  // the add row and per-entry settings appear.
  await page.evaluate((token) => localStorage.setItem("token", token), token);
  await page.reload();
  await expect(page.locator(".documentation-list-item")).toHaveCount(3); // two entries plus the add row
  await expect(page.locator(".documentation-item-settings")).toHaveCount(2);

  // The admin console renders against the live endpoints, and the overview
  // card shows the playground cap this target pinned (max_active "1").
  await page.getByRole("button", { name: "管理", exact: true }).click();
  await page.getByRole("navigation", { name: "Admin sections" }).getByRole("button", { name: "环境", exact: true }).first().click();
  await expect(page.getByRole("heading", { name: "环境观测" }).first()).toBeVisible();
  await expect(page.locator(".admin-overview-card").first()).toContainText("/ 1", { timeout: 30_000 });
});
