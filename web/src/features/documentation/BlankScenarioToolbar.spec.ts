import { mount } from "@vue/test-utils";
import { describe, expect, it } from "vitest";
import BlankScenarioToolbar from "./BlankScenarioToolbar.vue";

function toolbarAt(state: "none" | "creating" | "ready" | "failed", busy: "starting" | "stopping" | "resetting" | null = null) {
  return mount(BlankScenarioToolbar, {
    props: {
      state,
      starting: busy === "starting",
      stopping: busy === "stopping",
      resetting: busy === "resetting",
    },
  });
}

function availability(wrapper: ReturnType<typeof toolbarAt>) {
  return {
    create: wrapper.get('button[aria-label="Create blank scenario"]').attributes("disabled") === undefined,
    reset: wrapper.get('button[aria-label="Reset blank scenario"]').attributes("disabled") === undefined,
    close: wrapper.get('button[aria-label="Close blank scenario"]').attributes("disabled") === undefined,
  };
}

describe("BlankScenarioToolbar", () => {
  // The availability matrix follows the session state machine: create is the
  // entry and the failed retry, reset belongs to a ready session, and close
  // releases every session that still exists.
  it("enables create only from none and failed", () => {
    expect(availability(toolbarAt("none"))).toEqual({ create: true, reset: false, close: false });
    expect(availability(toolbarAt("failed"))).toEqual({ create: true, reset: false, close: true });
  });

  it("keeps create disabled while creating or ready", () => {
    expect(availability(toolbarAt("creating"))).toEqual({ create: false, reset: false, close: true });
    expect(availability(toolbarAt("ready"))).toEqual({ create: false, reset: true, close: true });
  });

  it("reflects busy verbs in the matrix", () => {
    expect(availability(toolbarAt("none", "starting")).create).toBe(false);
    expect(availability(toolbarAt("ready", "resetting")).reset).toBe(false);
    expect(availability(toolbarAt("ready", "stopping")).close).toBe(false);
  });

  it("labels the badge per state", () => {
    const cases = { none: "No scenario", creating: "Preparing", ready: "Ready", failed: "Failed" } as const;
    for (const [state, label] of Object.entries(cases)) {
      const wrapper = toolbarAt(state as keyof typeof cases);
      expect(wrapper.get(".scenario-toolbar-badge").text()).toBe(label);
      expect(wrapper.get(".scenario-toolbar-badge").attributes("data-state")).toBe(state);
    }
  });

  it("emits the session verbs from their states", async () => {
    const create = toolbarAt("none");
    await create.get('button[aria-label="Create blank scenario"]').trigger("click");
    expect(create.emitted("create")).toHaveLength(1);

    const ready = toolbarAt("ready");
    await ready.get('button[aria-label="Reset blank scenario"]').trigger("click");
    await ready.get('button[aria-label="Close blank scenario"]').trigger("click");
    expect(ready.emitted("reset")).toHaveLength(1);
    expect(ready.emitted("close")).toHaveLength(1);
  });
});
