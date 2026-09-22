import { flushPromises, mount } from "@vue/test-utils";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "../../api/client";
import type { AdminEnvironmentList, AdminRunnableActionPage, AdminRunnableReapList, AdminSystemStatus } from "../../api/generated";
import { automockApi } from "../../test/client-mock";
import { isStuckEnvironment, stuckThresholdMs } from "./admin";
import AdminEnvironmentsPage from "./AdminEnvironmentsPage.vue";

vi.mock("../../api/client", async (importOriginal) => {
	const { automockApi } = await import("../../test/client-mock");
	const actual = await importOriginal<typeof import("../../api/client")>();
	return { ...actual, api: automockApi(actual.api) };
});

function environment(overrides: Partial<AdminEnvironmentList["environments"][number]> & { name: string }) {
	return {
		namespace: "breakfix-system",
		phase: "Ready",
		purpose: "learning" as const,
		user: "u-member",
		content_kind: "playground",
		created_at: new Date(Date.now() - 5 * 60 * 1000).toISOString(),
		...overrides,
	};
}

const environments: AdminEnvironmentList = {
	environments: [
		environment({ name: "playground-u-one", user: "u-one", phase: "Ready", created_at: new Date().toISOString() }),
		environment({ name: "playground-u-two", user: "u-two", phase: "Provisioning", created_at: new Date().toISOString() }),
		environment({ name: "playground-u-old", user: "u-three", phase: "Draining", created_at: new Date(Date.now() - 25 * 60 * 60 * 1000).toISOString() }),
		environment({
			name: "challenge-u-one",
			user: "u-one",
			phase: "Failed",
			content_kind: "operations",
			created_at: new Date().toISOString(),
			failure: { class: "infrastructure", component: "vk8s", reason: "terminal pod lost", message: "terminal pod evicted", at: new Date(Date.now() - 25 * 60 * 1000).toISOString() },
		}),
		environment({ name: "challenge-u-gone", user: "u-two", phase: "Released", content_kind: "operations", created_at: new Date().toISOString() }),
	],
};

const reaps: AdminRunnableReapList = {
	reaps: [
		{ reap_key: "breakfix-system/environment-1/uid-1", state: "queued", attempt: 2, last_error: "namespace ownership metadata mismatch", next_attempt_at: new Date(Date.now() + 60_000).toISOString(), updated_at: new Date(Date.now() - 1000).toISOString() },
		{ reap_key: "breakfix-system/environment-2/uid-2", state: "succeeded", attempt: 1, last_error: "", next_attempt_at: new Date(Date.now() - 60_000).toISOString(), updated_at: new Date(Date.now() - 120_000).toISOString() },
		{ reap_key: "breakfix-system/environment-3/uid-3", state: "queued", attempt: 12, last_error: "provider unavailable", next_attempt_at: new Date(Date.now() + 4 * 60_000).toISOString(), updated_at: new Date(Date.now() - 3_600_000).toISOString() },
	],
};

// The map order is deliberately scrambled: the summary chips must sort by the
// queue lifecycle, not by JSON key order.
const runnableActions: AdminRunnableActionPage = {
	summary: { by_state: { failed: 1, completed: 1, queued: 2, running: 1 }, by_attempt: { "0": 1, "2": 1, "6": 1, "8": 1 } },
	items: [
		{
			action_key: "operations/s-one/r1/sha256:aaaaaaaa/materialize-artifact/1",
			content_kind: "operations",
			content_id: "s-one",
			content_revision: "r1",
			phase: "materialize-artifact",
			state: "queued",
			attempt: 2,
			lease_expires_at: null,
			next_run_at: new Date(Date.now() + 30_000).toISOString(),
			failure_class: "infrastructure",
			failure_code: "provider-unavailable",
			failure_summary: "node pool saturated",
			flag: "",
		},
		{
			action_key: "operations/s-two/r1/sha256:bbbbbbbb/verify/1",
			content_kind: "operations",
			content_id: "s-two",
			content_revision: "r1",
			phase: "verify",
			state: "running",
			attempt: 6,
			lease_expires_at: new Date(Date.now() + 60_000).toISOString(),
			next_run_at: new Date(Date.now() + 10 * 60_000).toISOString(),
			failure_class: "",
			failure_code: "",
			failure_summary: "",
			flag: "",
		},
		{
			action_key: "operations/s-three/r1/sha256:cccccccc/materialize-artifact/1",
			content_kind: "operations",
			content_id: "s-three",
			content_revision: "r1",
			phase: "materialize-artifact",
			state: "failed",
			attempt: 8,
			lease_expires_at: null,
			next_run_at: new Date(Date.now() - 60_000).toISOString(),
			failure_class: "infrastructure",
			failure_code: "attempts-exhausted",
			failure_summary: "provider kept failing",
			flag: "attempt-high",
		},
		{
			action_key: "operations/s-four/r1/sha256:dddddddd/materialize-artifact/1",
			content_kind: "operations",
			content_id: "s-four",
			content_revision: "r1",
			phase: "materialize-artifact",
			state: "completed",
			attempt: 1,
			lease_expires_at: null,
			next_run_at: new Date(Date.now() - 3_600_000).toISOString(),
			failure_class: "",
			failure_code: "",
			failure_summary: "",
			flag: "",
		},
		{
			action_key: "operations/s-five/r1/sha256:eeeeeeee/materialize-artifact/1",
			content_kind: "operations",
			content_id: "s-five",
			content_revision: "r1",
			phase: "materialize-artifact",
			state: "queued",
			attempt: 0,
			lease_expires_at: null,
			next_run_at: new Date(Date.now() - 1000).toISOString(),
			failure_class: "",
			failure_code: "",
			failure_summary: "",
			flag: "",
		},
	],
};

const system: AdminSystemStatus = {
	version: "dev",
	commit: "deadbeef",
	build_time: new Date().toISOString(),
	catalog_integrity: { state: "ok" },
	services: [],
	playground_max_active: 10,
};

function mountPage() {
	return mount(AdminEnvironmentsPage, { props: { active: true, loggedIn: true, refreshRequest: 0 } });
}

describe("AdminEnvironmentsPage", () => {
	beforeEach(() => {
		vi.clearAllMocks();
		vi.mocked(api.listAdminEnvironments).mockResolvedValue(environments);
		vi.mocked(api.listAdminRunnableReaps).mockResolvedValue(reaps);
		vi.mocked(api.listAdminRunnableActions).mockResolvedValue(runnableActions);
		vi.mocked(api.getAdminSystem).mockResolvedValue(system);
	});

	it("aggregates the overview card from the environment list", async () => {
		const page = mountPage();
		await flushPromises();

		// Live playground sessions: Ready + Provisioning count; Draining still
		// holds its slot; operations and Released never do.
		const cards = page.findAll(".admin-overview-card");
		expect(cards[0].text()).toContain("3");
		expect(cards[0].text()).toContain("10");
		// Three distinct owners across every environment.
		expect(cards[1].text()).toContain("3");
		// Four of the five fixtures were created today.
		expect(cards[2].text()).toContain("4");
	});

	it("renders the reap queue rows with retry diagnostics", async () => {
		const page = mountPage();
		await flushPromises();

		const rows = page.findAll(".admin-reap-table tbody tr");
		expect(rows).toHaveLength(3);
		expect(rows[0].text()).toContain("namespace ownership metadata mismatch");
		expect(rows[0].text()).toContain("2");

		// A long-stuck reap row is never terminal: it stays queued, keeps
		// promising its next capped-backoff attempt, and carries the derived
		// stuck highlight that asks for a human look.
		const stuckBadge = rows[2].get(".admin-phase-badge");
		expect(stuckBadge.attributes("data-state")).toBe("queued");
		expect(stuckBadge.text()).toBe("queued");
		expect(rows[2].text()).toContain("provider unavailable");
		expect(rows[2].classes()).toContain("admin-row-stuck");
		expect(rows[2].findAll("td")[4].text()).not.toBe("—");
	});

	it("renders the action queue summary chips in lifecycle order", async () => {
		const page = mountPage();
		await flushPromises();

		const chips = page.findAll(".admin-action-summary .admin-phase-chip");
		expect(chips.map((chip) => chip.text())).toEqual(["queued 2", "running 1", "completed 1", "failed 1"]);
	});

	it("renders the action queue rows with failure diagnostics", async () => {
		const page = mountPage();
		await flushPromises();

		const rows = page.findAll(".admin-action-table tbody tr");
		expect(rows).toHaveLength(5);

		// A retried infra row keeps its last failure visible while queued.
		expect(rows[0].text()).toContain("provider-unavailable");
		const failureNote = rows[0].get(".admin-failure-note");
		expect(failureNote.attributes("title")).toContain("infrastructure");
		expect(failureNote.attributes("title")).toContain("node pool saturated");
		expect(rows[0].findAll("td")[5].text()).toMatch(/后$/);

		// The explicit terminal failure carries the code, the server's
		// attempt-high flag drives the highlight, and the next-run column
		// goes quiet for terminal rows.
		const failedBadge = rows[2].get(".admin-phase-badge");
		expect(failedBadge.attributes("data-state")).toBe("failed");
		expect(rows[2].text()).toContain("attempts-exhausted");
		expect(rows[2].classes()).toContain("admin-row-stuck");
		expect(rows[2].findAll("td")[5].text()).toBe("—");
		const completedBadge = rows[3].get(".admin-phase-badge");
		expect(completedBadge.attributes("data-state")).toBe("completed");
		expect(rows[3].findAll("td")[5].text()).toBe("—");

		// Only the flagged row is highlighted: the running row sits at attempt
		// 6 (past the would-be front-end threshold) without the server flag,
		// so a derived highlight would fail this count.
		expect(page.findAll(".admin-action-table .admin-row-stuck")).toHaveLength(1);
	});

	it("shows the empty state when the action queue is idle", async () => {
		vi.mocked(api.listAdminRunnableActions).mockResolvedValue({ summary: { by_state: {}, by_attempt: {} }, items: [] });
		const page = mountPage();
		await flushPromises();

		expect(page.find(".admin-action-table").exists()).toBe(false);
		expect(page.find(".admin-action-summary").exists()).toBe(false);
		// The reap section keeps its own queue; only the action queue is idle.
		const empties = page.findAll(".admin-empty").filter((node) => node.text() === "队列为空。");
		expect(empties).toHaveLength(1);
	});

	it("filters the table by phase", async () => {
		const page = mountPage();
		await flushPromises();

		expect(page.findAll(".admin-environment-table tbody tr")).toHaveLength(5);
		await page.get(".admin-filter select").setValue("Draining");
		const rows = page.findAll(".admin-environment-table tbody tr");
		expect(rows).toHaveLength(1);
		expect(rows[0].text()).toContain("playground-u-old");
	});

	it("highlights stuck draining and failed rows past the threshold", async () => {
		const page = mountPage();
		await flushPromises();

		const stuck = page.findAll(".admin-environment-table .admin-row-stuck");
		expect(stuck).toHaveLength(2);
		expect(stuck[0].text()).toContain("playground-u-old");
		expect(stuck[1].text()).toContain("challenge-u-one");
		// The failure detail stays visible on the failed row.
		expect(stuck[1].text()).toContain("terminal pod evicted");
	});

	it("releases an environment through the confirm dialog", async () => {
		vi.mocked(api.releaseAdminEnvironment).mockResolvedValue({ id: "uid-1", phase: "Draining" });
		const page = mountPage();
		await flushPromises();

		const releaseButtons = page.findAll(".admin-release-button");
		// Every row renders the verb; Draining and Released rows carry it
		// disabled because their fate is already sealed.
		expect(releaseButtons).toHaveLength(5);
		expect(releaseButtons.filter((button) => button.attributes("disabled") !== undefined)).toHaveLength(2);
		await releaseButtons[0].trigger("click");

		const dialog = page.get(".dialog");
		expect(dialog.text()).toContain("playground-u-one");
		await dialog.get(".admin-confirm-actions .compact-button").trigger("click");
		await flushPromises();

		expect(api.releaseAdminEnvironment).toHaveBeenCalledWith("playground-u-one");
		// The release refreshes the observation in place.
		expect(api.listAdminEnvironments).toHaveBeenCalledTimes(2);
		expect(page.find(".dialog").exists()).toBe(false);
	});

	it("keeps the error visible when the release is rejected", async () => {
		vi.mocked(api.releaseAdminEnvironment).mockRejectedValue(new Error("environment is already draining"));
		const page = mountPage();
		await flushPromises();

		await page.findAll(".admin-release-button")[0].trigger("click");
		await page.get(".admin-confirm-actions .compact-button").trigger("click");
		await flushPromises();

		expect(page.get(".admin-error").text()).toContain("environment is already draining");
	});
});

describe("isStuckEnvironment", () => {
	it("flags phases past the threshold and leaves fresh ones alone", () => {
		const now = Date.now();
		const old = new Date(now - stuckThresholdMs - 60_000).toISOString();
		const fresh = new Date(now - 60_000).toISOString();
		expect(isStuckEnvironment({ ...environment({ name: "x", phase: "Draining" }), created_at: old }, now)).toBe(true);
		expect(isStuckEnvironment({ ...environment({ name: "x", phase: "Draining" }), created_at: fresh }, now)).toBe(false);
		expect(isStuckEnvironment({ ...environment({ name: "x", phase: "Ready" }), created_at: old }, now)).toBe(false);
		// A failed environment is measured from its failure timestamp, not
		// creation, so a long-lived session that just failed stays unflagged.
		expect(
			isStuckEnvironment(
				{ ...environment({ name: "x", phase: "Failed", created_at: old }), failure: { class: "infrastructure", component: "c", reason: "r", at: fresh } },
				now,
			),
		).toBe(false);
	});
});
