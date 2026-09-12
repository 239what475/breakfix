<script setup lang="ts">
import type { Scenario } from "../../api/types";
import ScenarioCard from "./ScenarioCard.vue";
import type { CatalogSort } from "./catalog";

defineProps<{
	scenarios: Scenario[];
	loading: boolean;
	loggedIn: boolean;
	startingId: string | null;
	sort: CatalogSort;
}>();

const emit = defineEmits<{
	start: [id: string];
	"update:sort": [value: CatalogSort];
	"open:filters": [];
}>();

function updateSort(event: Event) {
	emit("update:sort", (event.target as HTMLSelectElement).value as CatalogSort);
}
</script>

<template>
	<main class="catalog-results" aria-label="Scenarios">
		<header class="catalog-results-header">
			<div>
				<p class="eyebrow">Available scenarios</p>
				<h1>{{ scenarios.length }} scenario{{ scenarios.length === 1 ? '' : 's' }}</h1>
			</div>
			<div class="catalog-results-controls">
				<button class="compact-button filters-toggle" type="button" @click="emit('open:filters')">Filters</button>
				<label class="sort-control">
					<span class="sr-only">Sort scenarios</span>
					<select :value="sort" aria-label="Sort scenarios" @change="updateSort">
						<option value="newest">Newest first</option>
						<option value="oldest">Oldest first</option>
					</select>
				</label>
			</div>
		</header>

		<div class="scenario-card-list">
			<p v-if="loading" class="catalog-empty-copy">Loading scenarios...</p>
			<template v-else>
				<ScenarioCard
					v-for="scenario in scenarios"
					:key="scenario.id"
					:scenario="scenario"
					:logged-in="loggedIn"
					:starting="startingId === scenario.id"
					@start="emit('start', $event)"
				/>
				<p v-if="!scenarios.length" class="catalog-empty-copy">No scenarios match these filters.</p>
			</template>
		</div>
	</main>
</template>
