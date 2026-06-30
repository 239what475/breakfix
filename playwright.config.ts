import { defineConfig } from '@playwright/test';

export default defineConfig({
  testDir: './test/e2e',
  timeout: 30000,
  use: {
    baseURL: 'http://localhost:9090',
    headless: true,
  },
});
