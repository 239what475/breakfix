import { defineConfig } from "@playwright/test";

export default defineConfig({
	testDir: "./recovery",
	globalSetup: "./global-setup.ts",
	timeout: 30_000,
	workers: 1,
	outputDir: "./results/recovery",
	reporter: [["html", { outputFolder: "./report/recovery", open: "never" }]],
	use: {
		baseURL: process.env.BREAKFIX_E2E_BASE_URL,
		headless: true,
		trace: "retain-on-failure",
		screenshot: "only-on-failure",
		video: "retain-on-failure",
	},
});
