import { expect, test } from '@playwright/test'
import { registerAndLogin } from './helpers/ui'

test.describe('fix-broken-deployment-image', () => {
  test('vcluster challenge starts, accepts kubectl input, and passes submit', async ({ page }) => {
    test.setTimeout(300000)

    await registerAndLogin(page)

    const challengeButton = page.getByRole('button', { name: /修复错误的 Deployment 镜像/ })
    await expect(challengeButton).toBeVisible({ timeout: 10000 })
    await challengeButton.click()

    await expect(page.locator('.terminal-empty-card h2')).toContainText('修复错误的 Deployment 镜像')
    await expect(page.locator('.terminal-empty-card .brief-description')).toContainText('default')

    await page.locator('main').getByRole('button', { name: 'Start Challenge' }).click()

    await expect(page.locator('.terminal-frame')).toBeVisible({ timeout: 120000 })
    await expect(page.locator('.terminal-overlay')).toBeHidden({ timeout: 120000 })

    const terminalSurface = page.locator('.terminal-surface')
    await terminalSurface.click({ position: { x: 140, y: 120 } })

    await page.keyboard.type('kubectl get deployment web -n default', { delay: 20 })
    await page.keyboard.press('Enter')
    await expect(page.locator('.terminal-frame')).toContainText('web', { timeout: 15000 })

    await page.keyboard.type('kubectl set image deployment/web web=nginx:1.25.5 -n default', { delay: 20 })
    await page.keyboard.press('Enter')
    await page.keyboard.type('kubectl rollout status deployment/web -n default --timeout=120s', { delay: 20 })
    await page.keyboard.press('Enter')
    await expect(page.locator('.terminal-frame')).toContainText('successfully rolled out', { timeout: 120000 })

    await page.locator('.terminal-actions').getByRole('button', { name: 'Submit', exact: true }).click()

    const resultBar = page.locator('.result-bar')
    await expect(resultBar).toBeVisible({ timeout: 60000 })
    await expect(resultBar).toContainText('Verification passed', { timeout: 60000 })
  })
})
