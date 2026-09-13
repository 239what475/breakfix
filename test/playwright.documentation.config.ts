import { defineConfig } from "@playwright/test";

export default defineConfig({
  testDir: "./documentation",
  timeout: 30_000,
  reporter: "list",
  use: {
    baseURL: "http://localhost:5173",
    headless: true,
    trace: "retain-on-failure",
  },
  webServer: [
    {
      command: "VITE_DOCS_ORIGIN=http://localhost:1314 npm run dev --prefix ../web -- --host 127.0.0.1",
      url: "http://localhost:5173",
      reuseExistingServer: true,
    },
    {
      command: "node ./fixtures/documentation/server.mjs",
      url: "http://localhost:1314/docs/",
      reuseExistingServer: true,
    },
    {
      command: "DOCUMENTATION_FIXTURE_PORT=1315 node ./fixtures/documentation/server.mjs",
      url: "http://localhost:1315/docs/",
      reuseExistingServer: true,
    },
  ],
});
