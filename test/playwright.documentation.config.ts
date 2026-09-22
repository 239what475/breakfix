import { defineConfig } from "@playwright/test";

// The documentation suite drives the prepared Kind target directly: the API
// through BREAKFIX_E2E_BASE_URL and the embedded pages served by the same
// Server. It deliberately runs no local web servers.
export default defineConfig({
  testDir: "./documentation",
  timeout: 30_000,
  reporter: "list",
  use: {
    headless: true,
    trace: "retain-on-failure",
  },
  projects: [
    // The playground drives one real vk8s environment per user through
    // create, terminal, reset, and close; it stays on one worker because the
    // environment is the user's single session. The reset leg waits for a
    // physical wipe plus rebuild, so the budget covers two full provisions.
    { name: "playground-e2e", testMatch: /playground\.e2e\.spec\.ts$/, workers: 1, timeout: 40 * 60_000 },
  ],
});
