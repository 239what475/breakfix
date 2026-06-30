<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import { api, isLoggedIn, type Challenge } from '../composables/useApi'

const router = useRouter()
const challenges = ref<Challenge[]>([])
const loading = ref(true)
const selectedId = ref<string | null>(null)

onMounted(async () => {
  if (!isLoggedIn()) { router.push('/login'); return }
  try {
    const res = await api.listChallenges()
    challenges.value = res.challenges || []
  } catch { /* ignore */ }
  loading.value = false
})

function diffColor(d: string) {
  return d === 'easy' ? 'text-emerald-400' : d === 'medium' ? 'text-amber-400' : 'text-red-400'
}

async function start(ch: Challenge) {
  await api.startChallenge(ch.id)
  router.push(`/terminal/${ch.id}`)
}
</script>

<template>
  <div class="h-screen flex bg-zinc-950">
    <!-- Sidebar -->
    <div class="w-80 border-r border-zinc-800 flex flex-col flex-shrink-0">
      <div class="p-4 border-b border-zinc-800 flex items-center justify-between">
        <h1 class="text-lg font-bold text-white tracking-tight">Breakfix</h1>
        <span class="text-xs text-zinc-600">SRE Practice</span>
      </div>
      <div class="flex-1 overflow-y-auto">
        <div v-if="loading" class="p-4 text-zinc-500 text-sm">Loading...</div>
        <div v-else-if="challenges.length === 0" class="p-4 text-zinc-500 text-sm">
          No challenges yet. Generate one via the API.
        </div>
        <div v-for="ch in challenges" :key="ch.id"
          @click="selectedId = ch.id"
          :class="['p-4 border-b border-zinc-800/50 cursor-pointer transition-colors hover:bg-zinc-900/50',
                   selectedId === ch.id ? 'bg-zinc-900 border-l-2 border-l-white' : '']">
          <div class="flex items-center gap-2 mb-1">
            <span :class="['text-xs font-mono', diffColor(ch.difficulty)]">{{ ch.difficulty }}</span>
            <span v-if="ch.solved" class="text-emerald-400 text-xs">✓</span>
            <span v-if="ch.active" class="text-amber-400 text-xs">● running</span>
          </div>
          <p class="text-white text-sm font-medium">{{ ch.title }}</p>
          <div class="flex gap-1 mt-1.5 flex-wrap">
            <span v-for="t in ch.tags" :key="t"
              class="text-[10px] px-1.5 py-0.5 bg-zinc-800 text-zinc-500 rounded">{{ t }}</span>
          </div>
        </div>
      </div>
    </div>

    <!-- Main area -->
    <div class="flex-1 flex flex-col items-center justify-center text-zinc-600">
      <template v-if="selectedId">
        <p class="text-white text-lg mb-6">{{ challenges.find(c => c.id === selectedId)?.title }}</p>
        <div class="flex gap-3">
          <button @click="start(challenges.find(c => c.id === selectedId)!)"
            class="px-5 py-2 bg-white text-black text-sm font-medium rounded-lg hover:bg-zinc-200 transition-colors">
            Start Challenge
          </button>
          <button @click="router.push(`/terminal/${selectedId}`)"
            class="px-5 py-2 bg-zinc-800 text-white text-sm font-medium rounded-lg hover:bg-zinc-700 transition-colors">
            Resume
          </button>
        </div>
      </template>
      <template v-else>
        <svg class="w-12 h-12 mb-4 text-zinc-800" fill="none" stroke="currentColor" viewBox="0 0 24 24">
          <path stroke-linecap="round" stroke-linejoin="round" stroke-width="1"
            d="M8 9l3 3-3 3m5 0h3M5 20h14a2 2 0 002-2V6a2 2 0 00-2-2H5a2 2 0 00-2 2v12a2 2 0 002 2z" />
        </svg>
        <p class="text-sm">Select a challenge to begin</p>
      </template>
    </div>
  </div>
</template>
