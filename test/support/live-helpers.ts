import { expect, type Page } from "@playwright/test";
import { nodeRuntimeFixture } from "./catalog-fixture";

export type StartedScenario = {
	id: string;
	title: string;
};

type ActiveEnvironment = {
	environment_id: string;
	scenario: { id: string };
};

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

export function scenarioCard(page: Page, title: string) {
	return page.getByRole("article", { name: `Scenario: ${title}`, exact: true });
}

export function scenarioCardByID(page: Page, scenarioID: string) {
	return page.getByTestId(`catalog-scenario-${scenarioID}`);
}

export async function startScenarioFromCatalog(page: Page, title: string): Promise<StartedScenario> {
	const card = scenarioCard(page, title);
	const id = await card.getAttribute("data-scenario-id");
	if (!id) throw new Error(`catalog scenario ${title} is missing its published ID`);
	await card.getByRole("button", { name: "Start scenario", exact: true }).click();
	return { id, title };
}

export async function activeEnvironmentName(page: Page, scenarioID: string): Promise<string> {
	const read = () =>
		page.evaluate(async (id) => {
			const response = await fetch("/api/me/space", {
				headers: { Authorization: `Bearer ${localStorage.getItem("token") ?? ""}` },
			});
			if (!response.ok) throw new Error(await response.text());
			const body = (await response.json()) as { active_environments: ActiveEnvironment[] };
			return body.active_environments.find((environment) => environment.scenario.id === id)?.environment_id ?? "";
		}, scenarioID);
	await expect.poll(read, { timeout: 90_000, intervals: [500, 1_000, 2_000, 5_000] }).not.toBe("");
	return read();
}

export async function expectTerminalConnected(page: Page) {
	await expect(page.locator(".terminal-status").getByText("Connected", { exact: true })).toBeVisible({
		timeout: 90_000,
	});
}

export async function reconnectTerminal(page: Page) {
	const status = page.locator(".terminal-status");
	const connected = status.getByText("Connected", { exact: true });
	const overlayReconnect = page
		.locator(".terminal-disconnected")
		.getByRole("button", { name: "Reconnect", exact: true });
	const statusReconnect = status.getByRole("button", { name: "Reconnect", exact: true });

	await expect.poll(async () => {
		if (await connected.isVisible().catch(() => false)) return true;
		if (await overlayReconnect.isVisible().catch(() => false)) {
			await overlayReconnect.click();
		} else if (await statusReconnect.isVisible().catch(() => false)) {
			await statusReconnect.click();
		}
		return connected.isVisible().catch(() => false);
	}, {
		timeout: 90_000,
		intervals: [250, 500, 1_000, 2_000, 5_000],
	}).toBe(true);
}

export async function runTerminalCommand(page: Page, command: string) {
	await page.getByRole("textbox", { name: "Terminal input" }).focus();
	await page.keyboard.type(command);
	await page.keyboard.press("Enter");
}

export async function runNodeRuntimeFixtureAnswer(page: Page) {
	await runTerminalCommand(page, nodeRuntimeFixture.answerCommand);
}

export async function stopScenario(page: Page, scenarioID: string) {
	await page.evaluate(async (id) => {
		const response = await fetch(`/api/operations/scenarios/${id}/stop`, {
			method: "POST",
			headers: { Authorization: `Bearer ${localStorage.getItem("token") ?? ""}` },
		});
		if (!response.ok) throw new Error(await response.text());
	}, scenarioID);
}
