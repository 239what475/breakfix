import { defineConfig } from "@playwright/test";

export default defineConfig({
  testDir: "./e2e",
  globalSetup: "./global-setup.ts",
  timeout: 30_000,
	outputDir: "./results/browser",
	reporter: [["html", { outputFolder: "./report/browser", open: "never" }]],
	use: {
		baseURL: process.env.BREAKFIX_E2E_BASE_URL,
		headless: true,
		trace: "retain-on-failure",
		screenshot: "only-on-failure",
		video: "retain-on-failure",
	},
});
