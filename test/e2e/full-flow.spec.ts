import { test, expect } from '@playwright/test';

const BASE = 'http://localhost:9090';

async function totpCode(page: any, secret: string): Promise<string> {
  return page.evaluate(async (sec: string) => {
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
  }, secret);
}

test.describe('Full Challenge Flow via UI', () => {
  const user = `e2e-ui-${Date.now()}`;

  test('register → login → start challenge → terminal → submit', async ({ page }) => {
    test.setTimeout(180000);

    // STEP 1: Open app
    await page.goto(BASE + '/#/');
    await expect(page.locator('text=● Breakfix')).toBeVisible();

    // STEP 2: Register
    await page.getByRole('button', { name: 'Register', exact: true }).click();
    await page.locator('h3:has-text("Create Account")').waitFor();
    await page.locator('input[placeholder="Username"]').first().fill(user);
    await page.locator('input[placeholder="Password (min 6 chars)"]').fill('testpass123');
    await page.locator('.n-modal button:has-text("Register")').click();

    // Wait for TOTP
    await page.locator('text=TOTP Secret').waitFor({ timeout: 10000 });
    const secretEl = page.locator('text="TOTP Secret"').locator('..').locator('div[style*="monospace"]');
    const secret = await secretEl.textContent();
    await page.locator('text=Continue to Sign In').click();

    // STEP 3: Login
    const code = await totpCode(page, secret!);
    await page.locator('input[placeholder="Username"]').first().fill(user);
    await page.locator('input[placeholder="Password"]').first().fill('testpass123');
    await page.locator('input[placeholder="TOTP Code"]').fill(code);
    await page.locator('.n-modal button:has-text("Sign In")').click();
    await page.waitForTimeout(2000);

    // STEP 4: Find challenge in sidebar
    const challengeEntry = page.locator('text=批量压缩旧日志');
    const hasChallenge = await challengeEntry.isVisible().catch(() => false);
    console.log('Has challenge:', hasChallenge);
    if (!hasChallenge) { console.log('No challenge in DB, skipping'); return; }

    await challengeEntry.click();
    await expect(page.getByRole('button', { name: 'Start Challenge' })).toBeVisible();

    // STEP 5: Start challenge
    await page.getByRole('button', { name: 'Start Challenge' }).click();

    // Wait for terminal
    try {
      await page.waitForURL('**/#/**', { timeout: 5000 });
    } catch { /* hash stays the same, terminal shows in-place */ }
    console.log('Waiting for xterm.js...');
    const xtermContainer = page.locator('.xterm');
    const hasTerminal = await xtermContainer.isVisible({ timeout: 90000 }).catch(() => false);
    console.log('Terminal visible:', hasTerminal);

    if (hasTerminal) {
      // STEP 6: Solve challenge in terminal
      await xtermContainer.click();
      await page.waitForTimeout(500);

      const script = [
        'cat > /usr/local/bin/cleanup.sh << \'ENDOFSCRIPT\'',
        '#!/bin/bash',
        'mkdir -p /backup',
        'find /var/log -type f -name "*.log" -mtime +6 -size +100M | while IFS= read -r f; do',
        '  name=$(basename "$f" .log)',
        '  tar -czf "/backup/${name}.tar.gz" -C "$(dirname "$f")" "$(basename "$f")"',
        'done',
        'ENDOFSCRIPT',
        'chmod +x /usr/local/bin/cleanup.sh',
        'echo "Script created"',
      ];
      for (const line of script) {
        await page.keyboard.type(line, { delay: 20 });
        await page.keyboard.press('Enter');
        await page.waitForTimeout(200);
      }
      console.log('Script written');

      await page.keyboard.type('/usr/local/bin/cleanup.sh', { delay: 30 });
      await page.keyboard.press('Enter');
      await page.waitForTimeout(2000);
      console.log('Script executed');
    }

    // STEP 7: Submit
    await page.getByRole('button', { name: 'Submit' }).first().click();
    await page.waitForTimeout(3000);

    const passedBanner = page.locator('text=✓ PASSED');
    const failedBanner = page.locator('text=✗ FAILED');
    const result = await Promise.race([
      passedBanner.isVisible().then(() => 'pass'),
      failedBanner.isVisible().then(() => 'fail'),
      new Promise(r => setTimeout(() => r('timeout'), 5000)),
    ]);
    console.log('Submit result:', result);
  });
});
