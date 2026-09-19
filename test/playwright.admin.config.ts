import { defineConfig } from "@playwright/test";

// The suite drives the prepared Kind target directly: the API through
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
  projects: [
    // Elects (or reuses, on an already-prepared target) the bootstrap admin
    // and persists the session file every later project reads.
    { name: "admin-setup", testMatch: /admin\.setup\.ts/, timeout: 120_000 },
    // Console-only coverage: no workflow chains, iterates in seconds, and is
    // safe to parallelize and to re-run on a used target.
    { name: "admin-fast", testMatch: /\.fast\.spec\.ts$/, dependencies: ["admin-setup"] },
    // Real publication chains against the single shared cluster: one worker,
    // and each file owns a distinct (page, anchor) pair so any chain subset
    // can be grepped on a freshly reset target.
    { name: "admin-chain", testMatch: /\.chain\.spec\.ts$/, dependencies: ["admin-setup"], workers: 1 },
  ],
});
