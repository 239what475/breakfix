<script setup lang="ts">
import type { Scenario } from "../../api/types";
import { scenarioStatus, formatPublishedAt, formatRuntime } from "./catalog";

const props = defineProps<{
	scenario: Scenario;
	loggedIn: boolean;
	starting: boolean;
}>();
const emit = defineEmits<{ start: [id: string] }>();
</script>

<template>
<article
	class="scenario-card"
	:data-scenario-id="scenario.id"
	:data-testid="`catalog-scenario-${scenario.id}`"
	:aria-label="`Scenario: ${scenario.title}`"
>
		<div class="scenario-card-main">
			<div class="scenario-card-topline">
				<div class="scenario-pills">
					<span class="runtime-pill">{{ formatRuntime(scenario.runtime) }}</span>
				</div>
				<time :datetime="scenario.published_at">{{ formatPublishedAt(scenario.published_at) }}</time>
			</div>
			<h2>{{ scenario.title }}</h2>
			<p class="scenario-card-description">{{ scenario.description }}</p>
			<div class="scenario-card-bottom">
				<div class="scenario-tags" aria-label="Scenario tags">
					<span v-for="tag in scenario.scenario_tags" :key="tag">{{ tag }}</span>
				</div>
				<div v-if="loggedIn" class="scenario-state" :class="scenarioStatus(scenario)">
					<span v-if="scenarioStatus(scenario) === 'in-progress'">
						In progress<span v-if="scenario.progress"> · {{ scenario.progress.passed }}/{{ scenario.progress.total }} checkpoints</span>
					</span>
					<span v-else-if="scenarioStatus(scenario) === 'completed'">Completed</span>
					<span v-else>Todo</span>
					<span v-if="scenario.active && scenario.solved" class="completion-history">Completed before</span>
				</div>
			</div>
		</div>
		<div class="scenario-card-action">
			<button class="primary-button" type="button" :disabled="starting" @click="emit('start', scenario.id)">
				{{ starting ? 'Starting...' : 'Start scenario' }}
			</button>
		</div>
	</article>
</template>
