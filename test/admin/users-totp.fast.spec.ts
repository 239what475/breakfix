import { expect, test } from "@playwright/test";
import {
  adminPassword,
  apiBase,
  loginBootstrapAdmin,
  loginThroughConsole,
  readBootstrapAdmin,
  registerAndLogin,
  resolveUserId,
  totp,
} from "../support/admin";

test("TOTP reset through the console rotates the member's second factor once", async ({ page, request }) => {
  const token = await loginBootstrapAdmin(request);
  const member = await registerAndLogin(request, `member-${Date.now()}`);
  const memberUserId = await resolveUserId(request, token, member.username);

  // The API demands the operator's own password before rotating a factor.
  const badPassword = await request.post(`${apiBase}/api/admin/users/${memberUserId}/totp-reset`, {
    headers: { Authorization: `Bearer ${token}` },
    data: { password: "wrong-password" },
  });
  expect(badPassword.status()).toBe(403);

  await loginThroughConsole(page, readBootstrapAdmin());
  await page.getByRole("button", { name: "用户" }).click();
  const memberRow = page.locator(".admin-user-row", { hasText: member.username });
  await expect(memberRow.locator(".admin-role-badge")).toHaveText("user");
  await memberRow.getByRole("button", { name: "重置 TOTP" }).click();
  await page.getByLabel("操作者密码").fill(adminPassword);
  await page.getByRole("button", { name: "确认重置" }).click();
  const secretDialog = page.locator(".dialog", { hasText: "新 TOTP 已生效" });
  await expect(secretDialog).toBeVisible();
  const rotatedSecret = (await secretDialog.locator("code").first().textContent()) ?? "";
  await secretDialog.getByRole("button", { name: "我已保存" }).click();
  expect(rotatedSecret).not.toBe(member.totpSecret);

  const oldCodeLogin = await request.post(`${apiBase}/api/auth/login`, {
    data: { username: member.username, password: adminPassword, totp_code: totp(member.totpSecret) },
  });
  expect(oldCodeLogin.status()).toBe(401);
  const newCodeLogin = await request.post(`${apiBase}/api/auth/login`, {
    data: { username: member.username, password: adminPassword, totp_code: totp(rotatedSecret) },
  });
  expect(newCodeLogin.status()).toBe(200);
});
