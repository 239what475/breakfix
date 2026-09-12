<script setup lang="ts">
import { computed, nextTick, ref, watch } from "vue";
import type { Scenario } from "../../api/types";
import CatalogFilters from "./CatalogFilters.vue";
import ScenarioList from "./ScenarioList.vue";
import { scenarioStatus, toggleSelection, type CatalogSort } from "./catalog";
import "./catalog.css";

const props = defineProps<{
	scenarios: Scenario[];
	loading: boolean;
	loggedIn: boolean;
	startingId: string | null;
	focusScenarioId?: string;
}>();
const emit = defineEmits<{
	start: [id: string];
	studio: [];
	focused: [];
}>();

const query = ref("");
const runtimes = ref<string[]>([]);
const tags = ref<string[]>([]);
const statuses = ref<string[]>([]);
const sort = ref<CatalogSort>("newest");
const filtersOpen = ref(false);
const catalogPage = ref<HTMLElement>();

const availableTags = computed(() => {
	const tags = new Set<string>();
	for (const scenario of props.scenarios) {
		for (const tag of scenario.scenario_tags) tags.add(tag);
	}
	return [...tags].sort((left, right) => left.localeCompare(right));
});

const filteredScenarios = computed(() => {
	const search = query.value.trim().toLowerCase();
	const matches = props.scenarios.filter((scenario) => {
		if (
			search &&
			![scenario.title, scenario.description, ...scenario.scenario_tags]
				.join(" ")
				.toLowerCase()
				.includes(search)
		) {
			return false;
		}
		if (runtimes.value.length && !runtimes.value.includes(scenario.runtime)) return false;
		if (tags.value.length && !tags.value.some((tag) => scenario.scenario_tags.includes(tag))) return false;
		if (props.loggedIn && statuses.value.length && !statuses.value.includes(scenarioStatus(scenario))) return false;
		return true;
	});
	return [...matches].sort((a, b) => {
		const difference = new Date(a.published_at).getTime() - new Date(b.published_at).getTime();
		return sort.value === "newest" ? -difference : difference;
	});
});

function resetFilters() {
	query.value = "";
	runtimes.value = [];
	tags.value = [];
	statuses.value = [];
}

async function focusScenario(id: string) {
	resetFilters();
	await nextTick();
	const card = catalogPage.value?.querySelector<HTMLElement>(`[data-scenario-id="${CSS.escape(id)}"]`);
	if (!card) return;
	card.scrollIntoView({ behavior: "smooth", block: "center" });
	emit("focused");
}

watch([() => props.focusScenarioId, () => props.scenarios], ([id]) => {
	if (id) void focusScenario(id);
});
</script>

<template>
	<section ref="catalogPage" class="catalog-page">
		<div class="catalog-content">
			<CatalogFilters
				:query="query"
				:runtimes="runtimes"
				:tags="tags"
				:statuses="statuses"
				:available-tags="availableTags"
				:result-count="filteredScenarios.length"
				:logged-in="loggedIn"
				:open="filtersOpen"
				@update:query="query = $event"
				@toggle:runtime="runtimes = toggleSelection(runtimes, $event)"
				@toggle:tag="tags = toggleSelection(tags, $event)"
				@toggle:status="statuses = toggleSelection(statuses, $event)"
				@reset="resetFilters"
				@close="filtersOpen = false"
				@create="emit('studio')"
			/>
			<ScenarioList
				:scenarios="filteredScenarios"
				:loading="loading"
				:logged-in="loggedIn"
				:starting-id="startingId"
				:sort="sort"
				@start="emit('start', $event)"
				@update:sort="sort = $event"
				@open:filters="filtersOpen = true"
			/>
		</div>
	</section>
</template>
