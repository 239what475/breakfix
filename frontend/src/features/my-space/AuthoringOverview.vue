<script setup lang="ts">
import { ArrowUpRight, FilePenLine, UsersRound } from "lucide-vue-next";
import type { MySpaceAuthoring } from "../../api/types";

defineProps<{ authoring: MySpaceAuthoring }>();
const emit = defineEmits<{ authoring: [sessionId?: string]; catalog: [id: string] }>();

function date(value: string) {
  return new Intl.DateTimeFormat(undefined, { month: "short", day: "numeric" }).format(new Date(value));
}
function rate(value?: number) {
  return value === undefined ? "--" : `${Math.round(value * 100)}%`;
}
</script>

<template>
  <section class="space-section authoring-overview" aria-labelledby="authoring-title">
    <div class="space-section-heading"><div><p class="eyebrow">Create challenges</p><h2 id="authoring-title">Authoring</h2></div><button class="text-button" type="button" @click="emit('authoring')">Open studio</button></div>
    <div v-if="authoring.drafts.length" class="authoring-drafts">
      <button v-for="draft in authoring.drafts" :key="draft.session_id" class="authoring-draft" type="button" @click="emit('authoring', draft.session_id)"><FilePenLine :size="16" aria-hidden="true" /><span><strong>{{ draft.title }}</strong><small>{{ draft.state }} · Updated {{ date(draft.updated_at) }}</small></span><ArrowUpRight :size="15" aria-hidden="true" /></button>
    </div>
    <p v-else class="space-empty">No active authoring session.</p>
    <div class="published-heading"><h3>Published challenges</h3><span>{{ authoring.published.length }}</span></div>
    <div v-if="authoring.published.length" class="published-grid">
      <article v-for="published in authoring.published" :key="published.challenge.id" class="published-card">
        <div><p class="eyebrow">{{ published.challenge.runtime }} · {{ date(published.published_at) }}</p><h3>{{ published.challenge.title }}</h3></div>
        <dl><div><dt><UsersRound :size="13" aria-hidden="true" />Attempts</dt><dd>{{ published.attempted_users }}</dd></div><div><dt>Completed</dt><dd>{{ published.completed_users }}</dd></div><div><dt>Pass rate</dt><dd>{{ rate(published.pass_rate) }}</dd></div></dl>
        <button class="text-button" type="button" @click="emit('catalog', published.challenge.id)">View in catalog</button>
      </article>
    </div>
    <p v-else class="space-empty">Published challenges with a current catalog entry will appear here.</p>
  </section>
</template>
