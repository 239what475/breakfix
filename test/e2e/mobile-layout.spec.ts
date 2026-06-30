import { expect, test } from '@playwright/test'

const BASE = 'http://localhost:9090'

test('single-page workspace remains usable on narrow screens', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 })
  await page.goto(BASE + '/#/')

  await expect(page.locator('text=Breakfix')).toBeVisible()
  await expect(page.locator('text=Terminal Workspace')).toBeVisible()
  await expect(page.getByRole('button', { name: 'Register', exact: true })).toBeVisible()

  await page.screenshot({ path: '/tmp/breakfix-mobile-layout.png', fullPage: true })
})
