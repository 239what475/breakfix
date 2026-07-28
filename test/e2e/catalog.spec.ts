import { expect, test } from "@playwright/test";
import {
	challengeCard,
	expectViewportWithoutPageOverflow,
} from "../support/live-helpers";

test("guest can filter, sort, and browse the public catalog without page overflow", async ({
	page,
}) => {
	await page.setViewportSize({ width: 1440, height: 900 });
	const contentRequests: string[] = [];
	page.on("request", (request) => {
		if (request.url().includes("/content")) contentRequests.push(request.url());
	});
	await page.goto("/");

	await expect(challengeCard(page, "批量压缩旧日志")).toBeVisible();
	expect(contentRequests).toEqual([]);

	await page.getByRole("textbox", { name: "Search challenges" }).fill("日志");
	await expect(challengeCard(page, "批量压缩旧日志")).toBeVisible();
	await page.getByRole("textbox", { name: "Search challenges" }).fill("not-a-challenge");
	await expect(page.getByText("No challenges match these filters.", { exact: true })).toBeVisible();

	await page.getByRole("textbox", { name: "Search challenges" }).fill("");
	const catalog = await page.evaluate(async () => {
		const response = await fetch("/api/challenges");
		if (!response.ok) throw new Error(await response.text());
		return response.json() as Promise<{
			challenges: Array<{ id: string; tags: string[]; title: string; published_at: string }>;
		}>;
	});
	const challenge = catalog.challenges.find((entry) => entry.id === "chal-r7m4x2q9v6kp");
	expect(challenge?.tags.length).toBeGreaterThan(0);
	const taxonomyTag = challenge?.tags[0] ?? "";
	await page.locator(".catalog-filters").getByLabel(taxonomyTag, { exact: true }).check();
	await expect(challengeCard(page, "批量压缩旧日志")).toBeVisible();
	await page.locator(".catalog-filters").getByLabel("VCluster", { exact: true }).check();
	await expect(page.getByText("No challenges match these filters.", { exact: true })).toBeVisible();
	await page.getByRole("button", { name: "Reset filters", exact: true }).click();

	await expect(challengeCard(page, "批量压缩旧日志")).toBeVisible();
	await page.getByLabel("Sort challenges").selectOption("oldest");
	const oldestFirst = [...catalog.challenges]
		.sort((left, right) => new Date(left.published_at).getTime() - new Date(right.published_at).getTime())
		.map((entry) => entry.title);
	await expect(page.locator("article.challenge-card h2").allTextContents()).resolves.toEqual(oldestFirst);
	await page.getByLabel("Sort challenges").selectOption("newest");
	await expect(page.locator("article.challenge-card h2").allTextContents()).resolves.toEqual([...oldestFirst].reverse());

	await challengeCard(page, "批量压缩旧日志")
		.getByRole("button", { name: "Start challenge", exact: true })
		.click();
	await expect(page.getByRole("dialog")).toBeVisible();
	await expectViewportWithoutPageOverflow(page);
});

test("narrow catalog has no overflow", async ({ page }) => {
	await page.setViewportSize({ width: 390, height: 844 });
	await page.goto("/");

	await expect(page.getByRole("button", { name: "Filters", exact: true })).toBeVisible();
	await page.getByRole("button", { name: "Filters", exact: true }).click();
	await expect(page.getByRole("heading", { name: "Find a challenge", exact: true })).toBeVisible();
	await expect(page.getByRole("button", { name: "Start challenge", exact: true })).toHaveCount(0);
	await expectViewportWithoutPageOverflow(page);
});
