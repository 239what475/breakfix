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
    // Real publication chains: the first test publishes the pinned practice
    // and restarts Server and Runtime Worker mid-flight, so the project stays
    // on one worker. Reader navigation, URL/hash sync, retries, and the
    // practice panel behavior live in the vitest component tier.
    { name: "practice-chain", testMatch: /practice\.chain\.spec\.ts$/, workers: 1 },
    // Thin reader smoke: the generated content contract from the external
    // docs-project generator plus the viewport-driven surfaces. It runs after
    // the practice chain to reuse the published state on the pinned page.
    { name: "reader-smoke", testMatch: /reader\.smoke\.spec\.ts$/, dependencies: ["practice-chain"] },
  ],
});
