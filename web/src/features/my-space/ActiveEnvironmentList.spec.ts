import { mount } from "@vue/test-utils";
import { describe, expect, it } from "vitest";
import ActiveEnvironmentList from "./ActiveEnvironmentList.vue";

// The two ActiveEnvironments kinds share a row: operations entries carry a
// scenario and checkpoint progress, the playground entry carries only a state
// badge and an expiry — its lifecycle verbs live on the floating ball.
describe("ActiveEnvironmentList", () => {
	it("renders a playground row without scenario identity or verbs", () => {
		const list = mount(ActiveEnvironmentList, {
			props: {
				environments: [
					{
						environment_id: "playground-u-demo",
						kind: "playground",
						runtime: "k8s",
						phase: "Provisioning",
						expires_at: new Date(Date.now() + 25 * 60 * 1000).toISOString(),
					},
				],
			},
		});

		const row = list.get(".active-environment-row");
		expect(row.text()).toContain("Playground");
		expect(row.text()).toContain("Preparing");
		expect(row.text()).toContain("25 min left");
		expect(row.find("button").exists()).toBe(false);
		expect(row.text()).not.toContain("checkpoints");
	});

	it("renders an operations row with its scenario and verbs", () => {
		const list = mount(ActiveEnvironmentList, {
			props: {
				environments: [
					{
						environment_id: "challenge-u-demo",
						kind: "operations",
						runtime: "node",
						phase: "Ready",
						scenario: { id: "demo", title: "Demo challenge", runtime: "node" },
						checkpoint_progress: { passed: 1, total: 3 },
					},
				],
			},
		});

		const row = list.get(".active-environment-row");
		expect(row.text()).toContain("Demo challenge");
		expect(row.text()).toContain("1/3 checkpoints");
		expect(row.findAll("button")).toHaveLength(2);
	});
});
