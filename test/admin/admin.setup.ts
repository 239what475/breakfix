import { existsSync } from "node:fs";
import { expect, test } from "@playwright/test";
import {
  adminPassword,
  apiBase,
  registerAndLogin,
  saveBootstrapAdmin,
  sessionFile,
  totp,
  readBootstrapAdmin,
} from "../support/admin";

// The admin suite owns a freshly reset target: the first registration elects
// the bootstrap admin. Re-runs against a still-prepared target reuse the
// elected credentials through the session file instead of registering again
// (a second registration would stay an ordinary user).
test("elect or reuse the bootstrap admin", async ({ request }) => {
  test.setTimeout(120_000);

  if (existsSync(sessionFile)) {
    const session = readBootstrapAdmin();
    const login = await request.post(`${apiBase}/api/auth/login`, {
      data: { username: session.username, password: adminPassword, totp_code: totp(session.totpSecret) },
    });
    if (login.status() === 200) return;
    // The session went stale (the target was reset underneath it): fall
    // through and elect a fresh bootstrap admin if the target is virgin.
  }

  const admin = await registerAndLogin(request, `admin-${Date.now()}`);
  expect(admin.role, [
    "only the first registration on a fresh target elects the bootstrap admin;",
    "reset the target (make e2e-reset && make test-e2e-admin) before re-electing",
  ].join(" ")).toBe("admin");
  saveBootstrapAdmin({ username: admin.username, totpSecret: admin.totpSecret });
});
