import { expect, test } from "@playwright/test";
import {
  apiBase,
  podLifetimeAnchor,
  podLifecyclePage,
  registerAndLogin,
} from "../support/admin";

test("registration grants the ordinary role and locks non-admins out of the admin surface", async ({ request }) => {
  const member = await registerAndLogin(request, `member-${Date.now()}`);
  expect(member.role).toBe("user");

  // The plain user is locked out of the admin surface by the backend.
  const denied = await request.get(`${apiBase}/api/admin/users`, { headers: { Authorization: `Bearer ${member.token}` } });
  expect(denied.status()).toBe(403);
  const deniedIgnition = await request.post(`${apiBase}/api/documentation/practice`, {
    headers: { Authorization: `Bearer ${member.token}` },
    data: { page_path: podLifecyclePage, anchor: podLifetimeAnchor },
  });
  expect(deniedIgnition.status()).toBe(403);
});
