import { expect, test, type Page } from "@playwright/test";

const liveTest = process.env.RUN_LIVE_E2E === "1" ? test : test.skip;
const generationLiveTest =
  process.env.RUN_GENERATION_E2E === "1" ? test : test.skip;
const screenshotDir = process.env.CAPTURE_E2E_SCREENSHOTS;

async function captureWorkspace(page: Page, name: string) {
  if (!screenshotDir) return;
  await page.screenshot({ path: `${screenshotDir}/${name}.png` });
}

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

async function registerAndLogin(page: Page) {
  const username = `workspace-${Date.now()}-${Math.random().toString(36).slice(2, 7)}`;
  await page.goto("/");
  await page.getByRole("button", { name: "Register", exact: true }).click();
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

async function expectViewportWithoutPageOverflow(page: Page) {
  const viewport = await page.evaluate(() => ({
    scrollHeight: document.documentElement.scrollHeight,
    innerHeight: window.innerHeight,
    scrollWidth: document.documentElement.scrollWidth,
    innerWidth: window.innerWidth,
  }));
  expect(viewport.scrollHeight).toBeLessThanOrEqual(viewport.innerHeight);
  expect(viewport.scrollWidth).toBeLessThanOrEqual(viewport.innerWidth);
}

test("guest can browse the public catalog without page overflow", async ({
  page,
}) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.goto("/");

  await expect(
    page.getByRole("button", { name: /批量压缩旧日志/ }),
  ).toBeVisible();
  await page.getByRole("button", { name: /批量压缩旧日志/ }).click();
  await expect(
    page.getByRole("heading", { name: "批量压缩旧日志", exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("button", { name: "Sign in to start", exact: true }),
  ).toBeVisible();
  await expectViewportWithoutPageOverflow(page);
});

test("narrow catalog has no overflow", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto("/");

  await expect(
    page.getByRole("button", { name: /批量压缩旧日志/ }),
  ).toBeVisible();
  await expectViewportWithoutPageOverflow(page);
});

liveTest(
  "cleanup-logs runs through the component workbench",
  async ({ page }) => {
    test.setTimeout(180_000);
    await page.setViewportSize({ width: 1440, height: 900 });
    await registerAndLogin(page);

    await page.getByRole("button", { name: /批量压缩旧日志/ }).click();
    await page
      .getByRole("button", { name: "Start challenge", exact: true })
      .click();
    await expect(page.getByText("Connected", { exact: true })).toBeVisible({
      timeout: 90_000,
    });
    await expect(page.locator(".markdown-document")).toContainText(
      "批量压缩旧日志",
    );
    await page.getByRole("button", { name: "Solution", exact: true }).click();
    await expect(page.locator(".document-pane > .markdown-document")).toContainText(
      "解答：批量压缩旧日志",
    );
    await page.getByRole("button", { name: "Problem", exact: true }).click();
    await page.getByRole("button", { name: /创建清理脚本/ }).click();
    await expect(page.locator(".hint-panel")).toContainText("脚本路径");
    await page.getByTitle("Collapse sidebar").click();
    await page.getByRole("button", { name: "Show Solution" }).click();
    await expect(page.locator(".document-pane > .markdown-document")).toContainText(
      "解答：批量压缩旧日志",
    );
    await page.getByRole("button", { name: "Show Problem" }).click();
    await page.getByRole("button", { name: "Expand checkpoints" }).click();
    await captureWorkspace(page, "workspace-desktop");

    await page.getByRole("button", { name: "New terminal" }).click();
    await expect(
      page.getByRole("button", { name: "shell-2", exact: true }),
    ).toBeVisible();
    await page.getByRole("button", { name: "Close shell-2" }).click();
    await expect(
      page.getByRole("button", { name: "shell-2", exact: true }),
    ).toHaveCount(0);

    await page.setViewportSize({ width: 800, height: 900 });
    await expect(page.locator(".document-pane")).toBeVisible();
    await expect(page.locator(".terminal-pane")).toBeHidden();
    await expectViewportWithoutPageOverflow(page);
    await captureWorkspace(page, "workspace-narrow-documents");
    await page.getByRole("button", { name: "Terminal", exact: true }).click();
    await expect(page.locator(".terminal-pane")).toBeVisible();
    await expect(page.locator(".document-pane")).toBeHidden();
    await expectViewportWithoutPageOverflow(page);
    await captureWorkspace(page, "workspace-narrow-terminal");
    await page.setViewportSize({ width: 1440, height: 900 });

    await page.locator(".terminal-host").click();
    await page.keyboard.type("/answer.sh");
    await page.keyboard.press("Enter");
    await expect(
      page.getByText("All checkpoints complete", { exact: true }),
    ).toBeVisible({ timeout: 45_000 });

    await page.evaluate(async () => {
      const response = await fetch("/api/challenges/cleanup-logs/stop", {
        method: "POST",
        headers: {
          Authorization: `Bearer ${localStorage.getItem("token") ?? ""}`,
        },
      });
      if (!response.ok) throw new Error(await response.text());
    });
  },
);

liveTest(
  "vcluster workspace reports checkpoint progress",
  async ({ page }) => {
    test.setTimeout(8 * 60_000);
    await page.setViewportSize({ width: 1440, height: 900 });
    await registerAndLogin(page);

    await page.getByRole("button", { name: /修复错误的 Deployment 镜像/ }).click();
    await page
      .getByRole("button", { name: "Start challenge", exact: true })
      .click();
    await expect(page.getByText("Connected", { exact: true })).toBeVisible({
      timeout: 5 * 60_000,
    });
    await expect(
      page.getByText("Deployment 和 Service 均存在", { exact: true }),
    ).toBeVisible({ timeout: 60_000 });

    await page.evaluate(async () => {
      const response = await fetch(
        "/api/challenges/fix-broken-deployment-image/stop",
        {
          method: "POST",
          headers: {
            Authorization: `Bearer ${localStorage.getItem("token") ?? ""}`,
          },
        },
      );
      if (!response.ok) throw new Error(await response.text());
    });
  },
);

generationLiveTest(
  "a generated challenge is published and completes in the workbench",
  async ({ page }) => {
    test.setTimeout(65 * 60_000);
    await page.setViewportSize({ width: 1440, height: 900 });
    await registerAndLogin(page);

    await page.getByRole("button", { name: "Generate", exact: true }).click();
    const dialog = page.getByRole("dialog");
    await dialog
      .locator("textarea")
      .first()
      .fill(
        "请创建一道 container 题：服务配置文件 /etc/breakfix/app.env 中的 APP_PORT 被错误设置为 9090，" +
          "而 /usr/local/bin/healthcheck 只会在 APP_PORT=8080 时成功。用户需要修复配置，" +
          "并验证 healthcheck 成功。题目必须包含配置修复和健康检查两个公开检查点。",
      );
    await dialog
      .getByRole("button", { name: "Review idea", exact: true })
      .click();
    await expect(
      dialog.getByRole("button", { name: "Generate challenge", exact: true }),
    ).toBeVisible({ timeout: 5 * 60_000 });
    await dialog
      .getByRole("button", { name: "Generate challenge", exact: true })
      .click();
    await expect(dialog.locator(".job-panel")).toContainText("success", {
      timeout: 60 * 60_000,
    });
    await dialog.getByRole("button", { name: "Done", exact: true }).click();

    await page
      .getByRole("button", { name: "Start challenge", exact: true })
      .click();
    await expect(page.getByText("Connected", { exact: true })).toBeVisible({
      timeout: 5 * 60_000,
    });
    await page.locator(".terminal-host").click();
    await page.keyboard.type("/answer.sh");
    await page.keyboard.press("Enter");
    await expect
      .poll(async () =>
        page.locator(".progress-count").evaluate((element) => {
          const match = /^(\d+) \/ (\d+) complete$/.exec(
            element.textContent?.trim() ?? "",
          );
          return match && match[1] === match[2] && match[2] !== "0";
        }),
      )
      .toBe(true);
    await expect(
      page.getByText("All checkpoints complete", { exact: true }),
    ).toBeVisible({ timeout: 60_000 });
    await page.evaluate(async () => {
      const headers = {
        Authorization: `Bearer ${localStorage.getItem("token") ?? ""}`,
      };
      const catalog = await fetch("/api/challenges", { headers }).then(
        async (response) => {
          if (!response.ok) throw new Error(await response.text());
          return response.json() as Promise<{
            challenges: Array<{ id: string; active: boolean }>;
          }>;
        },
      );
      const active = catalog.challenges.find((challenge) => challenge.active);
      if (!active) throw new Error("generated challenge environment not found");
      const response = await fetch(`/api/challenges/${active.id}/stop`, {
        method: "POST",
        headers,
      });
      if (!response.ok) throw new Error(await response.text());
    });
  },
);
