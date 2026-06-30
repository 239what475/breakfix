import { test, expect } from '@playwright/test';

const BASE = 'http://localhost:9090';

test('main page loads with sign in prompt', async ({ page }) => {
  await page.goto(BASE + '/#/');
  await expect(page.locator('h1')).toContainText('Breakfix');
  await expect(page.locator('text=Sign in to practice')).toBeVisible();
  await expect(page.locator('text=SRE/DevOps interview challenges')).toBeVisible();
});

test('sign in button opens login modal', async ({ page }) => {
  await page.goto(BASE + '/#/');
  await page.click('button:has-text("Sign In")');
  await expect(page.locator('h2')).toContainText('Sign In', { timeout: 5000 });
  await expect(page.locator('input[placeholder="Username"]')).toBeVisible();
  await expect(page.locator('input[placeholder="Password"]')).toBeVisible();
  await expect(page.locator('input[placeholder="TOTP Code"]')).toBeVisible();
  // Close modal with Escape
  await page.keyboard.press('Escape');
  await expect(page.locator('h2')).not.toBeVisible({ timeout: 3000 });
});

test('register button opens register modal', async ({ page }) => {
  await page.goto(BASE + '/#/');
  await page.click('text=Register');
  await expect(page.locator('h2')).toContainText('Create Account');
  await expect(page.locator('input[placeholder="Username"]')).toBeVisible();
  await expect(page.locator('input[placeholder*="Password"]')).toBeVisible();
});

test('register and login flow via modal', async ({ page }) => {
  const user = 'e2e-' + Date.now();
  await page.goto(BASE + '/#/');

  // Open register modal
  await page.click('text=Register');
  await page.fill('input[placeholder="Username"]', user);
  await page.fill('input[placeholder*="Password"]', 'testpass123');
  await page.click('button[type="submit"]');

  // Should show TOTP secret
  await expect(page.locator('text=TOTP Secret')).toBeVisible({ timeout: 10000 });

  // Get TOTP code
  const secret = await page.locator('p.text-white.font-mono').textContent();
  expect(secret).toBeTruthy();

  // Generate TOTP in browser
  const totpCode = await page.evaluate(async (sec) => {
    function base32Decode(s: string): Uint8Array {
      const alphabet = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ234567';
      let bits = 0, value = 0;
      const result: number[] = [];
      for (let i = 0; i < s.length; i++) {
        value = (value << 5) | alphabet.indexOf(s[i].toUpperCase());
        bits += 5;
        if (bits >= 8) { result.push((value >>> (bits - 8)) & 0xff); bits -= 8; }
      }
      return new Uint8Array(result);
    }
    const key = base32Decode(sec!);
    const counter = Math.floor(Date.now() / 1000 / 30);
    const buf = new ArrayBuffer(8);
    const dv = new DataView(buf);
    dv.setBigUint64(0, BigInt(counter), false);
    const keyBuf = await crypto.subtle.importKey('raw', key, {name:'HMAC', hash:'SHA-1'}, false, ['sign']);
    const sig = await crypto.subtle.sign('HMAC', keyBuf, buf);
    const hash = new Uint8Array(sig);
    const offset = hash[hash.length - 1] & 0xf;
    const code = ((hash[offset] & 0x7f) << 24 | hash[offset+1] << 16 | hash[offset+2] << 8 | hash[offset+3]) % 1000000;
    return String(code).padStart(6, '0');
  }, secret);

  // Switch to login
  await page.click('text=Continue to Sign In');
  await page.fill('input[placeholder="Username"]', user);
  await page.fill('input[placeholder="Password"]', 'testpass123');
  await page.fill('input[placeholder="TOTP Code"]', totpCode);
  await page.click('button[type="submit"]');

  // Should see challenge list
  await expect(page.locator('text=No challenges yet')).toBeVisible({ timeout: 10000 });
  // Should see toast
  await expect(page.locator('text=Welcome back')).toBeVisible({ timeout: 5000 });
});

test('terminal page requires auth — redirects to main', async ({ page }) => {
  await page.goto(BASE + '/#/terminal/test-challenge');
  await expect(page.locator('text=Sign in to practice')).toBeVisible({ timeout: 5000 });
});
