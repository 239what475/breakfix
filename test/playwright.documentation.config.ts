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
    // Pure reader rendering: no practice chains, safe to re-run in seconds
    // against an already prepared target.
    { name: "reader-fast", testMatch: /reader\.fast\.spec\.ts$/ },
    // Real publication chains: the first test publishes the pinned practice
    // and restarts Server and Runtime Worker mid-flight, so the project stays
    // on one worker and runs only after the reader tests finish - a parallel
    // reader walk would hit the restart window and lose its tree fetches.
    { name: "practice-chain", testMatch: /practice\.chain\.spec\.ts$/, workers: 1, dependencies: ["reader-fast"] },
  ],
});
