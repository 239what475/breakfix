import { expect, test } from "@playwright/test";

const entryUrl = "/documentation?source=kubernetes&version=snapshot-ce98a43&path=%2Fdocs%2F";

test("guest can read fixture documentation and retain its location", async ({ page }) => {
  await page.goto("/");
  await expect(page.getByRole("button", { name: "Documentation", exact: true })).toBeVisible();
  await page.getByRole("button", { name: "Documentation", exact: true }).click();
  await expect(page).toHaveURL(/\/documentation\?source=kubernetes/);
  const frame = page.frameLocator('iframe[title="Kubernetes documentation"]');
  await expect(frame.getByRole("heading", { name: "Fixture documentation" })).toBeVisible();
  await expect(page.locator(".documentation-location span")).toHaveText("/docs/");

  await frame.getByRole("link", { name: "Open the next page" }).click();
  await expect(frame.getByRole("heading", { name: "Fixture page" })).toBeVisible();
  await expect(page.locator(".documentation-location span")).toHaveText("/docs/page/");
  await expect(page).toHaveURL(/path=%2Fdocs%2Fpage%2F/);

  await frame.locator("body").evaluate(() => window.history.back());
  await expect(frame.getByRole("heading", { name: "Fixture documentation" })).toBeVisible();
  await expect(page.locator(".documentation-location span")).toHaveText("/docs/");

  await frame.getByRole("link", { name: "Jump to topic" }).click();
  await expect(page.locator(".documentation-location span")).toHaveText("/docs/#topic");
  await expect(page).toHaveURL(/hash=topic/);
});

test("documentation reader ignores forged messages", async ({ page }) => {
  await page.goto(entryUrl);
  const frame = page.frameLocator('iframe[title="Kubernetes documentation"]');
  await expect(page.locator(".documentation-location span")).toHaveText("/docs/");
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
  await expect(page.locator(".documentation-location span")).toHaveText("/docs/");

  const sameOriginPopupPromise = page.waitForEvent("popup");
  await page.evaluate(() => window.open("http://localhost:1314/docs/sender/"));
  const sameOriginPopup = await sameOriginPopupPromise;
  await sameOriginPopup.waitForLoadState();
  await expect(page.locator(".documentation-location span")).toHaveText("/docs/");
  await sameOriginPopup.close();

  const otherOriginPopupPromise = page.waitForEvent("popup");
  await page.evaluate(() => window.open("http://localhost:1315/docs/sender/"));
  const otherOriginPopup = await otherOriginPopupPromise;
  await otherOriginPopup.waitForLoadState();
  await expect(page.locator(".documentation-location span")).toHaveText("/docs/");
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
  await expect(page.locator(".documentation-location span")).toHaveText("/docs/");
  await expect(page.frameLocator('iframe[title="Kubernetes documentation"]').getByRole("heading", { name: "Fixture documentation" })).toBeVisible();
});
