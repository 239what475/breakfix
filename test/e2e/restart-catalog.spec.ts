import { expect, test } from '@playwright/test'
import { BASE, registerAndLogin, uniqueUser } from './helpers/ui'

test.describe('Challenge Catalog Persistence', () => {
  test('preloaded challenges remain visible across reload', async ({ page }) => {
    await registerAndLogin(page, uniqueUser('restart-catalog'))
    await page.goto(BASE + '/')
    await expect(page.getByRole('button', { name: /批量压缩旧日志/ })).toBeVisible()

    await page.reload()

    await expect(page.getByRole('button', { name: /批量压缩旧日志/ })).toBeVisible()
  })
})
