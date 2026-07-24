import { expect, test, type Page } from "@playwright/test";
import {
  challengeCard,
  registerAndLogin,
  startChallengeFromCatalog,
} from "./live-helpers";

const liveTest = process.env.RUN_LIVE_E2E === "1" ? test : test.skip;
const generationLiveTest =
  process.env.RUN_GENERATION_E2E === "1" ? test : test.skip;
const screenshotDir = process.env.CAPTURE_E2E_SCREENSHOTS;

async function captureWorkspace(page: Page, name: string) {
  if (!screenshotDir) return;
  await page.screenshot({ path: `${screenshotDir}/${name}.png` });
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

async function expectElementsWithinViewport(
  page: Page,
  selector: string,
) {
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

async function runAnswer(page: Page) {
  // xterm renders the visible terminal in a div and receives keyboard input
  // through its own accessible textarea. Focus that input, then use the page
  // keyboard exactly as a user would.
  await page.getByRole("textbox", { name: "Terminal input" }).focus();
  await page.keyboard.type("/answer.sh");
  await page.keyboard.press("Enter");
}

async function runTerminalCommand(page: Page, command: string) {
  await page.getByRole("textbox", { name: "Terminal input" }).focus();
  await page.keyboard.type(command);
  await page.keyboard.press("Enter");
}

async function waitForVerifiedRevision(page: Page) {
  await expect(
    page.getByRole("button", { name: "发布挑战", exact: true }),
  ).toBeVisible({ timeout: 50 * 60_000 });
}

test("guest can filter, sort, and browse the public catalog without page overflow", async ({
  page,
}) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  const contentRequests: string[] = [];
  page.on("request", (request) => {
    if (request.url().includes("/content")) contentRequests.push(request.url());
  });
  await page.goto("/");

  await expect(challengeCard(page, "批量压缩旧日志")).toBeVisible();
  await expect(challengeCard(page, "修复错误的 Deployment 镜像")).toBeVisible();
  expect(contentRequests).toEqual([]);

  await page.getByRole("textbox", { name: "Search challenges" }).fill("deployment");
  await expect(challengeCard(page, "批量压缩旧日志")).toHaveCount(0);
  await expect(challengeCard(page, "修复错误的 Deployment 镜像")).toBeVisible();

  await page.getByRole("textbox", { name: "Search challenges" }).fill("");
  await page.locator(".catalog-filters").getByLabel("linux", { exact: true }).check();
  await expect(challengeCard(page, "批量压缩旧日志")).toBeVisible();
  await expect(challengeCard(page, "修复错误的 Deployment 镜像")).toHaveCount(0);
  await page.locator(".catalog-filters").getByLabel("VCluster", { exact: true }).check();
  await expect(page.getByText("No challenges match these filters.", { exact: true })).toBeVisible();
  await page.getByRole("button", { name: "Reset filters", exact: true }).click();

  await expect(challengeCard(page, "批量压缩旧日志")).toBeVisible();
  await page.getByLabel("Sort challenges").selectOption("oldest");
  await expect(page.locator("article.challenge-card h2").allTextContents()).resolves.toEqual([
    "修复错误的 Deployment 镜像",
    "批量压缩旧日志",
  ]);
  await page.getByLabel("Sort challenges").selectOption("newest");
  await expect(page.locator("article.challenge-card h2").allTextContents()).resolves.toEqual([
    "批量压缩旧日志",
    "修复错误的 Deployment 镜像",
  ]);

  await challengeCard(page, "批量压缩旧日志")
    .getByRole("button", { name: "Start challenge", exact: true })
    .click();
  await expect(page.getByRole("dialog")).toBeVisible();
  await expectViewportWithoutPageOverflow(page);
});

test("narrow catalog has no overflow", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto("/");

  await expect(page.getByRole("button", { name: "Filters", exact: true })).toBeVisible();
  await page.getByRole("button", { name: "Filters", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Find a challenge", exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: "Start challenge", exact: true })).toHaveCount(0);
  await expectViewportWithoutPageOverflow(page);
});

test("authenticated learner can navigate the responsive My space shell", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await registerAndLogin(page);
  await expect(page.locator(".app-topbar:visible")).toHaveCount(1);
  await expect(page.getByRole("navigation", { name: "Primary" })).toBeVisible();
  await expect(page.getByRole("button", { name: "Catalog", exact: true })).toHaveAttribute("aria-current", "page");
  await expect(page.getByRole("button", { name: "Challenge studio", exact: true })).toBeVisible();
  await captureWorkspace(page, "catalog-authenticated-desktop");
  await page.getByRole("button", { name: "My space", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Your learning space", exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: "My space", exact: true })).toHaveAttribute("aria-current", "page");
  await expect(page.getByText("Active environments", { exact: true })).toBeVisible();
  await page.getByRole("button", { name: "Learning", exact: true }).first().click();
  await expect(page.getByRole("heading", { name: "Learning history", exact: true })).toBeVisible();
  const stateRequest = page.waitForRequest((request) => {
    const url = new URL(request.url());
    return url.pathname === "/api/me/space/learning" && url.searchParams.get("state") === "completed";
  });
  await page.getByLabel("Filter learning state").selectOption("completed");
  await stateRequest;
  await expect(page.getByLabel("Filter learning state")).toHaveValue("completed");
  const runtimeRequest = page.waitForRequest((request) => {
    const url = new URL(request.url());
    return url.pathname === "/api/me/space/learning" && url.searchParams.get("runtime") === "container";
  });
  await page.getByLabel("Filter learning runtime").selectOption("container");
  await runtimeRequest;
  await expect(page.getByLabel("Filter learning runtime")).toHaveValue("container");
  await page.getByRole("button", { name: "Authoring", exact: true }).first().click();
  await expect(page.getByRole("heading", { name: "Challenge authoring", exact: true })).toBeVisible();
  await page.getByRole("button", { name: "Open studio", exact: true }).click();
  await expect(page.getByLabel("Challenge authoring workspace")).toBeVisible();
  await expect(page.locator(".app-topbar:visible")).toBeVisible();
  await expect(page.getByRole("navigation", { name: "Primary" })).toBeVisible();
  await expect(page.getByRole("button", { name: "Challenge studio", exact: true })).toHaveAttribute("aria-current", "page");
  await captureWorkspace(page, "authoring-shell-desktop");
  await page.getByRole("button", { name: "My space", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Challenge authoring", exact: true })).toBeVisible();
  await expect(page.locator(".authoring-draft")).toHaveCount(1);
  await page.getByRole("button", { name: "Challenge studio", exact: true }).click();
  await expect(page.getByLabel("Challenge authoring workspace")).toBeVisible();
  await page.getByRole("button", { name: "My space", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Challenge authoring", exact: true })).toBeVisible();
  const draftRequest = page.waitForRequest((request) => {
    const url = new URL(request.url());
    return request.method() === "GET" && /^\/api\/authoring\/sessions\/[^/]+$/.test(url.pathname);
  });
  await page.locator(".authoring-draft").click();
  await draftRequest;
  await expect(page.getByLabel("Challenge authoring workspace")).toBeVisible();
  await page.getByRole("button", { name: "My space", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Challenge authoring", exact: true })).toBeVisible();
  await expectViewportWithoutPageOverflow(page);
  await expectElementsWithinViewport(page, ".app-topbar:visible, .my-space-layout");
  await captureWorkspace(page, "my-space-desktop");

  await page.setViewportSize({ width: 390, height: 844 });
  await expect(page.getByRole("button", { name: "Overview", exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: "Authoring", exact: true })).toHaveCount(0);
  await page.getByRole("button", { name: "Navigation", exact: true }).click();
  await expect(page.getByRole("navigation", { name: "Mobile primary" })).toBeVisible();
  await expect(page.getByRole("button", { name: "Challenge studio", exact: true })).toHaveCount(0);
  await captureWorkspace(page, "my-space-mobile-menu");
  await page.getByRole("navigation", { name: "Mobile primary" }).getByRole("button", { name: "Catalog", exact: true }).click();
  await expect(challengeCard(page, "批量压缩旧日志")).toBeVisible();
  await expect(page.getByRole("button", { name: "Start challenge", exact: true })).toHaveCount(0);
  await expectViewportWithoutPageOverflow(page);
  await captureWorkspace(page, "catalog-mobile");
});

liveTest("catalog preserves completion after the challenge environment is stopped", async ({ page }) => {
  test.setTimeout(4 * 60_000);
  await page.setViewportSize({ width: 1440, height: 900 });
  await registerAndLogin(page);

  await startChallengeFromCatalog(page, "批量压缩旧日志");
  await expect(page.getByText("Connected", { exact: true })).toBeVisible({
    timeout: 90_000,
  });
  await page.getByRole("button", { name: "Catalog", exact: true }).click();
  const cleanupCard = challengeCard(page, "批量压缩旧日志");
  const activeState = cleanupCard.locator(".challenge-state.in-progress");
  await expect(activeState).toContainText("In progress", {
    timeout: 30_000,
  });
  await expect(activeState).toContainText(/\d\/3 checkpoints/);

  await startChallengeFromCatalog(page, "批量压缩旧日志");
  await expect(page.getByText("Connected", { exact: true })).toBeVisible({
    timeout: 90_000,
  });
  await runAnswer(page);
  await expect(
    page.getByText("All checkpoints complete", { exact: true }),
  ).toBeVisible({ timeout: 60_000 });

  await page.getByRole("button", { name: "Catalog", exact: true }).click();
  await expect(
    cleanupCard.getByText("Completed", { exact: true }),
  ).toBeVisible({ timeout: 30_000 });

  await page.evaluate(async () => {
    const response = await fetch("/api/challenges/cleanup-logs/stop", {
      method: "POST",
      headers: {
        Authorization: `Bearer ${localStorage.getItem("token") ?? ""}`,
      },
    });
    if (!response.ok) throw new Error(await response.text());
  });
  await page.reload();
  await expect(
    challengeCard(page, "批量压缩旧日志").getByText("Completed", { exact: true }),
  ).toBeVisible({ timeout: 30_000 });
});

liveTest("workbench keeps the environment active when opening Challenge studio", async ({ page }) => {
  test.setTimeout(4 * 60_000);
  await page.setViewportSize({ width: 1440, height: 900 });
  await registerAndLogin(page);
  await startChallengeFromCatalog(page, "批量压缩旧日志");
  await expect(page.getByText("Connected", { exact: true })).toBeVisible({
    timeout: 90_000,
  });

  await page.getByRole("button", { name: "Challenge studio", exact: true }).click();
  await expect(page.getByLabel("Challenge authoring workspace")).toBeVisible();
  await expect(page.getByRole("button", { name: "Challenge studio", exact: true })).toHaveAttribute("aria-current", "page");
  await page.getByRole("button", { name: "Catalog", exact: true }).click();

  const cleanupCard = challengeCard(page, "批量压缩旧日志");
  await expect(cleanupCard.locator(".challenge-state.in-progress")).toContainText(
    "In progress",
    { timeout: 30_000 },
  );

  await page.evaluate(async () => {
    const response = await fetch("/api/challenges/cleanup-logs/stop", {
      method: "POST",
      headers: { Authorization: `Bearer ${localStorage.getItem("token") ?? ""}` },
    });
    if (!response.ok) throw new Error(await response.text());
  });
});

liveTest("My space records the real terminal and checkpoint lifecycle", async ({ page }) => {
  test.setTimeout(10 * 60_000);
  await page.setViewportSize({ width: 1440, height: 900 });
  await registerAndLogin(page);
  await startChallengeFromCatalog(page, "批量压缩旧日志");
  await expect(page.getByText("Connected", { exact: true })).toBeVisible({ timeout: 90_000 });
  await page.waitForTimeout(1200);
  await page.getByRole("button", { name: "Catalog", exact: true }).click();
  await page.getByRole("button", { name: "My space", exact: true }).click();
  await expect(page.getByText("Active environments", { exact: true })).toBeVisible();
  await expect(page.locator(".active-environment-row")).toContainText("批量压缩旧日志", { timeout: 30_000 });
  await expect(page.locator(".active-environment-row")).toContainText(/\d\/3 checkpoints/);
  await captureWorkspace(page, "my-space-live-active");
  const startedSpace = await page.evaluate(async () => {
    const response = await fetch("/api/me/space", { headers: { Authorization: `Bearer ${localStorage.getItem("token") ?? ""}` } });
    if (!response.ok) throw new Error(await response.text());
    return response.json() as Promise<{ summary: { attempted_count: number; terminal_learning_seconds: number } }>;
  });
  expect(startedSpace.summary.attempted_count).toBe(1);
  expect(startedSpace.summary.terminal_learning_seconds).toBeGreaterThan(0);

  await page.locator(".active-environment-row").getByRole("button", { name: "Start challenge", exact: true }).click();
  await expect(page.getByText("Connected", { exact: true })).toBeVisible({ timeout: 90_000 });
  await runAnswer(page);
  await expect(page.getByText("All checkpoints complete", { exact: true })).toBeVisible({ timeout: 60_000 });
  await page.getByRole("button", { name: "Catalog", exact: true }).click();
  await page.getByRole("button", { name: "My space", exact: true }).click();
  const learningRow = page.locator(".history-row", { hasText: "批量压缩旧日志" });
  await expect(learningRow).toContainText("Completed", { timeout: 30_000 });
  await captureWorkspace(page, "my-space-live-completed");

  await page.evaluate(async () => {
    const response = await fetch("/api/challenges/cleanup-logs/stop", {
      method: "POST",
      headers: { Authorization: `Bearer ${localStorage.getItem("token") ?? ""}` },
    });
    if (!response.ok) throw new Error(await response.text());
  });
  await page.reload();
  await page.getByRole("button", { name: "My space", exact: true }).click();
  await expect(page.locator(".history-row", { hasText: "批量压缩旧日志" })).toContainText("Completed", { timeout: 30_000 });
  await expect(page.getByText("Attempt ended", { exact: true })).toHaveCount(0);
});

liveTest(
  "cleanup-logs runs through the component workbench",
  async ({ page }) => {
    test.setTimeout(10 * 60_000);
    await page.setViewportSize({ width: 1440, height: 900 });
    await page.goto("/");
    await startChallengeFromCatalog(page, "批量压缩旧日志");
    await expect(page.getByRole("dialog")).toBeVisible();
    await registerAndLogin(page, false);
    await expect(page.getByText("Connected", { exact: true })).toBeVisible({
      timeout: 90_000,
    });
    const terminalMarker = `BREAKFIX_ASSISTANT_SCROLLBACK_${Date.now()}`;
    const readonlyMarker = `assistant-readonly-${Date.now()}`;
    await runTerminalCommand(
      page,
      `printf '%s\\n' '${terminalMarker}'`,
    );
    await runTerminalCommand(
      page,
      `printf '%s' '${readonlyMarker}' > /tmp/breakfix-assistant-readonly-proof`,
    );
    await expect(page.locator(".xterm-rows")).toContainText(terminalMarker);

    await page.getByRole("button", { name: "Assistant", exact: true }).click();
    const assistantComposer = page.getByRole("textbox", {
      name: "Assistant message",
    });
    await expect(assistantComposer).toBeVisible();
    await assistantComposer.fill(
      "这是一次完整的环境诊断。回答前必须依次调用 get_terminal_scrollback（当前 shell-1）、" +
        "get_checkpoint_status、list_environment_files（路径 /）、" +
        "read_environment_file（路径 /etc/hostname）和 get_solution。" +
        "我明确要求你读取参考答案。只根据工具实际返回的内容，告诉我你看到的 " +
        "BREAKFIX_ASSISTANT_SCROLLBACK 标记。最终回复必须使用 Markdown，包含二级标题“诊断结果”、" +
        "一个无序列表、一个两列表格和一个 fenced code block。",
    );
    await page.getByRole("button", { name: "Send message" }).click();
    await expect(assistantComposer).toBeDisabled();
    await page.waitForTimeout(150);

    await page.getByRole("button", { name: "Problem", exact: true }).click();
    await page.waitForTimeout(500);
    await page.getByRole("button", { name: "Assistant", exact: true }).click();
    await expect(page.getByRole("textbox", { name: "Assistant message" })).toBeVisible();

    await page.reload();
    await startChallengeFromCatalog(page, "批量压缩旧日志");
    await expect(page.getByText("Connected", { exact: true })).toBeVisible({
      timeout: 90_000,
    });
    await page.getByRole("button", { name: "Assistant", exact: true }).click();

    await expect(
      page.getByText("终端 shell-1 的近期输出", { exact: true }),
    ).toBeVisible({ timeout: 4 * 60_000 });
    const completedAssistantReply = page.locator(".assistant-message.assistant").last();
    for (const evidence of [
      "检查点状态",
      "查看环境目录 /",
      "读取环境文件 /etc/hostname",
      "参考解答",
    ]) {
      await expect(
        completedAssistantReply.locator(".assistant-evidence span").filter({
          hasText: evidence,
        }),
      ).toBeVisible({ timeout: 30_000 });
    }
    await expect(completedAssistantReply).toContainText("BREAKFIX_ASSISTANT_SCROLLBACK", {
      timeout: 30_000,
    });
    const renderedAssistantReply = completedAssistantReply.locator(".assistant-markdown");
    await expect(renderedAssistantReply.locator("ul")).toBeVisible();
    await expect(renderedAssistantReply.locator("table")).toBeVisible();
    await expect(renderedAssistantReply.locator("pre code")).toBeVisible();
    await renderedAssistantReply.scrollIntoViewIfNeeded();
    await captureWorkspace(page, "workspace-assistant-markdown");
    const persistedReply = (await completedAssistantReply.textContent()) ?? "";
    expect(persistedReply).not.toBe("");
    await page.getByRole("button", { name: "Problem", exact: true }).click();
    await page.getByRole("button", { name: "Assistant", exact: true }).click();
    await expect(page.locator(".assistant-message.assistant").last()).toContainText(
      persistedReply,
    );
    await runTerminalCommand(page, "cat /tmp/breakfix-assistant-readonly-proof");
    await expect(page.locator(".xterm-rows")).toContainText(readonlyMarker);
    await captureWorkspace(page, "workspace-assistant-desktop");

    await page.getByRole("button", { name: "Problem", exact: true }).click();

    await page.getByRole("button", { name: "Reset", exact: true }).click();
    await page.reload();
    await startChallengeFromCatalog(page, "批量压缩旧日志");
    await expect(page.getByText("Connected", { exact: true })).toBeVisible({
      timeout: 90_000,
    });
    await page.getByRole("button", { name: "Assistant", exact: true }).click();
    await expect(page.locator(".assistant-message")).toHaveCount(0);
    await page.getByRole("button", { name: "Problem", exact: true }).click();

    await expect(page.locator(".markdown-document")).toContainText(
      "批量压缩旧日志",
    );
    await page.getByRole("button", { name: "Solution", exact: true }).click();
    await expect(
      page.locator(".document-pane > .markdown-document"),
    ).toContainText("解答：批量压缩旧日志");
    await page.getByRole("button", { name: "Problem", exact: true }).click();
    await page.getByRole("button", { name: /创建清理脚本/ }).click();
    await expect(page.locator(".hint-panel")).toContainText("脚本路径");
    await page.getByTitle("Collapse sidebar").click();
    await page.getByRole("button", { name: "Show Solution" }).click();
    await expect(
      page.locator(".document-pane > .markdown-document"),
    ).toContainText("解答：批量压缩旧日志");
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
    await page.getByRole("button", { name: "Docs", exact: true }).click();
    await page.getByRole("button", { name: "Show Assistant" }).click();
    await expect(page.locator(".assistant-chat")).toBeVisible();
    await expect(page.locator(".assistant-layer")).toHaveCount(0);
    await expectViewportWithoutPageOverflow(page);
    await captureWorkspace(page, "workspace-assistant-narrow");
    await page.getByRole("button", { name: "Show Problem" }).click();
    await page.setViewportSize({ width: 1440, height: 900 });

    await runAnswer(page);
    await expect(
      page.getByText("All checkpoints complete", { exact: true }),
    ).toBeVisible({ timeout: 45_000 });

    await page.getByRole("button", { name: "Catalog", exact: true }).click();
    await expect(
      challengeCard(page, "批量压缩旧日志").getByText("Completed", { exact: true }),
    ).toBeVisible({ timeout: 30_000 });

    await page.evaluate(async () => {
      const response = await fetch("/api/challenges/cleanup-logs/stop", {
        method: "POST",
        headers: {
          Authorization: `Bearer ${localStorage.getItem("token") ?? ""}`,
        },
      });
      if (!response.ok) throw new Error(await response.text());
    });
    await page.reload();
    await expect(
      challengeCard(page, "批量压缩旧日志").getByText("Completed", { exact: true }),
    ).toBeVisible({ timeout: 30_000 });
  },
);

liveTest("vcluster workspace reports checkpoint progress", async ({ page }) => {
  test.setTimeout(8 * 60_000);
  await page.setViewportSize({ width: 1440, height: 900 });
  await registerAndLogin(page);

  await startChallengeFromCatalog(page, "修复错误的 Deployment 镜像");
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
});

generationLiveTest(
  "an author-reviewed challenge is published and completes in the workbench",
  async ({ page }) => {
    test.setTimeout(90 * 60_000);
    await page.setViewportSize({ width: 1440, height: 900 });
    await registerAndLogin(page);

    await page.getByRole("button", { name: "Challenge studio", exact: true }).click();
    await expect(
      page.getByRole("region", { name: "Challenge authoring workspace" }),
    ).toBeVisible();
    await expect(page.locator(".toast")).toHaveCount(0);
    await expectElementsWithinViewport(page, ".app-topbar:visible, .authoring-plan-heading > *");
    await expectViewportWithoutPageOverflow(page);
    await captureWorkspace(page, "authoring-desktop");

    await page.setViewportSize({ width: 390, height: 844 });
    await expectElementsWithinViewport(page, ".app-topbar:visible, .authoring-narrow-tabs > *");
    await expect(page.locator(".authoring-plan-pane")).toBeVisible();
    await expect(page.locator(".authoring-chat-pane")).toBeHidden();
    await expectViewportWithoutPageOverflow(page);
    await captureWorkspace(page, "authoring-narrow-plan");
    await page.getByRole("button", { name: "对话", exact: true }).click();
    await expect(page.locator(".authoring-chat-pane")).toBeVisible();
    await expect(page.locator(".authoring-plan-pane")).toBeHidden();
    await expectViewportWithoutPageOverflow(page);
    await captureWorkspace(page, "authoring-narrow-chat");
    await page.setViewportSize({ width: 1440, height: 900 });

    const composer = page.locator(".authoring-composer textarea");
    await expect(composer).toBeEnabled();
    await composer.fill(
      "请创建一道 container 题，参考 cleanup-logs：/var/log/app 下有多个超过 7 天的未压缩日志。" +
        "学习者需要编写可重复执行的清理脚本，把符合条件的日志压缩为 .gz，并保留最近 7 天的文件。" +
        "题目应有两个公开检查点，分别验证脚本存在且可执行，以及旧日志已压缩而新日志未被误处理。",
    );
    await expect(
      page.getByRole("button", { name: "发送消息", exact: true }),
    ).toBeEnabled();
    await page.getByRole("button", { name: "发送消息", exact: true }).click();
    await expect(composer).toBeEnabled({ timeout: 8 * 60_000 });
    await composer.fill(
      "脚本路径由你根据容器环境选择合理且稳定的位置。题意已经足够明确，请现在通过题意约定函数落盘完整题意和两个检查点。",
    );
    await page.getByRole("button", { name: "发送消息", exact: true }).click();
    await expect(composer).toBeEnabled({ timeout: 8 * 60_000 });
    await expect(
      page.getByRole("button", { name: "生成并验证题目", exact: true }),
    ).toBeEnabled({ timeout: 8 * 60_000 });
    await expect(page.locator(".authoring-status")).toContainText(/r[2-9]/);
    await expect(page.locator(".authoring-change-impact").first()).toBeVisible();

    await composer.fill(
      "第二个检查点只验证最终文件状态，不要规定用户必须使用 gzip 或固定脚本实现。",
    );
    await page.getByRole("button", { name: "发送消息", exact: true }).click();
    await expect(
      page.getByRole("button", { name: "生成并验证题目", exact: true }),
    ).toBeVisible({ timeout: 5 * 60_000 });

    const generationStart = page.waitForResponse(
      (response) =>
        response.request().method() === "POST" &&
        response.url().includes("/generate"),
    );
    await page
      .getByRole("button", { name: "生成并验证题目", exact: true })
      .click();
    const generationSession = (await (await generationStart).json()) as {
      id: string;
      verify_task_id?: string;
      artifact?: unknown;
    };
    expect(generationSession.artifact).toBeFalsy();
    await expect(page.getByText(/正在生成并验证题目 revision/)).toBeVisible();
    await waitForVerifiedRevision(page);
    const verifiedReview = await page.evaluate(async (sessionID) => {
      const response = await fetch(`/api/authoring/sessions/${sessionID}`, {
        headers: { Authorization: `Bearer ${localStorage.getItem("token") ?? ""}` },
      });
      if (!response.ok) throw new Error(await response.text());
      return response.json() as Promise<{
        artifact?: unknown;
        visible_revision?: number;
        verification?: { phase?: string; task_id?: string };
      }>;
    }, generationSession.id);
    expect(verifiedReview.artifact).toBeTruthy();
    expect(verifiedReview.verification?.phase).toBe("Succeeded");
    expect(verifiedReview.visible_revision).toBeGreaterThan(0);
    expect(verifiedReview.verification?.task_id).toBeTruthy();

    await page.getByRole("button", { name: "Assets", exact: true }).click();
    await expect(page.getByLabel("已验证文件")).toContainText("challenge.yaml");
    expect(await page.getByLabel("已验证文件").locator("option").allTextContents()).toEqual(
      expect.arrayContaining([
        "challenge.yaml",
        "Dockerfile",
        "generate.sh",
        "checks/checkpoints.sh",
        "problem.md",
        "solution.md",
        "answer.sh",
      ]),
    );
    await page.getByRole("button", { name: "Diff", exact: true }).click();
    await expect(page.locator(".authoring-code-view pre")).not.toBeEmpty();

    await composer.fill(
      "请把题目简介明确为只处理 .log 文件，并重新生成和真实验证后再给我审核。",
    );
    await page.getByRole("button", { name: "发送消息", exact: true }).click();
    await expect(
      page.getByText("正在生成并验证修订题目", { exact: true }),
    ).toBeVisible({ timeout: 5 * 60_000 });
    await waitForVerifiedRevision(page);
    const revisedReview = await page.evaluate(async (sessionID) => {
      const response = await fetch(`/api/authoring/sessions/${sessionID}`, {
        headers: { Authorization: `Bearer ${localStorage.getItem("token") ?? ""}` },
      });
      if (!response.ok) throw new Error(await response.text());
      return response.json() as Promise<{
        artifact?: unknown;
        visible_revision?: number;
        verification?: { phase?: string; task_id?: string };
      }>;
    }, generationSession.id);
    expect(revisedReview.artifact).toBeTruthy();
    expect(revisedReview.verification?.phase).toBe("Succeeded");
    expect(revisedReview.visible_revision).toBeGreaterThan(
      verifiedReview.visible_revision ?? 0,
    );
    expect(revisedReview.verification?.task_id).not.toBe(
      verifiedReview.verification?.task_id,
    );
    await page.getByRole("button", { name: "发布挑战", exact: true }).click();
    await page
      .getByRole("button", { name: "查看已发布题目", exact: true })
      .click();
    await expect(page.locator("article.challenge-card").first()).toBeVisible({
      timeout: 35 * 60_000,
    });

    await page.locator("article.challenge-card").first().getByRole("button", { name: "Start challenge", exact: true }).click();
    await expect(page.getByText("Connected", { exact: true })).toBeVisible({
      timeout: 5 * 60_000,
    });
    await runAnswer(page);
    await expect(
      page.getByText("All checkpoints complete", { exact: true }),
    ).toBeVisible({ timeout: 45_000 });
  },
);
