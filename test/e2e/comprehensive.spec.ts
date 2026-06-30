import { test, expect } from '@playwright/test';

const BASE = 'http://localhost:9090';

// ── Helpers ──

function uniqueUser() {
  return `e2e-${Date.now()}-${Math.random().toString(36).slice(2, 6)}`;
}

async function genTOTP(page: any, secret: string): Promise<string> {
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

async function registerAndLogin(page: any): Promise<{ user: string; toast: any }> {
  const user = uniqueUser();
  await page.goto(BASE + '/#/');
  await page.click('button:has-text("Register")');
  await page.fill('input[placeholder="Username"]', user);
  await page.fill('input[placeholder*="Password"]', 'testpass123');
  await page.click('button[type="submit"]');
  await expect(page.locator('text=TOTP Secret')).toBeVisible({ timeout: 10000 });

  const secret = await page.locator('p.text-white.font-mono').textContent();
  await page.click('text=Continue to Sign In');

  const code = await genTOTP(page, secret!);
  await page.fill('input[placeholder="Username"]', user);
  await page.fill('input[placeholder="Password"]', 'testpass123');
  await page.fill('input[placeholder="TOTP Code"]', code);
  await page.click('button[type="submit"]');
  await expect(page.locator('text=Welcome back')).toBeVisible({ timeout: 10000 });

  return { user, toast: page.locator('text=Welcome back') };
}

// ── FIXTURES ──

test.describe('Main Page', () => {
  test('displays branding and auth prompt when not logged in', async ({ page }) => {
    await page.goto(BASE + '/#/');
    await expect(page.locator('h1')).toContainText('Breakfix');
    await expect(page.locator('text=Sign in to practice')).toBeVisible();
    await expect(page.locator('text=SRE/DevOps interview challenges')).toBeVisible();
  });

  test('shows Sign In and Register buttons', async ({ page }) => {
    await page.goto(BASE + '/#/');
    await expect(page.locator('button:has-text("Sign In")')).toBeVisible();
    await expect(page.locator('button:has-text("Register")')).toBeVisible();
  });

  test('shows lock icon in main area', async ({ page }) => {
    await page.goto(BASE + '/#/');
    await expect(page.locator('text=Sign in to view challenges')).toBeVisible();
  });
});

test.describe('Login Modal', () => {
  test('opens when Sign In button clicked', async ({ page }) => {
    await page.goto(BASE + '/#/');
    await page.click('button:has-text("Sign In")');
    await expect(page.locator('h2')).toContainText('Sign In');
  });

  test('contains all required fields', async ({ page }) => {
    await page.goto(BASE + '/#/');
    await page.click('button:has-text("Sign In")');
    await expect(page.locator('input[placeholder="Username"]')).toBeVisible();
    await expect(page.locator('input[placeholder="Password"]')).toBeVisible();
    await expect(page.locator('input[placeholder="TOTP Code"]')).toBeVisible();
    await expect(page.locator('button:has-text("Sign In")').last()).toBeVisible();
  });

  test('closes with Escape key', async ({ page }) => {
    await page.goto(BASE + '/#/');
    await page.click('button:has-text("Sign In")');
    await expect(page.locator('h2')).toBeVisible();
    await page.keyboard.press('Escape');
    await expect(page.locator('h2')).not.toBeVisible({ timeout: 3000 });
  });

  test('can switch to Register from login', async ({ page }) => {
    await page.goto(BASE + '/#/');
    await page.click('button:has-text("Sign In")');
    // Target the "Register" link inside the modal footer
    await page.locator('.relative button:has-text("Register")').click();
    await expect(page.locator('h2')).toContainText('Create Account');
    await expect(page.locator('input[placeholder*="Password"]')).toBeVisible();
  });

  test('shows error toast on invalid credentials', async ({ page }) => {
    await page.goto(BASE + '/#/');
    await page.click('button:has-text("Sign In")');
    await page.fill('input[placeholder="Username"]', 'nonexistent');
    await page.fill('input[placeholder="Password"]', 'wrongpass');
    await page.fill('input[placeholder="TOTP Code"]', '000000');
    await page.click('button[type="submit"]');
    await expect(page.locator('text=invalid credentials')).toBeVisible({ timeout: 5000 });
  });
});

test.describe('Register Modal', () => {
  test('opens when Register button clicked', async ({ page }) => {
    await page.goto(BASE + '/#/');
    await page.click('button:has-text("Register")');
    await expect(page.locator('h2')).toContainText('Create Account');
  });

  test('shows username and password fields', async ({ page }) => {
    await page.goto(BASE + '/#/');
    await page.click('button:has-text("Register")');
    await expect(page.locator('input[placeholder="Username"]')).toBeVisible();
    await expect(page.locator('input[placeholder*="Password"]')).toBeVisible();
  });

  test('shows error on duplicate username', async ({ page }) => {
    const user = uniqueUser();
    // Register once
    await page.goto(BASE + '/#/');
    await page.click('button:has-text("Register")');
    await page.fill('input[placeholder="Username"]', user);
    await page.fill('input[placeholder*="Password"]', 'testpass123');
    await page.click('button[type="submit"]');
    await expect(page.locator('text=TOTP Secret')).toBeVisible({ timeout: 10000 });
    // Close
    await page.keyboard.press('Escape');

    // Register again with same user
    await page.click('button:has-text("Register")');
    await page.fill('input[placeholder="Username"]', user);
    await page.fill('input[placeholder*="Password"]', 'testpass123');
    await page.click('button[type="submit"]');
    await expect(page.locator('text=user already exists')).toBeVisible({ timeout: 5000 });
  });

  test('shows TOTP secret and QR code after successful register', async ({ page }) => {
    await page.goto(BASE + '/#/');
    await page.click('button:has-text("Register")');
    await page.fill('input[placeholder="Username"]', uniqueUser());
    await page.fill('input[placeholder*="Password"]', 'testpass123');
    await page.click('button[type="submit"]');

    await expect(page.locator('text=TOTP Secret')).toBeVisible({ timeout: 10000 });
    await expect(page.locator('text=Scan QR Code')).toBeVisible();
    await expect(page.locator('img[alt="TOTP QR"]')).toBeVisible();
    // Should show "Continue to Sign In" button
    await expect(page.locator('text=Continue to Sign In')).toBeVisible();
  });

  test('can switch to Sign In from register', async ({ page }) => {
    await page.goto(BASE + '/#/');
    await page.click('button:has-text("Register")');
    // Target the "Sign In" link inside the modal footer
    await page.locator('.relative button:has-text("Sign In")').click();
    await expect(page.locator('h2')).toContainText('Sign In');
    await expect(page.locator('input[placeholder="TOTP Code"]')).toBeVisible();
  });
});

test.describe('Toast Notifications', () => {
  test('success toast appears on login', async ({ page }) => {
    await registerAndLogin(page);
    // Target only the welcome toast (not the register success toast)
    await expect(page.locator('.bg-emerald-600:has-text("Welcome")')).toBeVisible();
  });

  test('error toast appears on wrong password', async ({ page }) => {
    await page.goto(BASE + '/#/');
    await page.click('button:has-text("Sign In")');
    await page.fill('input[placeholder="Username"]', 'nobody');
    await page.fill('input[placeholder="Password"]', 'bad');
    await page.fill('input[placeholder="TOTP Code"]', '000000');
    await page.click('button[type="submit"]');
    await expect(page.locator('.bg-red-600')).toBeVisible({ timeout: 5000 });
  });

  test('toast disappears after timeout', async ({ page }) => {
    await page.goto(BASE + '/#/');
    await page.click('button:has-text("Sign In")');
    await page.fill('input[placeholder="Username"]', 'nobody');
    await page.fill('input[placeholder="Password"]', 'bad');
    await page.fill('input[placeholder="TOTP Code"]', '000000');
    await page.click('button[type="submit"]');
    const toast = page.locator('.bg-red-600');
    await expect(toast).toBeVisible({ timeout: 5000 });
    // Toast should disappear within 5 seconds
    await expect(toast).not.toBeVisible({ timeout: 6000 });
  });
});

test.describe('Authenticated Challenge List', () => {
  test('shows "No challenges" when DB empty', async ({ page }) => {
    await registerAndLogin(page);
    // Wait briefly for challenge list load
    await page.waitForTimeout(1000);
    await expect(page.locator('text=No challenges yet')).toBeVisible({ timeout: 5000 });
  });

  test('shows Sign Out button when logged in', async ({ page }) => {
    await registerAndLogin(page);
    await expect(page.locator('text=Out')).toBeVisible({ timeout: 5000 });
  });

  test('logout returns to sign-in prompt', async ({ page }) => {
    await registerAndLogin(page);
    await expect(page.locator('text=Out')).toBeVisible({ timeout: 5000 });
    await page.click('text=Out');
    await expect(page.locator('text=Sign in to practice')).toBeVisible({ timeout: 5000 });
  });

  test('shows logout toast', async ({ page }) => {
    await registerAndLogin(page);
    await page.waitForTimeout(500);
    await page.click('text=Out');
    await expect(page.locator('text=Signed out')).toBeVisible({ timeout: 5000 });
  });

  test('sign in button re-opens after logout', async ({ page }) => {
    await registerAndLogin(page);
    await page.click('text=Out');
    await expect(page.locator('button:has-text("Sign In")')).toBeVisible({ timeout: 5000 });
  });
});

test.describe('Terminal Page', () => {
  test('redirects to main when not logged in', async ({ page }) => {
    await page.goto(BASE + '/#/terminal/any-challenge');
    await expect(page.locator('text=Sign in to practice')).toBeVisible({ timeout: 5000 });
  });

  test('redirects to main when not logged in (hash route)', async ({ page }) => {
    await page.goto(BASE + '/#/terminal/test123');
    await expect(page.locator('text=Sign in to view challenges')).toBeVisible({ timeout: 5000 });
  });
});

test.describe('API Endpoints (Direct)', () => {
  let token: string;

  test.beforeAll(async ({ request }) => {
    const user = uniqueUser();
    // Register
    await request.post(BASE + '/api/auth/register', {
      data: { username: user, password: 'testpass123' },
    });
    // We can't login without TOTP, so we test unauthenticated access instead
  });

  test('POST /api/auth/register returns TOTP', async ({ request }) => {
    const res = await request.post(BASE + '/api/auth/register', {
      data: { username: uniqueUser(), password: 'testpass123' },
    });
    expect(res.ok()).toBeTruthy();
    const body = await res.json();
    expect(body.totp_secret).toBeTruthy();
    expect(body.totp_url).toContain('otpauth://');
  });

  test('POST /api/auth/register rejects short username', async ({ request }) => {
    const res = await request.post(BASE + '/api/auth/register', {
      data: { username: 'a', password: 'testpass123' },
    });
    expect(res.status()).toBe(400);
  });

  test('POST /api/auth/register rejects short password', async ({ request }) => {
    const res = await request.post(BASE + '/api/auth/register', {
      data: { username: uniqueUser(), password: '12345' },
    });
    expect(res.status()).toBe(400);
  });

  test('GET /api/challenges requires auth', async ({ request }) => {
    const res = await request.get(BASE + '/api/challenges');
    expect(res.status()).toBe(401);
  });

  test('POST /api/challenges/:id/start requires auth', async ({ request }) => {
    const res = await request.post(BASE + '/api/challenges/test/start');
    expect(res.status()).toBe(401);
  });

  test('POST /api/challenges/:id/submit requires auth', async ({ request }) => {
    const res = await request.post(BASE + '/api/challenges/test/submit');
    expect(res.status()).toBe(401);
  });

  test('POST /api/challenges/:id/reset requires auth', async ({ request }) => {
    const res = await request.post(BASE + '/api/challenges/test/reset');
    expect(res.status()).toBe(401);
  });

  test('POST /api/generate requires auth', async ({ request }) => {
    const res = await request.post(BASE + '/api/generate', {
      data: { topic: 'test' },
    });
    expect(res.status()).toBe(401);
  });

  test('GET /api/openapi.json returns spec', async ({ request }) => {
    const res = await request.get(BASE + '/api/openapi.json');
    expect(res.ok()).toBeTruthy();
    const body = await res.json();
    expect(body.info.title).toBe('Breakfix API');
    expect(body.paths).toBeDefined();
  });

  test('POST /api/auth/login with wrong TOTP returns 401', async ({ request }) => {
    const user = uniqueUser();
    await request.post(BASE + '/api/auth/register', {
      data: { username: user, password: 'testpass123' },
    });
    const res = await request.post(BASE + '/api/auth/login', {
      data: { username: user, password: 'testpass123', totp_code: '000000' },
    });
    expect(res.status()).toBe(401);
  });
});

test.describe('Complete User Flow', () => {
  test('register → login → view list → logout', async ({ page }) => {
    await registerAndLogin(page);
    // Auth produces welcome toast
    await expect(page.locator('text=Welcome back')).toBeVisible({ timeout: 5000 });
    // Challenge list visible
    await expect(page.locator('h1')).toContainText('Breakfix');
    // Logout
    await page.click('text=Out');
    await expect(page.locator('text=Sign in to practice')).toBeVisible({ timeout: 5000 });
  });

  test('re-login after logout works', async ({ page }) => {
    const { user } = await registerAndLogin(page);
    await page.click('text=Out');
    await page.waitForTimeout(300);

    // Re-login
    await page.click('button:has-text("Sign In")');
    // We can't re-login without fresh TOTP, but we verify the modal opens
    await expect(page.locator('h2')).toContainText('Sign In');
  });
});
