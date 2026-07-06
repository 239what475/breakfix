import { expect, type Page } from '@playwright/test'

export const BASE = 'http://localhost:9090'

export function uniqueUser(prefix = 'e2e') {
  return `${prefix}-${Date.now()}-${Math.random().toString(36).slice(2, 6)}`
}

export async function totpCode(page: Page, secret: string): Promise<string> {
  return page.evaluate(async (sec: string) => {
    const alphabet = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ234567'
    const result: number[] = []
    let bits = 0
    let value = 0
    for (let i = 0; i < sec.length; i++) {
      value = (value << 5) | alphabet.indexOf(sec[i].toUpperCase())
      bits += 5
      if (bits >= 8) {
        result.push((value >>> (bits - 8)) & 0xff)
        bits -= 8
      }
    }
    const key = new Uint8Array(result)
    const counter = Math.floor(Date.now() / 1000 / 30)
    const buf = new ArrayBuffer(8)
    const dv = new DataView(buf)
    dv.setBigUint64(0, BigInt(counter), false)
    const keyBuf = await crypto.subtle.importKey('raw', key, { name: 'HMAC', hash: 'SHA-1' }, false, ['sign'])
    const sig = await crypto.subtle.sign('HMAC', keyBuf, buf)
    const hash = new Uint8Array(sig)
    const offset = hash[hash.length - 1] & 0xf
    const code = (
      ((hash[offset] & 0x7f) << 24) |
      (hash[offset + 1] << 16) |
      (hash[offset + 2] << 8) |
      hash[offset + 3]
    ) % 1000000
    return String(code).padStart(6, '0')
  }, secret)
}

export async function registerAndLogin(page: Page, user = uniqueUser()): Promise<{ user: string; secret: string }> {
  await page.goto(BASE + '/')
  await page.getByRole('button', { name: 'Register', exact: true }).click()
  await expect(page.locator('h3:has-text("Create Account")')).toBeVisible()

  await page.locator('input[placeholder="Username"]').first().fill(user)
  await page.locator('input[placeholder="Password (min 6 chars)"]').fill('testpass123')
  await page.getByRole('button', { name: 'Register' }).last().click()

  await expect(page.locator('.totp-secret code')).toBeVisible({ timeout: 10000 })
  const secret = (await page.locator('.totp-secret code').textContent()) ?? ''
  await page.getByRole('button', { name: 'Continue to Sign In' }).click()

  const code = await totpCode(page, secret)
  await page.locator('input[placeholder="Username"]').first().fill(user)
  await page.locator('input[placeholder="Password"]').first().fill('testpass123')
  await page.locator('input[placeholder="TOTP Code"]').fill(code)
  await page.getByRole('button', { name: 'Sign In' }).last().click()

  await expect(page.locator('.account-value').getByText('Authenticated')).toBeVisible({ timeout: 10000 })
  await expect(page.locator('.terminal-empty-card')).toBeVisible()

  return { user, secret }
}

export async function selectCleanupLogs(page: Page) {
  const challengeButton = page.getByRole('button', { name: /批量压缩旧日志/ })
  await expect(challengeButton).toBeVisible({ timeout: 10000 })
  await challengeButton.click()
  await expect(page.locator('.terminal-empty-card h2')).toContainText('批量压缩旧日志')
  await expect(page.locator('.terminal-empty-card .brief-description')).toContainText('服务器磁盘空间不足')
}

export async function selectFixBrokenDeploymentImage(page: Page) {
  const challengeButton = page.getByRole('button', { name: /修复错误的 Deployment 镜像/ })
  await expect(challengeButton).toBeVisible({ timeout: 10000 })
  await challengeButton.click()
  await expect(page.locator('.terminal-empty-card h2')).toContainText('修复错误的 Deployment 镜像')
  await expect(page.locator('.terminal-empty-card .brief-description')).toContainText('default')
}

export async function solveFixBrokenDeploymentImage(page: Page) {
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
}

export async function signOut(page: Page) {
  await page.locator('.account-avatar').click()
  await page.getByText('Sign Out', { exact: true }).click()
  await expect(page.locator('text=Signed out')).toBeVisible({ timeout: 5000 })
}
