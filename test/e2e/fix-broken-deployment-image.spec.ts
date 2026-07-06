import { expect, test } from '@playwright/test'
import { registerAndLogin, selectFixBrokenDeploymentImage, solveFixBrokenDeploymentImage } from './helpers/ui'

test.describe('fix-broken-deployment-image', () => {
  test('vcluster challenge starts, accepts kubectl input, and passes submit', async ({ page }) => {
    test.setTimeout(300000)

    await registerAndLogin(page)
    await selectFixBrokenDeploymentImage(page)

    await page.locator('main').getByRole('button', { name: 'Start Challenge' }).click()

    await expect(page.locator('.terminal-frame')).toBeVisible({ timeout: 120000 })
    await expect(page.locator('.terminal-overlay')).toBeHidden({ timeout: 120000 })

    await solveFixBrokenDeploymentImage(page)

    await page.locator('.terminal-actions').getByRole('button', { name: 'Submit', exact: true }).click()

    const resultBar = page.locator('.result-bar')
    await expect(resultBar).toBeVisible({ timeout: 60000 })
    await expect(resultBar).toContainText('Verification passed', { timeout: 60000 })
  })
})
