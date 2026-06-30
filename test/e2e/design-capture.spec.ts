import { test } from '@playwright/test'

const RUN_EXTERNAL_CAPTURE = process.env.RUN_EXTERNAL_CAPTURE === '1'

const captureTest = RUN_EXTERNAL_CAPTURE ? test : test.skip

const sites = [
  { name: 'killercoda', url: 'https://killercoda.com/playgrounds/scenario/ubuntu' },
  { name: 'linear', url: 'https://linear.app/features' },
  { name: 'github', url: 'https://github.com' },
  { name: 'replit', url: 'https://replit.com' },
]

for (const site of sites) {
  captureTest(`capture ${site.name}`, async ({ page }) => {
    test.setTimeout(60000)
    await page.setViewportSize({ width: 1440, height: 900 })
    await page.goto(site.url, { waitUntil: 'networkidle', timeout: 30000 })
    await page.waitForTimeout(3000)
    await page.screenshot({ path: `/tmp/design-${site.name}.png`, fullPage: false })
  })
}
