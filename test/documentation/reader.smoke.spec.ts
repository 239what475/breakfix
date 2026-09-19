import { expect, test, type Page } from "@playwright/test";

const apiBase = process.env.BREAKFIX_E2E_BASE_URL;
const source = "kubernetes";
const version = "snapshot-ce98a43";
const entryUrl = `/documentation?source=${source}&version=${version}&path=%2Fdocs%2F`;
const podLifecycleUrl = `/documentation?source=${source}&version=${version}&path=%2Fdocs%2Fconcepts%2Fworkloads%2Fpods%2Fpod-lifecycle%2F`;

async function gotoReader(page: Page, readerUrl: string) {
  if (!apiBase) throw new Error("BREAKFIX_E2E_BASE_URL is required for the documentation reader smoke");
  await page.goto(`${apiBase}${readerUrl}`);
}

// Thin smoke of what only a real browser can carry: the generated content
// contract produced by the external docs-project generator, the
// viewport-driven outline drawer, and the practice entry's presence on the
// pinned page. Navigation, URL/hash sync, retries, and fallbacks live in the
// vitest reader tier; this project runs after the practice chain so the
// published practice exists.
test("the reader renders the generated corpus and gates the practice entry by viewport", async ({ page }) => {
  test.setTimeout(3 * 60_000);

  // Desktop: walk the outline to the pinned practice page. The heading
  // anchors, alert blockquotes, and shiki code blocks come from the offline
  // generator - this repository has no lower tier that could hold them.
  await gotoReader(page, entryUrl);
  await page.getByRole("button", { name: "Toggle Concepts section" }).click();
  await page.getByRole("button", { name: "Toggle Workloads section" }).click();
  await page.getByRole("button", { name: "Toggle Pods section" }).click();
  await page.getByRole("button", { name: "Pod Lifecycle", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Pod Lifecycle", exact: true })).toBeVisible();
  const article = page.locator(".documentation-article");
  await expect(article.locator("h2#pod-lifetime")).toBeVisible();
  await expect(article.locator("blockquote.doc-alert").first()).toBeVisible();
  await expect(article.locator("blockquote.doc-alert-note").first()).toBeVisible();
  await expect(article.locator("blockquote.doc-alert-caution").first()).toBeVisible();
  await expect(article.locator("pre.shiki").first()).toBeVisible();
  // Exactly the published anchor carries the practice entry.
  await expect(article.locator("h2#pod-lifetime .practice-anchor-button")).toBeVisible();
  await expect(article.locator(".practice-anchor-button")).toHaveCount(1);

  // Mobile viewport: the entry disappears and the outline moves into the
  // menu drawer.
  await page.setViewportSize({ width: 390, height: 844 });
  await page.reload();
  await expect(page.locator(".documentation-article")).toBeVisible();
  await expect(page.locator(".practice-anchor-button")).toBeHidden();
  await expect(page.locator(".practice-panel")).toHaveCount(0);
  await page.getByRole("button", { name: "Contents", exact: true }).click();
  await expect(page.getByRole("navigation", { name: "Documentation outline" })).toBeVisible();
  await page.getByRole("button", { name: "Close documentation outline" }).click();
  await expect(page.getByRole("navigation", { name: "Documentation outline" })).not.toBeVisible();

  // The app shell's mobile menu still routes into the reader.
  await page.goto(`${apiBase}/`);
  await page.getByRole("button", { name: "Navigation", exact: true }).click();
  await expect(page.getByRole("navigation", { name: "Mobile primary" }).getByRole("button", { name: "Documentation", exact: true })).toBeVisible();
});
