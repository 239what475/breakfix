import { defineConfig } from '@playwright/test';

export default defineConfig({
  testDir: './test/e2e',
  timeout: 30000,
  outputDir: './test-results',
  reporter: [['html', { outputFolder: './playwright-report', open: 'never' }]],
  use: {
    baseURL: 'http://localhost:9090',
    headless: true,
  },
});
