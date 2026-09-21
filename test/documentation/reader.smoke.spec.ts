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
// contract produced by the external docs-project generator and the
// viewport-driven outline drawer. Navigation, URL/hash sync, retries, and the
// playground session behavior live in the vitest reader tier and the
// playground e2e suite.
test("the reader renders the generated corpus and moves the outline by viewport", async ({ page }) => {
  test.setTimeout(3 * 60_000);

  // Desktop: walk the outline to a pinned page. The heading anchors, alert
  // blockquotes, and shiki code blocks come from the offline generator -
  // this repository has no lower tier that could hold them.
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
  // The reader carries no scenario surfaces anymore; an anonymous visitor
  // gets no playground ball and no request fires.
  await expect(page.locator("button.playground-fab")).toHaveCount(0);

  // Mobile viewport: the outline moves into the menu drawer; the ball stays
  // absent for the anonymous visitor.
  await page.setViewportSize({ width: 390, height: 844 });
  await page.reload();
  await expect(page.locator(".documentation-article")).toBeVisible();
  await expect(page.locator("button.playground-fab")).toHaveCount(0);
  await page.getByRole("button", { name: "Contents", exact: true }).click();
  await expect(page.getByRole("navigation", { name: "Documentation outline" })).toBeVisible();
  await page.getByRole("button", { name: "Close documentation outline", exact: true }).click();
  await expect(page.getByRole("navigation", { name: "Documentation outline" })).not.toBeVisible();

  // The app shell's mobile menu still routes into the reader.
  await page.goto(`${apiBase}/`);
  await page.getByRole("button", { name: "Navigation", exact: true }).click();
  await expect(page.getByRole("navigation", { name: "Mobile primary" }).getByRole("button", { name: "Documentation", exact: true })).toBeVisible();

  // Deep links restore the pinned page directly.
  await gotoReader(page, podLifecycleUrl);
  await expect(page.locator(".documentation-article").locator("h2#pod-lifetime")).toBeVisible();
});
