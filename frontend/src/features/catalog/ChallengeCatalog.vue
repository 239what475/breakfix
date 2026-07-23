<script setup lang="ts">
import { computed, ref } from "vue";
import type { Challenge } from "../../api/types";
import "./catalog.css";

const props = defineProps<{
  challenges: Challenge[];
  selectedId: string | null;
  loading: boolean;
  loggedIn: boolean;
}>();
const emit = defineEmits<{
  select: [id: string];
  login: [];
  register: [];
  logout: [];
  generate: [];
}>();
const query = ref("");
const filtered = computed(() => {
  const value = query.value.trim().toLowerCase();
  if (!value) return props.challenges;
  return props.challenges.filter((challenge) =>
    [
      challenge.title,
      challenge.description,
      challenge.difficulty,
      ...challenge.tags,
    ]
      .join(" ")
      .toLowerCase()
      .includes(value),
  );
});
</script>

<template>
  <aside class="catalog-pane">
    <header class="catalog-header">
      <div class="brand">
        <span class="brand-symbol">B</span><span>breakfix</span>
      </div>
      <div v-if="loggedIn" class="catalog-actions">
        <button class="text-button" @click="emit('generate')">Generate</button
        ><button class="text-button" @click="emit('logout')">Sign out</button>
      </div>
      <div v-else class="catalog-actions">
        <button class="text-button" @click="emit('login')">Sign in</button
        ><button class="compact-button" @click="emit('register')">
          Register
        </button>
      </div>
    </header>
    <div class="catalog-search">
      <input
        v-model="query"
        placeholder="Search challenges"
        aria-label="Search challenges"
      />
    </div>
    <div class="catalog-summary">
      <span>{{ challenges.length }} labs</span
      ><span>{{ challenges.filter((item) => item.solved).length }} solved</span>
    </div>
    <nav class="challenge-list" aria-label="Challenge catalog">
      <p v-if="loading" class="empty-copy">Loading challenges...</p>
      <button
        v-for="challenge in filtered"
        :key="challenge.id"
        class="challenge-row"
        :class="{ selected: challenge.id === selectedId }"
        @click="emit('select', challenge.id)"
      >
        <span class="challenge-row-top"
          ><strong>{{ challenge.title }}</strong
          ><span class="runtime-pill">{{ challenge.runtime }}</span></span
        >
        <span class="challenge-description">{{ challenge.description }}</span>
        <span class="challenge-meta"
          ><span :class="['difficulty', challenge.difficulty]">{{
            challenge.difficulty
          }}</span
          ><span v-if="challenge.active">active</span
          ><span v-else-if="challenge.solved">solved</span></span
        >
      </button>
      <p v-if="!loading && !filtered.length" class="empty-copy">
        No matching challenges.
      </p>
    </nav>
  </aside>
</template>
