<script setup lang="ts">
import type { Challenge } from "../../api/types";
import ChallengeCard from "./ChallengeCard.vue";
import type { CatalogSort } from "./catalog";

defineProps<{
	challenges: Challenge[];
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
	<main class="catalog-results" aria-label="Challenges">
		<header class="catalog-results-header">
			<div>
				<p class="eyebrow">Available challenges</p>
				<h1>{{ challenges.length }} challenge{{ challenges.length === 1 ? '' : 's' }}</h1>
			</div>
			<div class="catalog-results-controls">
				<button class="compact-button filters-toggle" type="button" @click="emit('open:filters')">Filters</button>
				<label class="sort-control">
					<span class="sr-only">Sort challenges</span>
					<select :value="sort" aria-label="Sort challenges" @change="updateSort">
						<option value="newest">Newest first</option>
						<option value="oldest">Oldest first</option>
					</select>
				</label>
			</div>
		</header>

		<div class="challenge-card-list">
			<p v-if="loading" class="catalog-empty-copy">Loading challenges...</p>
			<template v-else>
				<ChallengeCard
					v-for="challenge in challenges"
					:key="challenge.id"
					:challenge="challenge"
					:logged-in="loggedIn"
					:starting="startingId === challenge.id"
					@start="emit('start', $event)"
				/>
				<p v-if="!challenges.length" class="catalog-empty-copy">No challenges match these filters.</p>
			</template>
		</div>
	</main>
</template>
