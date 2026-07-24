<script setup lang="ts">
import { computed, nextTick, ref, watch } from "vue";
import type { Challenge } from "../../api/types";
import CatalogFilters from "./CatalogFilters.vue";
import ChallengeList from "./ChallengeList.vue";
import { challengeStatus, toggleSelection, type CatalogSort } from "./catalog";
import "./catalog.css";

const props = defineProps<{
	challenges: Challenge[];
	loading: boolean;
	loggedIn: boolean;
	startingId: string | null;
	focusChallengeId?: string;
}>();
const emit = defineEmits<{
	start: [id: string];
	studio: [];
	focused: [];
}>();

const query = ref("");
const difficulties = ref<string[]>([]);
const runtimes = ref<string[]>([]);
const tags = ref<string[]>([]);
const statuses = ref<string[]>([]);
const sort = ref<CatalogSort>("newest");
const filtersOpen = ref(false);
const catalogPage = ref<HTMLElement>();

const availableTags = computed(() =>
	[...new Set(props.challenges.flatMap((challenge) => challenge.tags))].sort((a, b) =>
		a.localeCompare(b),
	),
);

const filteredChallenges = computed(() => {
	const search = query.value.trim().toLowerCase();
	const matches = props.challenges.filter((challenge) => {
		if (
			search &&
			![challenge.title, challenge.description, ...challenge.tags]
				.join(" ")
				.toLowerCase()
				.includes(search)
		) {
			return false;
		}
		if (difficulties.value.length && !difficulties.value.includes(challenge.difficulty)) return false;
		if (runtimes.value.length && !runtimes.value.includes(challenge.runtime)) return false;
		if (tags.value.length && !tags.value.some((tag) => challenge.tags.includes(tag))) return false;
		if (props.loggedIn && statuses.value.length && !statuses.value.includes(challengeStatus(challenge))) return false;
		return true;
	});
	return [...matches].sort((a, b) => {
		const difference = new Date(a.published_at).getTime() - new Date(b.published_at).getTime();
		return sort.value === "newest" ? -difference : difference;
	});
});

function resetFilters() {
	query.value = "";
	difficulties.value = [];
	runtimes.value = [];
	tags.value = [];
	statuses.value = [];
}

async function focusChallenge(id: string) {
	resetFilters();
	await nextTick();
	const card = catalogPage.value?.querySelector<HTMLElement>(`[data-challenge-id="${CSS.escape(id)}"]`);
	if (!card) return;
	card.scrollIntoView({ behavior: "smooth", block: "center" });
	emit("focused");
}

watch([() => props.focusChallengeId, () => props.challenges], ([id]) => {
	if (id) void focusChallenge(id);
});
</script>

<template>
	<section ref="catalogPage" class="catalog-page">
		<div class="catalog-content">
			<CatalogFilters
				:query="query"
				:difficulties="difficulties"
				:runtimes="runtimes"
				:tags="tags"
				:statuses="statuses"
				:available-tags="availableTags"
				:result-count="filteredChallenges.length"
				:logged-in="loggedIn"
				:open="filtersOpen"
				@update:query="query = $event"
				@toggle:difficulty="difficulties = toggleSelection(difficulties, $event)"
				@toggle:runtime="runtimes = toggleSelection(runtimes, $event)"
				@toggle:tag="tags = toggleSelection(tags, $event)"
				@toggle:status="statuses = toggleSelection(statuses, $event)"
				@reset="resetFilters"
				@close="filtersOpen = false"
			/>
			<ChallengeList
				:challenges="filteredChallenges"
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
