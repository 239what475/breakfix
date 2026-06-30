import { test, expect } from '@playwright/test';

const BASE = 'http://localhost:9090';

test('main page loads with header and welcome', async ({ page }) => {
  await page.goto(BASE + '/#/');
  await expect(page.locator('text=● Breakfix')).toBeVisible();
  await expect(page.locator('text=SRE Practice')).toBeVisible();
  await expect(page.locator('text=Select a challenge to begin')).toBeVisible();
});

test('header has Sign In and Register when not logged in', async ({ page }) => {
  await page.goto(BASE + '/#/');
  // Header buttons — use getByRole for specificity
  await expect(page.getByRole('button', { name: 'Sign In', exact: true })).toBeVisible();
  await expect(page.getByRole('button', { name: 'Register', exact: true })).toBeVisible();
});

test('header Sign In opens modal', async ({ page }) => {
  await page.goto(BASE + '/#/');
  await page.getByRole('button', { name: 'Sign In', exact: true }).click();
  await expect(page.locator('h3:has-text("Sign In")')).toBeVisible();
});

test('register and login flow', async ({ page }) => {
  const user = 'e2e-' + Date.now();
  await page.goto(BASE + '/#/');

  // Open register
  await page.getByRole('button', { name: 'Register', exact: true }).click();
  await page.locator('h3:has-text("Create Account")').waitFor();
  await page.locator('input[placeholder="Username"]').first().fill(user);
  await page.locator('input[placeholder="Password (min 6 chars)"]').fill('testpass123');
  await page.locator('.n-modal button:has-text("Register")').click();

  // Wait for TOTP secret
  await page.locator('text=TOTP Secret').waitFor({ timeout: 10000 });
  const secretEl = page.locator('text="TOTP Secret"').locator('..').locator('div[style*="monospace"]');
  const secret = await secretEl.textContent();
  await page.locator('text=Continue to Sign In').click();

  // Generate TOTP
  const code = await page.evaluate(async (sec: string) => {
    const alphabet = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ234567';
    const result: number[] = [];
    let bits = 0, value = 0;
    for (let i = 0; i < sec.length; i++) {
      value = (value << 5) | alphabet.indexOf(sec[i].toUpperCase());
      bits += 5;
      if (bits >= 8) { result.push((value >>> (bits - 8)) & 0xff); bits -= 8; }
    }
    const key = new Uint8Array(result);
    const counter = Math.floor(Date.now() / 1000 / 30);
    const buf = new ArrayBuffer(8); const dv = new DataView(buf);
    dv.setBigUint64(0, BigInt(counter), false);
    const keyBuf = await crypto.subtle.importKey('raw', key, { name: 'HMAC', hash: 'SHA-1' }, false, ['sign']);
    const sig = await crypto.subtle.sign('HMAC', keyBuf, buf);
    const hash = new Uint8Array(sig);
    const offset = hash[hash.length - 1] & 0xf;
    const c = ((hash[offset] & 0x7f) << 24 | hash[offset + 1] << 16 | hash[offset + 2] << 8 | (hash[offset + 3])) % 1000000;
    return String(c).padStart(6, '0');
  }, secret!);

  // Login
  await page.locator('input[placeholder="Username"]').first().fill(user);
  await page.locator('input[placeholder="Password"]').first().fill('testpass123');
  await page.locator('input[placeholder="TOTP Code"]').fill(code);
  await page.locator('.n-modal button:has-text("Sign In")').click();

  // Should be logged in and see challenge area
  await page.waitForTimeout(2000);
  await expect(page.locator('text=Select a challenge to begin')).toBeVisible({ timeout: 10000 });
});
