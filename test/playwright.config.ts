import { defineConfig } from "@playwright/test";

export default defineConfig({
  testDir: "./e2e",
  timeout: 30_000,
  outputDir: "./results/browser",
  reporter: [["html", { outputFolder: "./report/browser", open: "never" }]],
  use: {
    baseURL: "http://localhost:9090",
    headless: true,
  },
});
