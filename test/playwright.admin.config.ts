import { defineConfig } from "@playwright/test";

// The admin suite drives the prepared Kind target directly: the API through
// BREAKFIX_E2E_BASE_URL and the embedded console served by the same Server.
// It deliberately runs no local web servers.
export default defineConfig({
  testDir: "./admin",
  timeout: 30_000,
  reporter: "list",
  use: {
    headless: true,
    trace: "retain-on-failure",
  },
});
