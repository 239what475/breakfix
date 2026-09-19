import { expect, test, type Page } from "@playwright/test";

const apiBase = process.env.BREAKFIX_E2E_BASE_URL;
const source = "kubernetes";
const version = "snapshot-ce98a43";
const entryUrl = `/documentation?source=${source}&version=${version}&path=%2Fdocs%2F`;
const podLifecycleUrl = `/documentation?source=${source}&version=${version}&path=%2Fdocs%2Fconcepts%2Fworkloads%2Fpods%2Fpod-lifecycle%2F`;

async function gotoReader(page: Page, readerUrl: string) {
  if (!apiBase) throw new Error("BREAKFIX_E2E_BASE_URL is required for the documentation reader tests");
  await page.goto(`${apiBase}${readerUrl}`);
}

test("guest can read parsed documentation and retain its location", async ({ page }) => {
  // The outline lazily loads each section's children; on a cold server that
  // shares the cluster with the practice chains this walk needs headroom.
  test.setTimeout(120_000);
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
