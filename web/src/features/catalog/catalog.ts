import type { Challenge } from "../../api/types";

export type ChallengeStatus = "todo" | "in-progress" | "completed";
export type CatalogSort = "newest" | "oldest";

export function challengeStatus(challenge: Challenge): ChallengeStatus {
	if (challenge.active) return "in-progress";
	if (challenge.solved) return "completed";
	return "todo";
}

export function formatRuntime(runtime: Challenge["runtime"]) {
	return runtime === "k8s" ? "Kubernetes" : "Linux nodes";
}

export function formatDifficulty(difficulty: Challenge["difficulty"]) {
	return difficulty.charAt(0).toUpperCase() + difficulty.slice(1);
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
