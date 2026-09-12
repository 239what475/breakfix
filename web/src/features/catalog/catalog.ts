import type { Scenario } from "../../api/types";

export type ScenarioStatus = "todo" | "in-progress" | "completed";
export type CatalogSort = "newest" | "oldest";

export function scenarioStatus(scenario: Scenario): ScenarioStatus {
	if (scenario.active) return "in-progress";
	if (scenario.solved) return "completed";
	return "todo";
}

export function formatRuntime(runtime: Scenario["runtime"]) {
	return runtime === "k8s" ? "Kubernetes" : "Linux nodes";
}

export function formatPublishedAt(value: string) {
	const date = new Date(value);
	if (Number.isNaN(date.getTime())) return "Published recently";
	return new Intl.DateTimeFormat("en", {
		day: "numeric",
		month: "short",
		year: "numeric",
	}).format(date);
}

export function toggleSelection(values: string[], value: string) {
	return values.includes(value)
		? values.filter((item) => item !== value)
		: [...values, value];
}
