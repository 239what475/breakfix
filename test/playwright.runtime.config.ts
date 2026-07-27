import { defineConfig } from "@playwright/test";

export default defineConfig({
  testDir: "./runtime",
  timeout: 30_000,
  workers: 1,
  outputDir: "./results/runtime",
  reporter: [["html", { outputFolder: "./report/runtime", open: "never" }]],
  use: {
    baseURL: process.env.BREAKFIX_E2E_BASE_URL ?? "http://localhost:9090",
    headless: true,
  },
});
