import { mount } from "@vue/test-utils";
import { describe, expect, it } from "vitest";
import PracticePanel from "./PracticePanel.vue";
import { practiceProjection } from "./fixtures";

function mountPanel() {
	return mount(PracticePanel, {
		props: {
			practiceId: practiceProjection.id,
			detail: practiceProjection,
			loading: false,
			failed: false,
			sessionActive: false,
			starting: false,
			stopping: false,
			resetting: false,
			ready: false,
			phase: "",
			runtime: "k8s",
			nodes: [],
		},
	});
}

describe("PracticePanel", () => {
	it("renders the frozen projection published with the practice", () => {
		const panel = mountPanel();

		expect(panel.get(".practice-panel-title").text()).toBe("Observe Pod lifetime");
		expect(panel.text()).toContain("Observe a Pod reach Running");
		expect(panel.text()).toContain("One Pod in the fixed Kubernetes environment");
		expect(panel.text()).toContain("Create the Pod and observe its phase");
		expect(panel.text()).toContain("The Pod reaches the Running phase");
		// Without a session the panel offers the explicit start entry; no
		// terminal renders before the environment is Ready.
		expect(panel.text()).toContain("Start practice");
		expect(panel.find(".practice-panel-terminal").exists()).toBe(false);
	});

	it("collapses the steps and observations sections", async () => {
		const panel = mountPanel();

		const steps = panel.findAll(".practice-panel-toggle").find((button) => button.text().includes("Steps"))!;
		expect(steps.attributes("aria-expanded")).toBe("true");
		expect((panel.get(".practice-panel-steps").element as HTMLElement).style.display).toBe("");

		await steps.trigger("click");
		expect(steps.attributes("aria-expanded")).toBe("false");
		expect((panel.get(".practice-panel-steps").element as HTMLElement).style.display).toBe("none");

		const observations = panel.findAll(".practice-panel-toggle").find((button) => button.text().includes("Observations"))!;
		await observations.trigger("click");
		expect(observations.attributes("aria-expanded")).toBe("false");
		expect((panel.get(".practice-panel-observations").element as HTMLElement).style.display).toBe("none");
	});
});
