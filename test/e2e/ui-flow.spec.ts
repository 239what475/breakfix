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

async function registerAndLogin(page: any, user: string) {
  await page.goto(BASE + '/#/')
  await page.getByRole('button', { name: 'Register', exact: true }).click()
  await page.locator('input[placeholder="Username"]').first().fill(user)
  await page.locator('input[placeholder="Password (min 6 chars)"]').fill('testpass123')
  await page.getByRole('button', { name: 'Register' }).last().click()

  await expect(page.locator('.totp-secret code')).toBeVisible()
  const secret = (await page.locator('.totp-secret code').textContent()) ?? ''
  await page.getByRole('button', { name: 'Continue to Sign In' }).click()

  const code = await totpCode(page, secret)
  await page.locator('input[placeholder="Username"]').first().fill(user)
  await page.locator('input[placeholder="Password"]').first().fill('testpass123')
  await page.locator('input[placeholder="TOTP Code"]').fill(code)
  await page.getByRole('button', { name: 'Sign In' }).last().click()
}

async function selectCleanupLogs(page: any) {
  const challengeButton = page.getByRole('button', { name: /批量压缩旧日志/ })
  await expect(challengeButton).toBeVisible({ timeout: 10000 })
  await expect(page.getByRole('heading', { name: '批量压缩旧日志', exact: true }).first()).toBeVisible()
  await expect(page.locator('.brief-description').getByText('服务器磁盘空间不足')).toBeVisible()
  await challengeButton.click()
}

test.describe('UI Flow', () => {
  test('manual flow with real challenge', async ({ page }) => {
    test.setTimeout(180000)

    const user = `ui-flow-${Date.now()}`

    await registerAndLogin(page, user)
    await selectCleanupLogs(page)

    await page.locator('main').getByRole('button', { name: 'Start Challenge' }).click()

    await expect(page.locator('.terminal-frame')).toBeVisible({ timeout: 90000 })
    await expect(page.locator('.brief-actions').getByRole('button', { name: 'Submit', exact: true })).toBeEnabled()
    await expect(page.locator('.brief-actions').getByRole('button', { name: 'Reset', exact: true })).toBeEnabled()
    await expect(page.locator('.terminal-overlay')).toBeHidden({ timeout: 90000 })

    const terminalSurface = page.locator('.terminal-surface')
    await expect(terminalSurface).toBeVisible()
    await terminalSurface.click({ position: { x: 120, y: 120 } })
    await page.keyboard.type('echo codex-input-check', { delay: 20 })
    await page.keyboard.press('Enter')

    await expect(page.locator('.terminal-frame')).toContainText('codex-input-check', { timeout: 10000 })

    const hasPageScroll = await page.evaluate(() => {
      const el = document.scrollingElement
      if (!el) return true
      return el.scrollHeight > el.clientHeight
    })
    expect(hasPageScroll).toBe(false)

    await page.screenshot({ path: '/tmp/breakfix-terminal-live.png', fullPage: true })
  })

  test('resume reconnects to an existing running environment after disconnect', async ({ browser }) => {
    test.setTimeout(180000)

    const user = `resume-${Date.now()}`
    const context = await browser.newContext()
    const page = await context.newPage()

    await registerAndLogin(page, user)
    await selectCleanupLogs(page)
    await page.locator('main').getByRole('button', { name: 'Start Challenge' }).click()

    await expect(page.locator('.terminal-frame')).toBeVisible({ timeout: 90000 })
    await expect(page.locator('.brief-actions').getByRole('button', { name: 'Submit', exact: true })).toBeVisible()
    await expect(page.locator('.terminal-overlay')).toBeHidden({ timeout: 90000 })

    await page.close()
    await new Promise((resolve) => setTimeout(resolve, 1500))

    const resumePage = await context.newPage()
    await resumePage.goto(BASE + '/#/')
    await expect(resumePage.locator('.account-value').getByText('Authenticated')).toBeVisible({ timeout: 10000 })
    await expect(resumePage.getByRole('button', { name: /批量压缩旧日志/ })).toBeVisible({ timeout: 10000 })
    await expect(resumePage.locator('.brief-actions').getByRole('button', { name: 'Resume Session', exact: true })).toBeVisible()

    await resumePage.locator('.brief-actions').getByRole('button', { name: 'Resume Session', exact: true }).click()
    await expect(resumePage.locator('.terminal-frame')).toBeVisible({ timeout: 90000 })
    await expect(resumePage.locator('.terminal-overlay')).toBeHidden({ timeout: 90000 })
    await expect(resumePage.locator('.brief-actions').getByRole('button', { name: 'Submit', exact: true })).toBeVisible()

    await context.close()
  })

  test('resume restores the same tmux shell session after disconnect', async ({ browser }) => {
    test.setTimeout(180000)

    const user = `tmux-${Date.now()}`
    const context = await browser.newContext()
    const page = await context.newPage()

    await registerAndLogin(page, user)
    await selectCleanupLogs(page)
    await page.locator('main').getByRole('button', { name: 'Start Challenge' }).click()

    await expect(page.locator('.terminal-frame')).toBeVisible({ timeout: 90000 })
    await expect(page.locator('.terminal-overlay')).toBeHidden({ timeout: 90000 })

    const terminalSurface = page.locator('.terminal-surface')
    await terminalSurface.click({ position: { x: 120, y: 120 } })
    await page.keyboard.type('export BREAKFIX_TMUX_CHECK=sticky && cd /var && pwd', { delay: 20 })
    await page.keyboard.press('Enter')
    await page.waitForTimeout(1200)

    await page.close()
    await new Promise((resolve) => setTimeout(resolve, 1500))

    const resumePage = await context.newPage()
    await resumePage.goto(BASE + '/#/')
    await expect(resumePage.locator('.account-value').getByText('Authenticated')).toBeVisible({ timeout: 10000 })
    await resumePage.locator('.brief-actions').getByRole('button', { name: 'Resume Session', exact: true }).click()
    await expect(resumePage.locator('.terminal-frame')).toBeVisible({ timeout: 90000 })
    await expect(resumePage.locator('.terminal-overlay')).toBeHidden({ timeout: 90000 })

    const resumedSurface = resumePage.locator('.terminal-surface')
    await resumedSurface.click({ position: { x: 120, y: 120 } })
    await resumePage.keyboard.type('printf "%s:%s\\n" "$BREAKFIX_TMUX_CHECK" "$PWD"', { delay: 20 })
    await resumePage.keyboard.press('Enter')
    await expect(resumePage.locator('.terminal-frame')).toContainText('sticky:/var', { timeout: 10000 })

    await resumePage.screenshot({ path: '/tmp/breakfix-terminal-tmux-resume.png', fullPage: true })
    await context.close()
  })

  test('terminal stays connected while idle after attach', async ({ page }) => {
    test.setTimeout(180000)

    const user = `idle-${Date.now()}`

    await registerAndLogin(page, user)
    await selectCleanupLogs(page)
    await page.locator('main').getByRole('button', { name: 'Start Challenge' }).click()

    await expect(page.locator('.terminal-frame')).toBeVisible({ timeout: 90000 })
    await expect(page.locator('.terminal-overlay')).toBeHidden({ timeout: 90000 })

    await page.waitForTimeout(12000)

    await expect(page.locator('.workspace-header')).not.toContainText('Session disconnected')
    await expect(page.locator('.terminal-frame')).not.toContainText('Disconnected')
    await expect(page.locator('.terminal-frame')).toContainText(/root@challenge-|tmux/, { timeout: 5000 })

    await page.screenshot({ path: '/tmp/breakfix-terminal-idle.png', fullPage: true })
  })

  test('draft review and generation job flow works from the UI', async ({ page }) => {
    test.setTimeout(240000)

    const user = `draft-${Date.now()}`
    await registerAndLogin(page, user)

    await page.getByRole('button', { name: 'Generate Challenge', exact: true }).click()
    await expect(page.locator('.generate-card').getByText('Generate Challenge')).toBeVisible()

    await page.locator('textarea').fill('Create a Linux debugging challenge where an on-call engineer must investigate disk pressure caused by stale compressed backups and restore safe cleanup automation.')
    await page.getByRole('button', { name: 'Review with Agent', exact: true }).click()

    await expect(page.locator('.generate-card')).toContainText('Step 2. Review the draft', { timeout: 120000 })
    await expect(page.locator('.draft-form-grid')).toBeVisible()

    await page.locator('.draft-field:has(label:text("Description")) textarea').fill('你是值班工程师，需要排查磁盘压力并修复不安全的清理流程。')
    await page.locator('.draft-field:has(label:text("Notes")) textarea').fill('Favor layered investigation through shell tools before the root cause becomes obvious.')
    await page.locator('.tag-editor input').fill('storage')
    await page.getByRole('button', { name: 'Add', exact: true }).click()

    await page.getByRole('button', { name: 'Generate Challenge', exact: true }).last().click()

    await expect(page.locator('.job-status-card')).toBeVisible({ timeout: 15000 })
    await expect(page.locator('.job-status-card')).toContainText(/queued|running|success|failed/)
    await expect(page.locator('.job-status-card')).toContainText(/Job gen-/)
    await expect(page.locator('.job-status-card')).toContainText(/building and verifying challenge|challenge .* generated|generation failed/i, { timeout: 30000 })

    await page.waitForTimeout(8000)
    const jobText = (await page.locator('.job-status-card').textContent()) ?? ''
    expect(jobText).toMatch(/running|success|failed/i)

    if (/success/i.test(jobText)) {
      await expect(page.locator('.challenge-list .challenge-card').first()).toBeVisible()
    }
  })
})
