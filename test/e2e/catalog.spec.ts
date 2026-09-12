import { expect, test } from "@playwright/test";
import { scenarioCard } from "../support/live-helpers";
import { nodeRuntimeFixture } from "../support/catalog-fixture";

test("guest can browse the prepared public catalog", async ({ page }) => {
	await page.setViewportSize({ width: 1440, height: 900 });
	await page.goto("/");

	await expect(scenarioCard(page, nodeRuntimeFixture.title)).toBeVisible();
	await page.getByRole("textbox", { name: "Search scenarios" }).fill(nodeRuntimeFixture.searchTerm);
	await expect(scenarioCard(page, nodeRuntimeFixture.title)).toBeVisible();
	await page.getByRole("textbox", { name: "Search scenarios" }).fill("not-a-scenario");
	await expect(page.getByText("No scenarios match these filters.", { exact: true })).toBeVisible();
	await page.getByRole("textbox", { name: "Search scenarios" }).fill("");
	await expect(scenarioCard(page, nodeRuntimeFixture.title)).toBeVisible();
	await scenarioCard(page, nodeRuntimeFixture.title)
		.getByRole("button", { name: "Start scenario", exact: true })
		.click();
	await expect(page.getByRole("dialog")).toBeVisible();
});

test("guest can open the catalog filters on a narrow viewport", async ({ page }) => {
	await page.setViewportSize({ width: 390, height: 844 });
	await page.goto("/");

	await expect(page.getByRole("button", { name: "Filters", exact: true })).toBeVisible();
	await page.getByRole("button", { name: "Filters", exact: true }).click();
	await expect(page.getByRole("heading", { name: "Find a scenario", exact: true })).toBeVisible();
	await expect(page.getByRole("button", { name: "Start scenario", exact: true })).toHaveCount(0);
});
