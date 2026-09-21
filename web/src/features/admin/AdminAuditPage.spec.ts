import { flushPromises, mount } from "@vue/test-utils";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "../../api/client";
import type { AdminAuditPage as AdminAuditPageModel } from "../../api/generated";
import { automockApi } from "../../test/client-mock";
import AdminAuditPage from "./AdminAuditPage.vue";

vi.mock("../../api/client", async (importOriginal) => {
	const { automockApi } = await import("../../test/client-mock");
	const actual = await importOriginal<typeof import("../../api/client")>();
	return { ...actual, api: automockApi(actual.api) };
});

const ledger: AdminAuditPageModel = {
	items: [
		{
			id: "audit-force-fail",
			user_id: "u-admin",
			action: "environment.release",
			target_type: "runtime_environment",
			target_id: "document-workflow-stuck",
			detail: { from_state: "MaterializingArtifact", to_state: "Failed", reason: "materialization stalled" },
			created_at: "2026-09-19T08:05:00Z",
		},
		{
			id: "audit-start",
			user_id: "u-admin",
			action: "user.totp.reset",
			target_type: "runtime_environment",
			target_id: "document-workflow-stuck",
			detail: {},
			created_at: "2026-09-19T08:00:00Z",
		},
	],
};

function mountPage() {
	return mount(AdminAuditPage, { props: { active: true, loggedIn: true, refreshRequest: 0 } });
}

describe("AdminAuditPage", () => {
	beforeEach(() => {
		vi.clearAllMocks();
		vi.mocked(api.listAdminAudit).mockResolvedValue(ledger);
	});

	it("renders the ledger rows with their action and target", async () => {
		const page = mountPage();
		await flushPromises();

		const rows = page.findAll(".admin-audit-item");
		expect(rows).toHaveLength(2);
		expect(rows[0].get(".admin-audit-action").text()).toBe("environment.release");
		expect(rows[0].get(".admin-audit-target").text()).toBe("u-admin → document-workflow-stuck");
	});

	it("expands a row's payload and disables payload-less rows", async () => {
		const page = mountPage();
		await flushPromises();

		expect(page.find(".admin-audit-detail").exists()).toBe(false);

		const toggles = page.findAll(".admin-audit-toggle");
		await toggles[0].trigger("click");
		const detail = page.get(".admin-audit-detail");
		expect(detail.text()).toContain("from_state");
		expect(detail.text()).toContain("MaterializingArtifact");
		expect(detail.text()).toContain("to_state");

		expect(toggles[1].attributes("disabled")).toBeDefined();
	});
});
