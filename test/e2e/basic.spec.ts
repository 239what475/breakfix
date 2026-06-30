import { test, expect } from '@playwright/test';

const BASE = 'http://localhost:9090';

test('login page loads', async ({ page }) => {
  await page.goto(BASE + '/#/login');
  await expect(page.locator('h1')).toContainText('Breakfix');
  await expect(page.locator('input[placeholder="Username"]')).toBeVisible();
  await expect(page.locator('input[placeholder="Password"]')).toBeVisible();
  await expect(page.locator('input[placeholder="TOTP Code"]')).toBeVisible();
  await expect(page.locator('button[type="submit"]')).toContainText('Sign in');
});

test('register page loads', async ({ page }) => {
  await page.goto(BASE + '/#/register');
  await expect(page.locator('h1')).toContainText('Create Account');
  await expect(page.locator('input[placeholder="Username"]')).toBeVisible();
  await expect(page.locator('input[placeholder*="Password"]')).toBeVisible();
  await expect(page.locator('button[type="submit"]')).toContainText('Register');
});

test('register and login flow', async ({ page }) => {
  const user = 'e2e-' + Date.now();
  // Register
  await page.goto(BASE + '/#/register');
  await page.fill('input[placeholder="Username"]', user);
  await page.fill('input[placeholder*="Password"]', 'testpass123');
  await page.click('button[type="submit"]');
  await expect(page.locator('text=TOTP Secret')).toBeVisible({ timeout: 10000 });

  // Get TOTP secret from page (the code value, not the label)
  const secret = await page.locator('p.text-white.font-mono').textContent();
  expect(secret).toBeTruthy();
  console.log('TOTP Secret:', secret);

  // Go to login
  await page.click('text=Go to Login');

  // Generate TOTP code using the secret
  const totpCode = await page.evaluate(async (sec) => {
    // Simple TOTP implementation in browser
    async function sha1(message: string): Promise<ArrayBuffer> {
      const encoder = new TextEncoder();
      return crypto.subtle.digest('SHA-1', encoder.encode(message));
    }
    function base32Decode(s: string): Uint8Array {
      const alphabet = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ234567';
      let bits = 0, value = 0;
      const result: number[] = [];
      for (let i = 0; i < s.length; i++) {
        value = (value << 5) | alphabet.indexOf(s[i].toUpperCase());
        bits += 5;
        if (bits >= 8) {
          result.push((value >> (bits - 8)) & 0xff);
          bits -= 8;
        }
      }
      return new Uint8Array(result);
    }
    const key = base32Decode(sec);
    const counter = Math.floor(Date.now() / 1000 / 30);
    const buf = new ArrayBuffer(8);
    const dv = new DataView(buf);
    dv.setBigUint64(0, BigInt(counter), false);
    const keyBuf = await crypto.subtle.importKey('raw', key, {name: 'HMAC', hash:'SHA-1'}, false, ['sign']);
    const sig = await crypto.subtle.sign('HMAC', keyBuf, buf);
    const hash = new Uint8Array(sig);
    const offset = hash[hash.length - 1] & 0xf;
    const code = ((hash[offset] & 0x7f) << 24 | hash[offset+1] << 16 | hash[offset+2] << 8 | hash[offset+3]) % 1000000;
    return String(code).padStart(6, '0');
  }, secret);
  console.log('TOTP Code:', totpCode);

  await page.fill('input[placeholder="Username"]', user);
  await page.fill('input[placeholder="Password"]', 'testpass123');
  await page.fill('input[placeholder="TOTP Code"]', totpCode);
  await page.click('button[type="submit"]');

  // Should redirect to challenge list
  await expect(page.locator('text=Breakfix')).toBeVisible({ timeout: 10000 });
  await expect(page.locator('text=No challenges yet')).toBeVisible({ timeout: 5000 });
});

test('challenge list page loads when not logged in', async ({ page }) => {
  await page.goto(BASE + '/#/');
  // Should redirect to login
  await expect(page.locator('h1')).toContainText('Breakfix', { timeout: 5000 });
  await expect(page.locator('input[placeholder="Username"]')).toBeVisible();
});

test('terminal page requires auth', async ({ page }) => {
  await page.goto(BASE + '/#/terminal/test-challenge');
  await expect(page.locator('input[placeholder="Username"]')).toBeVisible({ timeout: 5000 });
});
