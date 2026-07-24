<script setup lang="ts">
import type { Challenge } from "../../api/types";
import { challengeStatus, formatDifficulty, formatPublishedAt, formatRuntime } from "./catalog";

const props = defineProps<{
	challenge: Challenge;
	loggedIn: boolean;
	starting: boolean;
}>();
const emit = defineEmits<{ start: [id: string] }>();
</script>

<template>
	<article class="challenge-card">
		<div class="challenge-card-main">
			<div class="challenge-card-topline">
				<div class="challenge-pills">
					<span class="runtime-pill">{{ formatRuntime(challenge.runtime) }}</span>
					<span :class="['difficulty', challenge.difficulty]">{{ formatDifficulty(challenge.difficulty) }}</span>
				</div>
				<time :datetime="challenge.published_at">{{ formatPublishedAt(challenge.published_at) }}</time>
			</div>
			<h2>{{ challenge.title }}</h2>
			<p class="challenge-card-description">{{ challenge.description }}</p>
			<div class="challenge-card-bottom">
				<div class="challenge-tags" aria-label="Challenge tags">
					<span v-for="tag in challenge.tags" :key="tag">{{ tag }}</span>
				</div>
				<div v-if="loggedIn" class="challenge-state" :class="challengeStatus(challenge)">
					<span v-if="challengeStatus(challenge) === 'in-progress'">
						In progress<span v-if="challenge.progress"> · {{ challenge.progress.passed }}/{{ challenge.progress.total }} checkpoints</span>
					</span>
					<span v-else-if="challengeStatus(challenge) === 'completed'">Completed</span>
					<span v-else>Todo</span>
					<span v-if="challenge.active && challenge.solved" class="completion-history">Completed before</span>
				</div>
			</div>
		</div>
		<div class="challenge-card-action">
			<button class="primary-button" type="button" :disabled="starting" @click="emit('start', challenge.id)">
				{{ starting ? 'Starting...' : 'Start challenge' }}
			</button>
		</div>
	</article>
</template>
