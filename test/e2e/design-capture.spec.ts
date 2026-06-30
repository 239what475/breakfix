import { test } from '@playwright/test';
import path from 'path';

const sites = [
  { name: 'killercoda', url: 'https://killercoda.com/playgrounds/scenario/ubuntu' },
  { name: 'linear', url: 'https://linear.app/features' },
  { name: 'github', url: 'https://github.com' },
  { name: 'replit', url: 'https://replit.com' },
];

for (const site of sites) {
  test(`capture ${site.name}`, async ({ page }) => {
    test.setTimeout(60000);
    await page.setViewportSize({ width: 1440, height: 900 });
    await page.goto(site.url, { waitUntil: 'networkidle', timeout: 30000 });
    await page.waitForTimeout(3000);
    await page.screenshot({ path: `/tmp/design-${site.name}.png`, fullPage: false });
  });
}
