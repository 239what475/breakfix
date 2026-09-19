import { flushPromises, mount } from "@vue/test-utils";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "../../api/client";
import type { AdminUserList, RegisterResponse } from "../../api/generated";
import { automockApi } from "../../test/client-mock";
import AdminUsersPage from "./AdminUsersPage.vue";

vi.mock("../../api/client", async (importOriginal) => {
	const { automockApi } = await import("../../test/client-mock");
	const actual = await importOriginal<typeof import("../../api/client")>();
	return { ...actual, api: automockApi(actual.api) };
});

const users: AdminUserList = {
	users: [
		{ id: "u-admin", subject: "admin", name: "admin", role: "admin", created_at: "2026-09-19T08:00:00Z" },
		{ id: "u-member", subject: "member", name: "member", role: "user", created_at: "2026-09-19T08:01:00Z" },
	],
};

const rotated: RegisterResponse = { totp_secret: "NEWSECRET234567", totp_url: "otpauth://totp/member?secret=NEWSECRET234567" };

function mountPage() {
	return mount(AdminUsersPage, { props: { active: true, loggedIn: true, refreshRequest: 0 } });
}

describe("AdminUsersPage", () => {
	beforeEach(() => {
		vi.clearAllMocks();
		vi.mocked(api.listAdminUsers).mockResolvedValue(users);
	});

	it("renders the account rows with their role badges", async () => {
		const page = mountPage();
		await flushPromises();

		const rows = page.findAll(".admin-user-row");
		expect(rows).toHaveLength(2);
		expect(rows[0].get(".admin-role-badge").text()).toBe("admin");
		expect(rows[1].get(".admin-role-badge").text()).toBe("user");
	});

	it("resets a member's TOTP through the password-confirmed dialog", async () => {
		vi.mocked(api.resetAdminUserTOTP).mockResolvedValue(rotated);
		const page = mountPage();
		await flushPromises();

		await page.findAll(".admin-user-row")[1].get(".admin-user-action").trigger("click");
		const dialog = page.get(".dialog");
		expect(dialog.text()).toContain("重置 member 的 TOTP");

		const confirm = dialog.get(".admin-confirm-actions .compact-button");
		expect(confirm.attributes("disabled")).toBeDefined();

		await dialog.get(".admin-confirm-reason input").setValue("admin-e2e-password");
		expect(confirm.attributes("disabled")).toBeUndefined();
		await confirm.trigger("click");
		await flushPromises();

		// The operator's own password travels with the rotation request and
		// the rotated secret is shown exactly once in the follow-up dialog.
		expect(api.resetAdminUserTOTP).toHaveBeenCalledWith("u-member", "admin-e2e-password");
		const secretDialog = page.get(".dialog");
		expect(secretDialog.text()).toContain("新 TOTP 已生效");
		expect(secretDialog.get("code").text()).toBe("NEWSECRET234567");
		expect(secretDialog.text()).toContain("otpauth://totp/member?secret=NEWSECRET234567");

		await secretDialog.get(".admin-confirm-actions .compact-button").trigger("click");
		expect(page.find(".dialog").exists()).toBe(false);
	});

	it("keeps the dialog open with the error when the password is rejected", async () => {
		vi.mocked(api.resetAdminUserTOTP).mockRejectedValue(new Error("invalid operator password"));
		const page = mountPage();
		await flushPromises();

		await page.findAll(".admin-user-row")[1].get(".admin-user-action").trigger("click");
		await page.get(".admin-confirm-reason input").setValue("wrong");
		await page.get(".admin-confirm-actions .compact-button").trigger("click");
		await flushPromises();

		expect(page.get(".admin-error").text()).toContain("invalid operator password");
		expect(page.get(".dialog").text()).toContain("重置 member 的 TOTP");
	});
});
