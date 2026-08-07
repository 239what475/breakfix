import { expect, test } from "@playwright/test";
import { challengeCard } from "../support/live-helpers";
import { nodeRuntimeFixture } from "../support/catalog-fixture";

test("guest can browse the prepared public catalog", async ({ page }) => {
	await page.setViewportSize({ width: 1440, height: 900 });
	await page.goto("/");

	await expect(challengeCard(page, nodeRuntimeFixture.title)).toBeVisible();
	await page.getByRole("textbox", { name: "Search challenges" }).fill(nodeRuntimeFixture.searchTerm);
	await expect(challengeCard(page, nodeRuntimeFixture.title)).toBeVisible();
	await page.getByRole("textbox", { name: "Search challenges" }).fill("not-a-challenge");
	await expect(page.getByText("No challenges match these filters.", { exact: true })).toBeVisible();
	await page.getByRole("textbox", { name: "Search challenges" }).fill("");
	await expect(challengeCard(page, nodeRuntimeFixture.title)).toBeVisible();
	await challengeCard(page, nodeRuntimeFixture.title)
		.getByRole("button", { name: "Start challenge", exact: true })
		.click();
	await expect(page.getByRole("dialog")).toBeVisible();
});

test("guest can open the catalog filters on a narrow viewport", async ({ page }) => {
	await page.setViewportSize({ width: 390, height: 844 });
	await page.goto("/");

	await expect(page.getByRole("button", { name: "Filters", exact: true })).toBeVisible();
	await page.getByRole("button", { name: "Filters", exact: true }).click();
	await expect(page.getByRole("heading", { name: "Find a challenge", exact: true })).toBeVisible();
	await expect(page.getByRole("button", { name: "Start challenge", exact: true })).toHaveCount(0);
});
