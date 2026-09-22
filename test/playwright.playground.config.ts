import { defineConfig } from "@playwright/test";

// The playground suite drives its own freshly prepared Kind target: the API
// through BREAKFIX_E2E_BASE_URL and the embedded pages served by the same
// Server. It deliberately runs no local web servers. The aggregation smoke
// registers first on purpose — the first account is the bootstrap admin —
// and the playground project depends on it to keep that order deterministic.
export default defineConfig({
  testDir: "./playground",
  timeout: 30_000,
  reporter: "list",
  use: {
    headless: true,
    trace: "retain-on-failure",
  },
  projects: [
    { name: "documentation-smoke", testMatch: /aggregation\.smoke\.spec\.ts$/, workers: 1 },
    // The playground drives one real vk8s environment per user through
    // create, terminal, reset, and close; it stays on one worker because the
    // environment is the user's single session. The reset leg waits for a
    // physical wipe plus rebuild, so the budget covers two full provisions.
    { name: "playground-e2e", testMatch: /playground\.e2e\.spec\.ts$/, workers: 1, timeout: 40 * 60_000, dependencies: ["documentation-smoke"] },
  ],
});
