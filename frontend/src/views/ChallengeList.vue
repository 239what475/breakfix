<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import { api, isLoggedIn, clearToken, type Challenge } from '../composables/useApi'
import AuthModal from '../components/AuthModal.vue'
import ToastContainer from '../components/ToastContainer.vue'
import { useToast } from '../composables/toast'

const router = useRouter()
const { show: toast } = useToast()

const challenges = ref<Challenge[]>([])
const loading = ref(true)
const selectedId = ref<string | null>(null)
const loggedIn = ref(false)
const showAuth = ref(false)
const authMode = ref<'login' | 'register'>('login')

onMounted(async () => {
  loggedIn.value = isLoggedIn()
  if (loggedIn.value) await loadChallenges()
  else loading.value = false
})

async function loadChallenges() {
  loading.value = true
  try {
    const res = await api.listChallenges()
    challenges.value = res.challenges || []
  } catch { /* ignore */ }
  loading.value = false
}

function onAuthenticated() {
  loggedIn.value = true
  loadChallenges()
}

function logout() {
  clearToken()
  loggedIn.value = false
  challenges.value = []
  selectedId.value = null
  toast('Signed out', 'info')
}

function diffColor(d: string) {
  return d === 'easy' ? 'text-emerald-400' : d === 'medium' ? 'text-amber-400' : 'text-red-400'
}

async function start(ch: Challenge) {
  try {
    await api.startChallenge(ch.id)
    router.push(`/terminal/${ch.id}`)
  } catch (e: any) {
    toast(e.message, 'error')
  }
}

async function submit(ch: Challenge) {
  try {
    const res = await api.submitChallenge(ch.id)
    if (res.passed) toast('✓ PASSED!', 'success')
    else toast(`✗ FAILED (exit ${res.exit_code})`, 'error')
    await loadChallenges()
  } catch (e: any) {
    toast(e.message, 'error')
  }
}

async function doReset(ch: Challenge) {
  try {
    await api.resetChallenge(ch.id)
    toast('Challenge reset', 'success')
  } catch (e: any) {
    toast(e.message, 'error')
  }
}
</script>

<template>
  <div class="h-screen flex bg-zinc-950">
    <!-- Sidebar -->
    <div class="w-80 border-r border-zinc-800 flex flex-col flex-shrink-0">
      <div class="p-4 border-b border-zinc-800 flex items-center justify-between">
        <h1 class="text-lg font-bold text-white tracking-tight">Breakfix</h1>
        <div class="flex items-center gap-2">
          <span v-if="loggedIn" class="text-[10px] text-zinc-600">SRE</span>
          <button v-if="loggedIn" @click="logout"
            class="text-[10px] text-zinc-500 hover:text-zinc-300 transition-colors">Out</button>
        </div>
      </div>

      <!-- Auth prompt or challenge list -->
      <template v-if="!loggedIn">
        <div class="flex-1 flex flex-col items-center justify-center p-6 text-center gap-3">
          <svg class="w-10 h-10 text-zinc-700" fill="none" stroke="currentColor" viewBox="0 0 24 24">
            <path stroke-linecap="round" stroke-linejoin="round" stroke-width="1.5"
              d="M12 15v2m-6 4h12a2 2 0 002-2v-6a2 2 0 00-2-2H6a2 2 0 00-2 2v6a2 2 0 002 2zm10-10V7a4 4 0 00-8 0v4h8z" />
          </svg>
          <p class="text-zinc-400 text-sm font-medium">Sign in to practice</p>
          <p class="text-zinc-600 text-xs">SRE/DevOps interview challenges</p>
          <div class="flex gap-2 mt-2">
            <button @click="showAuth = true; authMode = 'login'"
              class="px-4 py-1.5 bg-white text-black text-xs font-medium rounded-lg hover:bg-zinc-200 transition-colors">
              Sign In
            </button>
            <button @click="showAuth = true; authMode = 'register'"
              class="px-4 py-1.5 bg-zinc-800 text-zinc-300 text-xs font-medium rounded-lg hover:bg-zinc-700 transition-colors">
              Register
            </button>
          </div>
        </div>
      </template>

      <template v-else>
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
      </template>
    </div>

    <!-- Main area -->
    <div class="flex-1 flex flex-col items-center justify-center text-zinc-600">
      <template v-if="loggedIn && selectedId">
        <div class="text-center">
          <p class="text-white text-lg mb-2">{{ challenges.find(c => c.id === selectedId)?.title }}</p>
          <p class="text-zinc-500 text-xs mb-6 font-mono">{{ selectedId }}</p>
          <div class="flex gap-3">
            <button @click="start(challenges.find(c => c.id === selectedId)!)"
              class="px-5 py-2 bg-white text-black text-sm font-medium rounded-lg hover:bg-zinc-200 transition-colors">
              Start Challenge
            </button>
            <button v-if="challenges.find(c => c.id === selectedId)?.active"
              @click="router.push(`/terminal/${selectedId}`)"
              class="px-5 py-2 bg-zinc-800 text-white text-sm font-medium rounded-lg hover:bg-zinc-700 transition-colors">
              Resume
            </button>
          </div>
          <div class="flex gap-2 mt-3 justify-center">
            <button @click="submit(challenges.find(c => c.id === selectedId)!)"
              class="px-3 py-1.5 bg-emerald-600 text-white text-xs font-medium rounded hover:bg-emerald-500 transition-colors">
              Submit
            </button>
            <button @click="doReset(challenges.find(c => c.id === selectedId)!)"
              class="px-3 py-1.5 bg-zinc-800 text-zinc-300 text-xs font-medium rounded hover:bg-zinc-700 transition-colors">
              Reset
            </button>
          </div>
        </div>
      </template>
      <template v-else-if="loggedIn">
        <svg class="w-12 h-12 mb-4 text-zinc-800" fill="none" stroke="currentColor" viewBox="0 0 24 24">
          <path stroke-linecap="round" stroke-linejoin="round" stroke-width="1"
            d="M8 9l3 3-3 3m5 0h3M5 20h14a2 2 0 002-2V6a2 2 0 00-2-2H5a2 2 0 00-2 2v12a2 2 0 002 2z" />
        </svg>
        <p class="text-sm">Select a challenge to begin</p>
      </template>
      <template v-else>
        <svg class="w-12 h-12 mb-4 text-zinc-800" fill="none" stroke="currentColor" viewBox="0 0 24 24">
          <path stroke-linecap="round" stroke-linejoin="round" stroke-width="1.5"
            d="M15 7a2 2 0 012 2m4 0a6 6 0 01-7.743 5.743L11 17H9v2H7v2H4a1 1 0 01-1-1v-2.586a1 1 0 01.293-.707l5.964-5.964A6 6 0 1121 9z" />
        </svg>
        <p class="text-sm">Sign in to view challenges</p>
      </template>
    </div>

    <!-- Auth Modal -->
    <AuthModal :show="showAuth" :mode="authMode"
      @update:show="v => showAuth = v" @update:mode="v => authMode = v"
      @authenticated="onAuthenticated" />

    <!-- Toast notifications -->
    <ToastContainer />
  </div>
</template>
