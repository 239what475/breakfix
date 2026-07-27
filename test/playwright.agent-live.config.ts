import { defineConfig } from "@playwright/test";

export default defineConfig({
  testDir: "./agent",
  timeout: 30_000,
  workers: 1,
  outputDir: "./results/agent-live",
  reporter: [["html", { outputFolder: "./report/agent-live", open: "never" }]],
  use: {
    baseURL: "http://localhost:9090",
    headless: true,
  },
});
