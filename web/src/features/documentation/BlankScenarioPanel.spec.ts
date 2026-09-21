import { mount } from "@vue/test-utils";
import { describe, expect, it, vi } from "vitest";
import BlankScenarioPanel from "./BlankScenarioPanel.vue";

// TerminalPane pulls xterm and the ticket plumbing; the panel spec verifies
// the binding contract, not the terminal itself.
vi.mock("../workspace/TerminalPane.vue", () => ({ default: { name: "TerminalPane", props: ["terminalId", "runtime", "nodes", "visible", "channel", "closeWindow"], template: '<div class="terminal-stub"></div>' } }));

function panelAt(state: "creating" | "ready" | "failed") {
  return mount(BlankScenarioPanel, {
    props: { state, starting: false, stopping: false, resetting: false, runtime: "k8s" },
    global: { stubs: { TerminalPane: true } },
  });
}

describe("BlankScenarioPanel", () => {
  it("shows the preparing state without a terminal while creating", () => {
    const wrapper = panelAt("creating");
    expect(wrapper.get(".scenario-panel-state").text()).toContain("Preparing the environment");
    expect(wrapper.find(".terminal-stub").exists()).toBe(false);
  });

  it("offers a retry from the failed state", async () => {
    const wrapper = panelAt("failed");
    expect(wrapper.get(".scenario-panel-state").text()).toContain("failed to start");
    await wrapper.get(".scenario-panel-state button").trigger("click");
    expect(wrapper.emitted("create")).toHaveLength(1);
  });

  it("attaches the terminal on the fixed blank scenario channel when ready", () => {
    const wrapper = panelAt("ready");
    const terminal = wrapper.findComponent({ name: "TerminalPane" });
    expect(terminal.exists()).toBe(true);
    expect(terminal.props("terminalId")).toBe("documentation-scenario");
    expect(terminal.props("runtime")).toBe("k8s");
    expect(terminal.props("visible")).toBe(true);
    const channel = terminal.props("channel");
    expect(channel).toBeTruthy();
  });

  it("emits reset and close from the ready session actions", async () => {
    const wrapper = panelAt("ready");
    await wrapper.get(".scenario-panel-actions button", { from: false }).trigger("click");
    await wrapper.get(".scenario-panel-actions .danger-button").trigger("click");
    expect(wrapper.emitted("reset")).toHaveLength(1);
    expect(wrapper.emitted("close")).toHaveLength(1);
  });
});
