<script setup lang="ts">
import { Plus, X } from "lucide-vue-next";
const props = defineProps<{
	query: string;
	runtimes: string[];
	tags: string[];
	statuses: string[];
	availableTags: string[];
	resultCount: number;
	loggedIn: boolean;
	open: boolean;
}>();

const emit = defineEmits<{
	"update:query": [value: string];
	"toggle:runtime": [value: string];
	"toggle:tag": [value: string];
	"toggle:status": [value: string];
	reset: [];
	close: [];
	create: [];
}>();

function updateQuery(event: Event) {
	emit("update:query", (event.target as HTMLInputElement).value);
}
</script>

<template>
	<aside class="catalog-filters" :class="{ open }" aria-label="Scenario filters">
		<div class="filters-heading">
			<div>
				<p class="filters-kicker">Scenario catalog</p>
				<h1>Find a scenario</h1>
			</div>
			<div class="filters-heading-actions">
				<button v-if="loggedIn" class="compact-button" type="button" @click="emit('create')"><Plus :size="14" aria-hidden="true" />Create scenario</button>
				<button class="filters-close icon-button" type="button" aria-label="Close filters" title="Close filters" @click="emit('close')">
					<X :size="16" aria-hidden="true" />
				</button>
			</div>
		</div>

		<label class="catalog-search-field">
			<span class="sr-only">Search scenarios</span>
			<input :value="query" placeholder="Search scenarios" aria-label="Search scenarios" @input="updateQuery" />
		</label>

		<div class="filters-result-row">
			<span>{{ resultCount }} matching</span>
			<button class="text-button" type="button" @click="emit('reset')">Reset filters</button>
		</div>

		<section class="filter-group" aria-labelledby="runtime-filter">
			<h2 id="runtime-filter">Runtime</h2>
			<label v-for="runtime in ['node', 'k8s']" :key="runtime" class="filter-option">
				<input type="checkbox" :checked="runtimes.includes(runtime)" @change="emit('toggle:runtime', runtime)" />
				<span>{{ runtime === 'k8s' ? 'Kubernetes' : 'Linux nodes' }}</span>
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
			<label v-for="tag in availableTags" :key="tag" class="filter-option">
				<input type="checkbox" :checked="tags.includes(tag)" @change="emit('toggle:tag', tag)" />
				<span>{{ tag }}</span>
			</label>
		</section>
	</aside>
</template>
