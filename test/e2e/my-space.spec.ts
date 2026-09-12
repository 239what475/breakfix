import { expect, test } from "@playwright/test";
import {
	scenarioCard,
	registerAndLogin,
} from "../support/live-helpers";
import { nodeRuntimeFixture } from "../support/catalog-fixture";

test("authenticated learner can navigate the responsive My space shell", async ({ page }) => {
	await page.setViewportSize({ width: 1440, height: 900 });
	await registerAndLogin(page);
	await expect(page.getByRole("navigation", { name: "Primary" })).toBeVisible();
	await expect(page.getByRole("button", { name: "Operations", exact: true })).toHaveAttribute("aria-current", "page");
	await page.getByRole("button", { name: "My space", exact: true }).click();
	await expect(page.getByRole("heading", { name: "Your learning space", exact: true })).toBeVisible();
	await expect(page.getByRole("button", { name: "My space", exact: true })).toHaveAttribute("aria-current", "page");
	await expect(page.getByText("Active environments", { exact: true })).toBeVisible();
	await page.getByRole("button", { name: "Learning", exact: true }).first().click();
	await expect(page.getByRole("heading", { name: "Learning history", exact: true })).toBeVisible();
	await page.getByLabel("Filter learning state").selectOption("completed");
	await expect(page.getByLabel("Filter learning state")).toHaveValue("completed");
	await page.getByLabel("Filter learning runtime").selectOption("node");
	await expect(page.getByLabel("Filter learning runtime")).toHaveValue("node");

	await page.setViewportSize({ width: 390, height: 844 });
	await expect(page.getByRole("button", { name: "Overview", exact: true })).toBeVisible();
	await page.getByRole("button", { name: "Navigation", exact: true }).click();
	await expect(page.getByRole("navigation", { name: "Mobile primary" })).toBeVisible();
	await page.getByRole("navigation", { name: "Mobile primary" }).getByRole("button", { name: "Operations", exact: true }).click();
	await expect(scenarioCard(page, nodeRuntimeFixture.title)).toBeVisible();
	await expect(page.getByRole("button", { name: "Start scenario", exact: true })).toHaveCount(0);
});
