import { expect, test } from '@playwright/test'

const BASE = 'http://localhost:9090'

async function totpCode(page: any, secret: string): Promise<string> {
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

test('single-page workspace loads for guests', async ({ page }) => {
  await page.goto(BASE + '/')
  await expect(page.locator('text=Breakfix')).toBeVisible()
  await expect(page.locator('text=SRE terminal labs')).toBeVisible()
  await expect(page.locator('text=Terminal Workspace')).toBeVisible()
  await expect(page.locator('text=Pick a challenge from the left')).toBeVisible()
  await expect(page.getByRole('button', { name: 'Sign In', exact: true })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Register', exact: true })).toBeVisible()
})

test('register and login into the single-page workspace', async ({ page }) => {
  const user = `e2e-${Date.now()}`

  await page.goto(BASE + '/')
  await page.getByRole('button', { name: 'Register', exact: true }).click()
  await expect(page.locator('h3:has-text("Create Account")')).toBeVisible()

  await page.locator('input[placeholder="Username"]').first().fill(user)
  await page.locator('input[placeholder="Password (min 6 chars)"]').fill('testpass123')
  await page.getByRole('button', { name: 'Register' }).last().click()

  await expect(page.locator('text=TOTP Secret')).toBeVisible({ timeout: 10000 })
  const secret = (await page.locator('.totp-secret code').textContent()) ?? ''
  await page.getByRole('button', { name: 'Continue to Sign In' }).click()

  const code = await totpCode(page, secret)
  await page.locator('input[placeholder="Username"]').first().fill(user)
  await page.locator('input[placeholder="Password"]').first().fill('testpass123')
  await page.locator('input[placeholder="TOTP Code"]').fill(code)
  await page.getByRole('button', { name: 'Sign In' }).last().click()

  await expect(page.locator('.account-value').getByText('Authenticated')).toBeVisible({ timeout: 10000 })
  await expect(page.locator('.terminal-empty-card .brief-kicker')).toHaveText('Challenge brief')
})
