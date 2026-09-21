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
  projects: [
    // The blank scenario drives one real vk8s environment per reader through
    // create, terminal, reset, and close; it stays on one worker because the
    // environment is the user's single session.
    { name: "scenario-e2e", testMatch: /scenario\.e2e\.spec\.ts$/, workers: 1, timeout: 30 * 60_000 },
    // Thin reader smoke: the generated content contract from the external
    // docs-project generator plus the viewport-driven surfaces.
    { name: "reader-smoke", testMatch: /reader\.smoke\.spec\.ts$/ },
  ],
});
