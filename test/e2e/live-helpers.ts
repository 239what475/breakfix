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
