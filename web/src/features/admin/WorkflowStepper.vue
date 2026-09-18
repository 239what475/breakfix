<script setup lang="ts">
import { computed } from "vue";

// Stage ladder mirrors the domain WorkflowState order. Terminal outcomes that
// never walk the ladder (Failed/Rejected/NoPractice) are filtered out by the
// parent; Published is rendered as a fully completed ladder.
const STAGES = [
	{ state: "Planning", label: "规划" },
	{ state: "PlanReviewing", label: "计划评审" },
	{ state: "Generating", label: "生成" },
	{ state: "ArtifactReviewing", label: "工件评审" },
	{ state: "MaterializingArtifact", label: "物化" },
	{ state: "Verifying", label: "验证" },
	{ state: "VerificationReviewing", label: "验证评审" },
	{ state: "Publishing", label: "发布" },
	{ state: "Published", label: "已发布" },
] as const;

const props = defineProps<{ state: string; dwellSeconds: number; stuck: boolean }>();

const nodes = computed(() => {
	const current = STAGES.findIndex((stage) => stage.state === props.state);
	return STAGES.map((stage, index) => ({ ...stage, status: index < current ? "done" : index === current ? "current" : "pending" }));
});

function dwell(seconds: number) {
	if (seconds < 90) return `${seconds}s`;
	if (seconds < 5400) return `${Math.round(seconds / 60)}m`;
	return `${Math.round(seconds / 3600)}h`;
}
</script>

<template>
	<ol class="workflow-stepper" :aria-label="`工作流阶段:${state}`">
		<li v-for="node in nodes" :key="node.state" class="stepper-node" :class="[node.status, { stuck: node.status === 'current' && stuck }]">
			<span class="stepper-dot" aria-hidden="true">{{ node.status === "done" ? "✓" : node.status === "current" ? (stuck ? "◐" : "●") : "○" }}</span>
			<span class="stepper-label">{{ node.label }}</span>
			<span v-if="node.status === 'current'" class="stepper-dwell">{{ dwell(dwellSeconds) }}</span>
		</li>
	</ol>
</template>
