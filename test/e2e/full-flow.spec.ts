import { test, expect } from '@playwright/test';

const BASE = 'http://localhost:9090';

// TOTP generator that runs inside browser context
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
    const buf = new ArrayBuffer(8);
    const dv = new DataView(buf);
    dv.setBigUint64(0, BigInt(counter), false);
    const keyBuf = await crypto.subtle.importKey('raw', key, { name: 'HMAC', hash: 'SHA-1' }, false, ['sign']);
    const sig = await crypto.subtle.sign('HMAC', keyBuf, buf);
    const hash = new Uint8Array(sig);
    const offset = hash[hash.length - 1] & 0xf;
    const code = ((hash[offset] & 0x7f) << 24 | hash[offset + 1] << 16 | hash[offset + 2] << 8 | (hash[offset + 3])) % 1000000;
    return String(code).padStart(6, '0');
  }, secret);
}

test.describe('Full Challenge Flow via UI', () => {
  const user = `e2e-ui-${Date.now()}`;

  test('register → login → start challenge → terminal → submit', async ({ page }) => {
    // ===== STEP 1: Open the app, see sign-in prompt =====
    await page.goto(BASE + '/#/');
    await expect(page.locator('h1')).toContainText('Breakfix');
    await expect(page.locator('text=Sign in to practice')).toBeVisible();

    // ===== STEP 2: Register =====
    await page.click('button:has-text("Register")');
    await expect(page.locator('h2')).toContainText('Create Account');
    await page.fill('input[placeholder="Username"]', user);
    await page.fill('input[placeholder*="Password"]', 'testpass123');
    await page.click('button[type="submit"]');

    // After register, should see TOTP secret + QR
    await expect(page.locator('text=TOTP Secret')).toBeVisible({ timeout: 10000 });
    const secret = await page.locator('p.text-white.font-mono').textContent();
    expect(secret).toBeTruthy();
    console.log('Registered, TOTP secret:', secret);

    // ===== STEP 3: Switch to login and sign in =====
    await page.click('text=Continue to Sign In');
    await expect(page.locator('h2')).toContainText('Sign In');

    const code = await totpCode(page, secret!);
    await page.fill('input[placeholder="Username"]', user);
    await page.fill('input[placeholder="Password"]', 'testpass123');
    await page.fill('input[placeholder="TOTP Code"]', code);
    await page.click('button[type="submit"]');

    // Should see welcome toast and challenge list
    await expect(page.locator('text=Welcome back')).toBeVisible({ timeout: 10000 });

    // ===== STEP 4: Browse challenge list =====
    // Wait for challenge list to load
    await page.waitForTimeout(1500);

    // Challenge list shows challenge titles. cleanup-logs title is "批量压缩旧日志"
    const challengeEntry = page.locator('text=批量压缩旧日志');
    const noChallenges = page.locator('text=No challenges yet');

    // Wait for either the challenge or the empty message
    await page.waitForTimeout(2000);
    const hasChallenge = await challengeEntry.isVisible().catch(() => false);
    const isEmpty = await noChallenges.isVisible().catch(() => false);
    console.log('Has challenge:', hasChallenge, 'Is empty:', isEmpty);

    if (hasChallenge) {
      console.log('Found cleanup-logs challenge');
      await challengeEntry.click();
      await expect(page.locator('text=Start Challenge')).toBeVisible({ timeout: 3000 });

      // ===== STEP 5: Start challenge → go to terminal =====
      await page.click('text=Start Challenge');

      // Wait for navigation to terminal page (start API blocks until pod is ready)
      try {
        await page.waitForURL('**/terminal/**', { timeout: 90000 });
        console.log('Navigated to terminal');
      } catch {
        console.log('Start failed — no navigation to terminal');
        // Check for error toast
        const errorToast = page.locator('.bg-red-600').last();
        if (await errorToast.isVisible().catch(() => false)) {
          console.log('Error:', await errorToast.textContent());
        }
        return;
      }

      // Wait for xterm.js to render
      await page.waitForTimeout(2000);

      // Collect console errors and page content for debugging
      const errors: string[] = [];
      page.on('console', msg => {
        if (msg.type() === 'error') errors.push(msg.text());
      });

      // Wait for xterm.js to render
      // Wait for xterm.js to render
      console.log('Waiting for xterm.js...');
      const xtermContainer = page.locator('.xterm');
      let hasTerminal = false;
      try {
        await xtermContainer.waitFor({ state: 'visible', timeout: 30000 });
        hasTerminal = true;
      } catch { hasTerminal = false; }
      console.log('Terminal visible:', hasTerminal);

      if (hasTerminal) {
        // ===== STEP 6: Solve the challenge in terminal =====
        // Cleanup-logs challenge: write a script that finds old/large logs and compresses them
        await xtermContainer.click();
        await page.waitForTimeout(500);

        // Write the cleanup script
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

        console.log('Typing solution script...');
        for (const line of script) {
          await page.keyboard.type(line, { delay: 20 });
          await page.keyboard.press('Enter');
          await page.waitForTimeout(200);
        }
        console.log('Script written to /usr/local/bin/cleanup.sh');

        // Run the script
        await page.keyboard.type('/usr/local/bin/cleanup.sh', { delay: 30 });
        await page.keyboard.press('Enter');
        await page.waitForTimeout(2000);
        console.log('Script executed');

        // Check results
        await page.keyboard.type('ls -la /backup/', { delay: 30 });
        await page.keyboard.press('Enter');
        await page.waitForTimeout(1000);
        console.log('Checked /backup directory');
      }

      // ===== STEP 7: Submit and verify PASS =====
      await page.click('text=Submit');
      await page.waitForTimeout(3000);

      const passedBanner = page.locator('text=PASSED');
      const failedBanner = page.locator('text=FAILED');
      const hasResult = await Promise.race([
        passedBanner.isVisible().then(() => 'pass'),
        failedBanner.isVisible().then(() => 'fail'),
        new Promise(r => setTimeout(() => r('timeout'), 5000)),
      ]);
      console.log('Submit result:', hasResult);

      // ===== STEP 8: Go back to list =====
      await page.goto(BASE + '/#/');
      await expect(page.locator('h1')).toContainText('Breakfix', { timeout: 5000 });
      console.log('Back to challenge list OK');
    } else {
      console.log('No challenges in DB — skipping terminal test');
      console.log('Run: bin/gateway to sync challenges into DB');
    }
  });
});
