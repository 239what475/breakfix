import { expect, test } from "@playwright/test";
import {
	captureWorkspace,
	challengeCard,
	expectElementsWithinViewport,
	expectTerminalConnected,
	expectViewportWithoutPageOverflow,
	registerAndLogin,
} from "../support/live-helpers";

test("authenticated learner can navigate the responsive My space shell", async ({ page }) => {
	await page.setViewportSize({ width: 1440, height: 900 });
	await registerAndLogin(page);
	await expect(page.locator(".app-topbar:visible")).toHaveCount(1);
	await expect(page.getByRole("navigation", { name: "Primary" })).toBeVisible();
	await expect(page.getByRole("button", { name: "Catalog", exact: true })).toHaveAttribute("aria-current", "page");
	await expect(page.getByRole("button", { name: "Challenge studio", exact: true })).toBeVisible();
	await captureWorkspace(page, "catalog-authenticated-desktop");
	await page.getByRole("button", { name: "My space", exact: true }).click();
	await expect(page.getByRole("heading", { name: "Your learning space", exact: true })).toBeVisible();
	await expect(page.getByRole("button", { name: "My space", exact: true })).toHaveAttribute("aria-current", "page");
	await expect(page.getByText("Active environments", { exact: true })).toBeVisible();
	await page.getByRole("button", { name: "Learning", exact: true }).first().click();
	await expect(page.getByRole("heading", { name: "Learning history", exact: true })).toBeVisible();
	const stateRequest = page.waitForRequest((request) => {
		const url = new URL(request.url());
		return url.pathname === "/api/me/space/learning" && url.searchParams.get("state") === "completed";
	});
	await page.getByLabel("Filter learning state").selectOption("completed");
	await stateRequest;
	await expect(page.getByLabel("Filter learning state")).toHaveValue("completed");
	const runtimeRequest = page.waitForRequest((request) => {
		const url = new URL(request.url());
		return url.pathname === "/api/me/space/learning" && url.searchParams.get("runtime") === "container";
	});
	await page.getByLabel("Filter learning runtime").selectOption("container");
	await runtimeRequest;
	await expect(page.getByLabel("Filter learning runtime")).toHaveValue("container");
	await page.getByRole("button", { name: "Authoring", exact: true }).first().click();
	await expect(page.getByRole("heading", { name: "Challenge authoring", exact: true })).toBeVisible();
	await page.getByRole("button", { name: "Open studio", exact: true }).click();
	await expect(page.getByLabel("Challenge authoring workspace")).toBeVisible();
	await expect(page.locator(".app-topbar:visible")).toBeVisible();
	await expect(page.getByRole("navigation", { name: "Primary" })).toBeVisible();
	await expect(page.getByRole("button", { name: "Challenge studio", exact: true })).toHaveAttribute("aria-current", "page");
	await captureWorkspace(page, "authoring-shell-desktop");
	await page.getByRole("button", { name: "My space", exact: true }).click();
	await expect(page.getByRole("heading", { name: "Challenge authoring", exact: true })).toBeVisible();
	await expect(page.locator(".authoring-draft")).toHaveCount(1);
	await page.getByRole("button", { name: "Challenge studio", exact: true }).click();
	await expect(page.getByLabel("Challenge authoring workspace")).toBeVisible();
	await page.getByRole("button", { name: "My space", exact: true }).click();
	await expect(page.getByRole("heading", { name: "Challenge authoring", exact: true })).toBeVisible();
	const draftRequest = page.waitForRequest((request) => {
		const url = new URL(request.url());
		return request.method() === "GET" && /^\/api\/authoring\/sessions\/[^/]+$/.test(url.pathname);
	});
	await page.locator(".authoring-draft").click();
	await draftRequest;
	await expect(page.getByLabel("Challenge authoring workspace")).toBeVisible();
	await page.getByRole("button", { name: "My space", exact: true }).click();
	await expect(page.getByRole("heading", { name: "Challenge authoring", exact: true })).toBeVisible();
	await expectViewportWithoutPageOverflow(page);
	await expectElementsWithinViewport(page, ".app-topbar:visible, .my-space-layout");
	await captureWorkspace(page, "my-space-desktop");

	await page.setViewportSize({ width: 390, height: 844 });
	await expect(page.getByRole("button", { name: "Overview", exact: true })).toBeVisible();
	await expect(page.getByRole("button", { name: "Authoring", exact: true })).toHaveCount(0);
	await page.getByRole("button", { name: "Navigation", exact: true }).click();
	await expect(page.getByRole("navigation", { name: "Mobile primary" })).toBeVisible();
	await expect(page.getByRole("button", { name: "Challenge studio", exact: true })).toHaveCount(0);
	await captureWorkspace(page, "my-space-mobile-menu");
	await page.getByRole("navigation", { name: "Mobile primary" }).getByRole("button", { name: "Catalog", exact: true }).click();
	await expect(challengeCard(page, "批量压缩旧日志")).toBeVisible();
	await expect(page.getByRole("button", { name: "Start challenge", exact: true })).toHaveCount(0);
	await expectViewportWithoutPageOverflow(page);
	await captureWorkspace(page, "catalog-mobile");
});
