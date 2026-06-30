<script setup lang="ts">
import { ref, onMounted, onUnmounted, nextTick } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import { api, isLoggedIn, token } from '../composables/useApi'
import '@xterm/xterm/css/xterm.css'

const route = useRoute()
const router = useRouter()
const terminalEl = ref<HTMLDivElement>()
const title = ref('')
const passed = ref<boolean | null>(null)
const submitOutput = ref('')

let term: Terminal | null = null
let ws: WebSocket | null = null
let fitAddon: FitAddon | null = null

onMounted(async () => {
  if (!isLoggedIn()) { router.push('/login'); return }

  const id = route.params.id as string
  try {
    const res = await api.startChallenge(id)
    title.value = res.challenge_title
  } catch { /* ignore */ }

  await nextTick()
  if (!terminalEl.value) return

  term = new Terminal({
    cursorBlink: true,
    fontSize: 14,
    fontFamily: "'JetBrains Mono', 'Fira Code', 'Cascadia Code', monospace",
    theme: { background: '#0a0a0a', foreground: '#e0e0e0', cursor: '#ffffff' },
    allowProposedApi: true,
  })

  fitAddon = new FitAddon()
  term.loadAddon(fitAddon)
  term.open(terminalEl.value)
  fitAddon.fit()

  const protocol = location.protocol === 'https:' ? 'wss:' : 'ws:'
  const wsUrl = `${protocol}//${location.host}/api/challenges/${id}/terminal`
  const headers = token() ? `?token=${encodeURIComponent(token()!)}` : ''
  ws = new WebSocket(wsUrl + headers)

  ws.onmessage = (e) => {
    try {
      const msg = JSON.parse(e.data)
      if (msg.type === 'data' && term) {
        term.write(new Uint8Array(msg.data))
      }
    } catch {
      if (term) term.write(e.data)
    }
  }

  ws.onclose = () => { term?.write('\r\n[33mConnection closed[0m\r\n') }
  ws.onerror = () => { term?.write('\r\n[31mConnection error[0m\r\n') }

  term.onData((data) => {
    if (ws?.readyState === WebSocket.OPEN) {
      ws.send(JSON.stringify({ type: 'data', data: Array.from(new TextEncoder().encode(data)) }))
    }
  })

  const observer = new ResizeObserver(() => fitAddon?.fit())
  observer.observe(terminalEl.value)
})

onUnmounted(() => {
  term?.dispose()
  ws?.close()
})

async function submit() {
  const id = route.params.id as string
  try {
    const res = await api.submitChallenge(id)
    passed.value = res.passed
    submitOutput.value = res.output
  } catch (e: any) {
    passed.value = false
    submitOutput.value = e.message
  }
}

async function reset() {
  const id = route.params.id as string
  try {
    await api.resetChallenge(id)
    passed.value = null
    submitOutput.value = ''
  } catch { /* ignore */ }
}
</script>

<template>
  <div class="h-screen flex flex-col bg-zinc-950">
    <!-- Header -->
    <div class="flex items-center justify-between px-4 py-2 border-b border-zinc-800 flex-shrink-0">
      <div class="flex items-center gap-3">
        <router-link to="/" class="text-zinc-500 hover:text-white transition-colors text-sm">← Back</router-link>
        <span class="text-zinc-700">|</span>
        <span class="text-white text-sm font-medium">{{ title || route.params.id }}</span>
      </div>
      <div class="flex items-center gap-2">
        <button @click="submit"
          class="px-3 py-1.5 bg-emerald-600 text-white text-xs font-medium rounded hover:bg-emerald-500 transition-colors">
          Submit
        </button>
        <button @click="reset"
          class="px-3 py-1.5 bg-zinc-800 text-zinc-300 text-xs font-medium rounded hover:bg-zinc-700 transition-colors">
          Reset
        </button>
      </div>
    </div>

    <!-- Terminal -->
    <div ref="terminalEl" class="flex-1 overflow-hidden" />

    <!-- Submit result -->
    <div v-if="passed !== null"
      :class="['px-4 py-3 border-t text-sm flex-shrink-0',
               passed ? 'border-emerald-800 bg-emerald-950/50 text-emerald-400' : 'border-red-800 bg-red-950/50 text-red-400']">
      <p class="font-medium">{{ passed ? '✓ PASSED!' : '✗ FAILED' }}</p>
      <pre v-if="submitOutput" class="text-xs mt-1 opacity-75 whitespace-pre-wrap">{{ submitOutput }}</pre>
    </div>
  </div>
</template>
