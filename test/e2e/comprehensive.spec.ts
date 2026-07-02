import { expect, test } from '@playwright/test'
import { BASE, registerAndLogin, selectCleanupLogs, signOut, uniqueUser } from './helpers/ui'

test.describe('Main Page', () => {
  test('displays single-page workspace for guests', async ({ page }) => {
    await page.goto(BASE + '/')
    await expect(page.locator('.brand-title')).toHaveText('Breakfix')
    await expect(page.locator('.brand-subtitle')).toHaveText('SRE terminal labs')
    await expect(page.locator('.workspace-header h1')).toHaveText('Terminal Workspace')
    await expect(page.locator('.workspace-header p')).toContainText('Authenticate to browse labs')
    await expect(page.getByRole('button', { name: 'Sign In', exact: true })).toBeVisible()
    await expect(page.getByRole('button', { name: 'Register', exact: true })).toBeVisible()
  })

  test('shows terminal empty state before launch', async ({ page }) => {
    await page.goto(BASE + '/')
    await expect(page.locator('.terminal-empty-card h2')).toHaveText('Pick a challenge from the left')
    await expect(page.locator('.terminal-empty-card')).toContainText('Browse the challenge catalog')
    await expect(page.getByRole('button', { name: 'Sign In', exact: true })).toBeVisible()
  })
})

test.describe('Auth Modal', () => {
  test('login modal opens and closes with escape', async ({ page }) => {
    await page.goto(BASE + '/')
    await page.getByRole('button', { name: 'Sign In', exact: true }).click()
    await expect(page.locator('h3:has-text("Sign In")')).toBeVisible()
    await expect(page.locator('input[placeholder="Username"]')).toBeVisible()
    await expect(page.locator('input[placeholder="Password"]')).toBeVisible()
    await expect(page.locator('input[placeholder="TOTP Code"]')).toBeVisible()
    await page.keyboard.press('Escape')
    await expect(page.locator('h3:has-text("Sign In")')).not.toBeVisible({ timeout: 3000 })
  })

  test('can switch from login to register', async ({ page }) => {
    await page.goto(BASE + '/')
    await page.getByRole('button', { name: 'Sign In', exact: true }).click()
    await page.locator('.auth-foot').getByRole('button', { name: 'Register', exact: true }).click()
    await expect(page.locator('h3:has-text("Create Account")')).toBeVisible()
    await expect(page.locator('input[placeholder="Password (min 6 chars)"]')).toBeVisible()
  })

  test('shows error toast on invalid credentials', async ({ page }) => {
    await page.goto(BASE + '/')
    await page.getByRole('button', { name: 'Sign In', exact: true }).click()
    await page.locator('input[placeholder="Username"]').first().fill('nonexistent')
    await page.locator('input[placeholder="Password"]').first().fill('wrongpass')
    await page.locator('input[placeholder="TOTP Code"]').fill('000000')
    await page.getByRole('button', { name: 'Sign In' }).last().click()
    await expect(page.locator('text=invalid credentials')).toBeVisible({ timeout: 5000 })
  })
})

test.describe('Register Flow', () => {
  test('shows TOTP secret and QR after successful register', async ({ page }) => {
    await page.goto(BASE + '/')
    await page.getByRole('button', { name: 'Register', exact: true }).click()
    await page.locator('input[placeholder="Username"]').first().fill(uniqueUser('register'))
    await page.locator('input[placeholder="Password (min 6 chars)"]').fill('testpass123')
    await page.getByRole('button', { name: 'Register' }).last().click()

    await expect(page.locator('text=TOTP Secret')).toBeVisible({ timeout: 10000 })
    await expect(page.locator('.totp-secret code')).toBeVisible()
    await expect(page.locator('.totp-qr-wrap canvas')).toBeVisible()
    await expect(page.getByRole('button', { name: 'Continue to Sign In', exact: true })).toBeVisible()
  })

  test('shows error on duplicate username', async ({ page }) => {
    const user = uniqueUser('dupe')
    await page.goto(BASE + '/')
    await page.getByRole('button', { name: 'Register', exact: true }).click()
    await page.locator('input[placeholder="Username"]').first().fill(user)
    await page.locator('input[placeholder="Password (min 6 chars)"]').fill('testpass123')
    await page.getByRole('button', { name: 'Register' }).last().click()
    await expect(page.locator('.totp-secret code')).toBeVisible({ timeout: 10000 })

    await page.keyboard.press('Escape')
    await page.getByRole('button', { name: 'Register', exact: true }).click()
    await page.locator('input[placeholder="Username"]').first().fill(user)
    await page.locator('input[placeholder="Password (min 6 chars)"]').fill('testpass123')
    await page.getByRole('button', { name: 'Register' }).last().click()
    await expect(page.locator('text=user already exists')).toBeVisible({ timeout: 5000 })
  })
})

test.describe('Authenticated Workspace', () => {
  test('shows challenge list after login', async ({ page }) => {
    await registerAndLogin(page)
    await expect(page.locator('.account-value')).toHaveText('Authenticated')
    await expect(page.locator('.challenge-list .challenge-card').first()).toBeVisible()
    await expect(page.locator('.workspace-header h1')).toHaveText('Terminal Workspace')
    await expect(page.locator('.terminal-empty-card h2')).toBeVisible()
  })

  test('can select a challenge and view its brief', async ({ page }) => {
    await registerAndLogin(page)
    await selectCleanupLogs(page)
    await expect(page.locator('.brief-kicker')).toHaveText('Challenge brief')
    await expect(page.locator('.terminal-empty-card .brief-kicker')).toHaveText('Challenge brief')
    await expect(page.locator('.terminal-empty-card .brief-actions').getByRole('button', { name: 'Start Challenge', exact: true })).toBeVisible()
  })

  test('logout returns to guest mode', async ({ page }) => {
    await registerAndLogin(page)
    await signOut(page)
    await expect(page.getByRole('button', { name: 'Sign In', exact: true })).toBeVisible({ timeout: 5000 })
    await expect(page.locator('.workspace-status')).toContainText('Guest mode')
  })
})

test.describe('Hash Routes', () => {
  test('unknown hash route still renders the single-page app', async ({ page }) => {
    await page.goto(BASE + '/terminal/any-challenge')
    await expect(page.locator('.brand-title')).toHaveText('Breakfix')
    await expect(page.locator('.workspace-header h1')).toBeVisible()
  })
})

test.describe('API Endpoints (Direct)', () => {
  test('POST /api/auth/register returns TOTP', async ({ request }) => {
    const res = await request.post(BASE + '/api/auth/register', {
      data: { username: uniqueUser('api-register'), password: 'testpass123' },
    })
    expect(res.ok()).toBeTruthy()
    const body = await res.json()
    expect(body.totp_secret).toBeTruthy()
    expect(body.totp_url).toContain('otpauth://')
  })

  test('POST /api/auth/register rejects short username', async ({ request }) => {
    const res = await request.post(BASE + '/api/auth/register', {
      data: { username: 'a', password: 'testpass123' },
    })
    expect(res.status()).toBe(400)
  })

  test('POST /api/auth/register rejects short password', async ({ request }) => {
    const res = await request.post(BASE + '/api/auth/register', {
      data: { username: uniqueUser('api-shortpass'), password: '12345' },
    })
    expect(res.status()).toBe(400)
  })

  test('GET /api/challenges requires auth', async ({ request }) => {
    const res = await request.get(BASE + '/api/challenges')
    expect(res.status()).toBe(401)
  })

  test('POST /api/challenges/:id/start requires auth', async ({ request }) => {
    const res = await request.post(BASE + '/api/challenges/test/start')
    expect(res.status()).toBe(401)
  })

  test('POST /api/challenges/:id/submit requires auth', async ({ request }) => {
    const res = await request.post(BASE + '/api/challenges/test/submit')
    expect(res.status()).toBe(401)
  })

  test('POST /api/challenges/:id/reset requires auth', async ({ request }) => {
    const res = await request.post(BASE + '/api/challenges/test/reset')
    expect(res.status()).toBe(401)
  })

  test('POST /api/generate requires auth', async ({ request }) => {
    const res = await request.post(BASE + '/api/generate', {
      data: {
        draft: {
          title: 'test',
          difficulty: 'easy',
          tags: ['test'],
          description: 'test',
          goal: 'test',
          symptoms: 'test',
          fault_mechanism: 'test',
          environment_shape: 'test',
          acceptance_criteria: 'test',
          difficulty_reason: 'test',
        },
      },
    })
    expect(res.status()).toBe(401)
  })

  test('POST /api/generate/draft requires auth', async ({ request }) => {
    const res = await request.post(BASE + '/api/generate/draft', {
      data: { topic: 'test' },
    })
    expect(res.status()).toBe(401)
  })

  test('GET /api/generate/jobs/:id requires auth', async ({ request }) => {
    const res = await request.get(BASE + '/api/generate/jobs/test-job')
    expect(res.status()).toBe(401)
  })

  test('POST /api/verify/submissions requires auth', async ({ request }) => {
    const res = await request.fetch(BASE + '/api/verify/submissions', {
      method: 'POST',
      multipart: {
        artifact: {
          name: 'demo.tar.gz',
          mimeType: 'application/gzip',
          buffer: Buffer.from('demo'),
        },
      },
    })
    expect(res.status()).toBe(401)
  })

  test('GET /api/openapi.json returns spec', async ({ request }) => {
    const res = await request.get(BASE + '/api/openapi.json')
    expect(res.ok()).toBeTruthy()
    const body = await res.json()
    expect(body.info.title).toBe('Breakfix API')
    expect(body.paths).toBeDefined()
  })

  test('POST /api/auth/login with wrong TOTP returns 401', async ({ request }) => {
    const user = uniqueUser('api-login')
    await request.post(BASE + '/api/auth/register', {
      data: { username: user, password: 'testpass123' },
    })
    const res = await request.post(BASE + '/api/auth/login', {
      data: { username: user, password: 'testpass123', totp_code: '000000' },
    })
    expect(res.status()).toBe(401)
  })
})

test.describe('Complete User Flow', () => {
  test('register → login → view list → logout', async ({ page }) => {
    await registerAndLogin(page)
    await expect(page.locator('.account-value')).toHaveText('Authenticated')
    await expect(page.locator('.challenge-list .challenge-card').first()).toBeVisible()
    await signOut(page)
    await expect(page.getByRole('button', { name: 'Sign In', exact: true })).toBeVisible()
  })

  test('re-login after logout works', async ({ page }) => {
    const { user } = await registerAndLogin(page, uniqueUser('relogin'))
    await signOut(page)

    await page.getByRole('button', { name: 'Sign In', exact: true }).click()
    await expect(page.locator('h3:has-text("Sign In")')).toBeVisible()
    await page.locator('input[placeholder="Username"]').first().fill(user)
    await page.locator('input[placeholder="Password"]').first().fill('testpass123')
    await expect(page.locator('input[placeholder="TOTP Code"]')).toBeVisible()
  })
})
