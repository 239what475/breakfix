import { expect, type Page } from "@playwright/test";

async function totpCode(page: Page, secret: string): Promise<string> {
  return page.evaluate(async (value: string) => {
    const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567";
    const bytes: number[] = [];
    let bits = 0;
    let accumulator = 0;
    for (const char of value) {
      accumulator = (accumulator << 5) | alphabet.indexOf(char);
      bits += 5;
      if (bits >= 8) {
        bytes.push((accumulator >>> (bits - 8)) & 0xff);
        bits -= 8;
      }
    }
    const counter = Math.floor(Date.now() / 30_000);
    const buffer = new ArrayBuffer(8);
    new DataView(buffer).setBigUint64(0, BigInt(counter), false);
    const key = await crypto.subtle.importKey(
      "raw",
      new Uint8Array(bytes),
      { name: "HMAC", hash: "SHA-1" },
      false,
      ["sign"],
    );
    const hash = new Uint8Array(await crypto.subtle.sign("HMAC", key, buffer));
    const offset = hash[hash.length - 1] & 0x0f;
    const code =
      (((hash[offset] & 0x7f) << 24) |
        (hash[offset + 1] << 16) |
        (hash[offset + 2] << 8) |
        hash[offset + 3]) %
      1_000_000;
    return String(code).padStart(6, "0");
  }, secret);
}

export async function registerAndLogin(page: Page, openRegistration = true) {
  const username = `workspace-${Date.now()}-${Math.random().toString(36).slice(2, 7)}`;
  if (openRegistration) {
    await page.goto("/");
    await page.getByRole("button", { name: "Register", exact: true }).click();
  } else {
    await page.getByRole("dialog").getByRole("button", { name: "Create one", exact: true }).click();
  }
  await page.locator('input[autocomplete="username"]').fill(username);
  await page
    .locator('input[autocomplete="new-password"]')
    .fill("test-password-123");
  await page.getByRole("button", { name: "Continue", exact: true }).click();
  const secret = await page.locator(".totp-setup code").textContent();
  await page
    .getByRole("button", { name: "Continue to sign in", exact: true })
    .click();
  await page.locator('input[autocomplete="username"]').fill(username);
  await page
    .locator('input[autocomplete="current-password"]')
    .fill("test-password-123");
  await page
    .locator('input[autocomplete="one-time-code"]')
    .fill(await totpCode(page, secret ?? ""));
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "Sign in", exact: true })
    .click();
}

export function challengeCard(page: Page, title: string) {
  return page.locator("article.challenge-card", {
    has: page.getByRole("heading", { name: title, exact: true }),
  });
}

export function challengeCardByID(page: Page, challengeID: string) {
  return page.locator(`article.challenge-card[data-challenge-id="${challengeID}"]`);
}

export async function startChallengeFromCatalog(page: Page, title: string) {
  await challengeCard(page, title)
    .getByRole("button", { name: "Start challenge", exact: true })
    .click();
}

export async function expectTerminalConnected(page: Page) {
	await expect(page.getByText("Connected", { exact: true })).toBeVisible({
		timeout: 90_000,
	});
}

const screenshotDir = process.env.CAPTURE_E2E_SCREENSHOTS;

export async function captureWorkspace(page: Page, name: string) {
	if (!screenshotDir) return;
	await page.screenshot({ path: `${screenshotDir}/${name}.png` });
}

export async function expectViewportWithoutPageOverflow(page: Page) {
	const viewport = await page.evaluate(() => ({
		scrollHeight: document.documentElement.scrollHeight,
		innerHeight: window.innerHeight,
		scrollWidth: document.documentElement.scrollWidth,
		innerWidth: window.innerWidth,
	}));
	expect(viewport.scrollHeight).toBeLessThanOrEqual(viewport.innerHeight);
	expect(viewport.scrollWidth).toBeLessThanOrEqual(viewport.innerWidth);
}

export async function expectElementsWithinViewport(page: Page, selector: string) {
	const bounds = await page.locator(selector).evaluateAll((elements) =>
		elements.map((element) => {
			const rect = element.getBoundingClientRect();
			return { left: rect.left, right: rect.right, top: rect.top, bottom: rect.bottom };
		}),
	);
	for (const bound of bounds) {
		expect(bound.left).toBeGreaterThanOrEqual(0);
		expect(bound.right).toBeLessThanOrEqual(await page.evaluate(() => innerWidth));
		expect(bound.top).toBeGreaterThanOrEqual(0);
		expect(bound.bottom).toBeLessThanOrEqual(await page.evaluate(() => innerHeight));
	}
}

export async function runTerminalCommand(page: Page, command: string) {
	await page.getByRole("textbox", { name: "Terminal input" }).focus();
	await page.keyboard.type(command);
	await page.keyboard.press("Enter");
}

export async function runAnswer(page: Page) {
	await runTerminalCommand(page, "/answer.sh");
}

export async function stopChallenge(page: Page, challengeID: string) {
	await page.evaluate(async (id) => {
		const response = await fetch(`/api/challenges/${id}/stop`, {
			method: "POST",
			headers: { Authorization: `Bearer ${localStorage.getItem("token") ?? ""}` },
		});
		if (!response.ok) throw new Error(await response.text());
	}, challengeID);
}

export async function waitForVerifiedRevision(page: Page) {
	await expect(
		page.getByRole("button", { name: "发布挑战", exact: true }),
	).toBeVisible({ timeout: 50 * 60_000 });
}
