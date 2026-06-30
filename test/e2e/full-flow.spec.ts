import { expect, test } from '@playwright/test'
import { BASE, registerAndLogin, selectCleanupLogs, uniqueUser } from './helpers/ui'

test.describe('Full Challenge Flow via UI', () => {
  test('register → login → start challenge → terminal → submit', async ({ page }) => {
    test.setTimeout(180000)

    await registerAndLogin(page, uniqueUser('full-flow'))
    await selectCleanupLogs(page)

    await page.locator('main').getByRole('button', { name: 'Start Challenge' }).click()

    await expect(page.locator('.terminal-frame')).toBeVisible({ timeout: 90000 })
    await expect(page.locator('.terminal-overlay')).toBeHidden({ timeout: 90000 })

    const terminalSurface = page.locator('.terminal-surface')
    await terminalSurface.click({ position: { x: 120, y: 120 } })
    await page.keyboard.type('echo full-flow-terminal-check', { delay: 20 })
    await page.keyboard.press('Enter')
    await expect(page.locator('.terminal-frame')).toContainText('full-flow-terminal-check', { timeout: 10000 })

    await page.getByRole('button', { name: 'Submit' }).last().click()

    const resultBar = page.locator('.result-bar')
    await expect(resultBar).toBeVisible({ timeout: 30000 })
    await expect(resultBar).toContainText(/Verification passed|Verification failed/)

    await page.goto(BASE + '/#/')
    await expect(page.locator('.account-value').getByText('Authenticated')).toBeVisible()
  })
})
