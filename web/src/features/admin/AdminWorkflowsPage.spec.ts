import { flushPromises, mount } from "@vue/test-utils";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { AdminDocumentationWorkflow } from "../../api/generated";
import AdminWorkflowsPage from "./AdminWorkflowsPage.vue";

const workflow = (over: Partial<AdminDocumentationWorkflow>): AdminDocumentationWorkflow => ({
	id: "document-workflow-rescue",
	state: "MaterializingArtifact",
	state_version: 4,
	revision: 1,
	page_path: "docs/concepts/workloads/pods/pod-lifecycle",
	anchor: "pod-lifetime",
	title: "Pod Lifecycle",
	updated_at: "2026-09-19T08:05:00Z",
	dwell_seconds: 300,
	stuck: { flag: false },
	...over,
});

const walking = workflow({});
const failed = workflow({ id: "document-workflow-failed", state: "Failed" });

function mountPage(over: { workflows?: AdminDocumentationWorkflow[]; runAction?: ReturnType<typeof vi.fn> } = {}) {
	const runAction = over.runAction ?? vi.fn().mockResolvedValue(true);
	const page = mount(AdminWorkflowsPage, {
		props: {
			workflows: over.workflows ?? [walking, failed],
			loading: false,
			busy: false,
			openDetail: vi.fn(),
			closeDetail: vi.fn(),
			runAction,
		},
	});
	return { page, runAction };
}

describe("AdminWorkflowsPage", () => {
	beforeEach(() => {
		vi.clearAllMocks();
	});

	it("steppers walking states and drops the stepper on terminal outcomes", () => {
		const { page } = mountPage();
		const rows = page.findAll(".admin-workflow-row");

		const stepper = rows[0].get(".workflow-stepper");
		expect(stepper.get(".stepper-node.current .stepper-label").text()).toBe("物化");
		expect(stepper.get(".stepper-node.done .stepper-label").text()).toBe("规划");

		// Failed never walked the ladder: badge + meta only, no stepper.
		expect(rows[1].text()).toContain("Failed");
		expect(rows[1].find(".workflow-stepper").exists()).toBe(false);
	});

	it("gates the destructive verbs behind the overflow menu", async () => {
		const { page } = mountPage();
		const rows = page.findAll(".admin-workflow-row");

		// A walking state offers force-fail only; the failed one offers restart.
		await rows[0].get('button[aria-label="工作流操作"]').trigger("click");
		const menu = rows[0].get('[role="menu"]');
		expect(menu.text()).toContain("Force-fail 强制失败");
		expect(menu.text()).not.toContain("Restart 重启");

		await rows[1].get('button[aria-label="工作流操作"]').trigger("click");
		const failedMenu = rows[1].get('[role="menu"]');
		expect(failedMenu.text()).toContain("Restart 重启");
		expect(failedMenu.text()).not.toContain("Force-fail 强制失败");
	});

	it("requires a reason before the confirm dialog executes force-fail", async () => {
		const runAction = vi.fn().mockResolvedValue(true);
		const { page } = mountPage({ runAction });
		const row = page.findAll(".admin-workflow-row")[0];

		await row.get('button[aria-label="工作流操作"]').trigger("click");
		await row.get('[role="menuitem"]').trigger("click");

		const dialog = page.get(".dialog");
		expect(dialog.text()).toContain("强制失败确认");
		const confirm = dialog.get(".danger-button");
		expect(confirm.attributes("disabled")).toBeDefined();

		await dialog.get(".admin-confirm-reason textarea").setValue("worker removed, materialization stalled");
		expect(confirm.attributes("disabled")).toBeUndefined();
		await confirm.trigger("click");
		await flushPromises();

		expect(runAction).toHaveBeenCalledWith("force-fail", "document-workflow-rescue", "worker removed, materialization stalled");
		expect(page.find(".dialog").exists()).toBe(false);
	});
});
