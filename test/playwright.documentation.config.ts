import { defineConfig } from "@playwright/test";

// The documentation suite drives the prepared Kind target directly: the API
// through BREAKFIX_E2E_BASE_URL and the embedded reader served by the same
// Server. It deliberately runs no local web servers — the reader renders
// parsed library pages fetched from the Server itself.
export default defineConfig({
  testDir: "./documentation",
  timeout: 30_000,
  reporter: "list",
  use: {
    headless: true,
    trace: "retain-on-failure",
  },
});
