<script setup lang="ts">
import { X } from "lucide-vue-next";
import type { TaxonomyReference } from "../../api/types";

const props = defineProps<{
	query: string;
	difficulties: string[];
	runtimes: string[];
	tags: string[];
	statuses: string[];
	availableTags: TaxonomyReference[];
	resultCount: number;
	loggedIn: boolean;
	open: boolean;
}>();

const emit = defineEmits<{
	"update:query": [value: string];
	"toggle:difficulty": [value: string];
	"toggle:runtime": [value: string];
	"toggle:tag": [value: string];
	"toggle:status": [value: string];
	reset: [];
	close: [];
}>();

function updateQuery(event: Event) {
	emit("update:query", (event.target as HTMLInputElement).value);
}
</script>

<template>
	<aside class="catalog-filters" :class="{ open }" aria-label="Challenge filters">
		<div class="filters-heading">
			<div>
				<p class="filters-kicker">Challenge catalog</p>
				<h1>Find a challenge</h1>
			</div>
			<button class="filters-close icon-button" type="button" aria-label="Close filters" title="Close filters" @click="emit('close')">
				<X :size="16" aria-hidden="true" />
			</button>
		</div>

		<label class="catalog-search-field">
			<span class="sr-only">Search challenges</span>
			<input :value="query" placeholder="Search challenges" aria-label="Search challenges" @input="updateQuery" />
		</label>

		<div class="filters-result-row">
			<span>{{ resultCount }} matching</span>
			<button class="text-button" type="button" @click="emit('reset')">Reset filters</button>
		</div>

		<section class="filter-group" aria-labelledby="difficulty-filter">
			<h2 id="difficulty-filter">Difficulty</h2>
			<label v-for="difficulty in ['easy', 'medium', 'hard']" :key="difficulty" class="filter-option">
				<input type="checkbox" :checked="difficulties.includes(difficulty)" @change="emit('toggle:difficulty', difficulty)" />
				<span>{{ difficulty.charAt(0).toUpperCase() + difficulty.slice(1) }}</span>
			</label>
		</section>

		<section class="filter-group" aria-labelledby="runtime-filter">
			<h2 id="runtime-filter">Runtime</h2>
			<label v-for="runtime in ['container', 'vcluster']" :key="runtime" class="filter-option">
				<input type="checkbox" :checked="runtimes.includes(runtime)" @change="emit('toggle:runtime', runtime)" />
				<span>{{ runtime === 'vcluster' ? 'VCluster' : 'Container' }}</span>
			</label>
		</section>

		<section v-if="loggedIn" class="filter-group" aria-labelledby="status-filter">
			<h2 id="status-filter">Status</h2>
			<label v-for="status in [['todo', 'Todo'], ['in-progress', 'In progress'], ['completed', 'Completed']]" :key="status[0]" class="filter-option">
				<input type="checkbox" :checked="statuses.includes(status[0])" @change="emit('toggle:status', status[0])" />
				<span>{{ status[1] }}</span>
			</label>
		</section>

		<section class="filter-group tags-filter-group" aria-labelledby="tags-filter">
			<h2 id="tags-filter">Tags</h2>
			<label v-for="tag in availableTags" :key="tag.id" class="filter-option">
				<input type="checkbox" :checked="tags.includes(tag.id)" @change="emit('toggle:tag', tag.id)" />
				<span>{{ tag.title }}</span>
			</label>
		</section>
	</aside>
</template>
