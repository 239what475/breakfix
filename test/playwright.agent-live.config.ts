import { defineConfig } from "@playwright/test";

export default defineConfig({
  testDir: "./agent",
  globalSetup: "./global-setup.ts",
  timeout: 30_000,
  workers: 1,
  outputDir: "./results/agent-live",
  reporter: [["html", { outputFolder: "./report/agent-live", open: "never" }]],
  use: {
    baseURL: process.env.BREAKFIX_E2E_BASE_URL ?? "http://localhost:9090",
    headless: true,
  },
});
