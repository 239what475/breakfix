<script setup lang="ts">
import { ref, watch, nextTick } from 'vue'
import QRCode from 'qrcode'
import { api, setToken } from '../composables/useApi'
import { useToast } from '../composables/toast'

const props = defineProps<{ show: boolean; mode: 'login' | 'register' }>()
const emit = defineEmits(['update:show', 'update:mode', 'authenticated'])

const { show: toast } = useToast()

// Close on Escape
if (typeof window !== 'undefined') {
  window.addEventListener('keydown', (e) => {
    if (e.key === 'Escape' && props.show) emit('update:show', false)
  })
}

const username = ref('')
const password = ref('')
const totp = ref('')
const loading = ref(false)
const totpSecret = ref('')
const totpUrl = ref('')
const qrCanvas = ref<HTMLCanvasElement>()

watch(totpUrl, async (url) => {
  await nextTick()
  if (qrCanvas.value && url) {
    await QRCode.toCanvas(qrCanvas.value, url, { width: 160 })
  }
})

watch(() => props.show, (v) => {
  if (!v) { username.value = ''; password.value = ''; totp.value = ''; totpSecret.value = ''; totpUrl.value = ''; loading.value = false }
})

function close() { emit('update:show', false) }

async function doRegister() {
  loading.value = true
  try {
    const res = await api.register(username.value, password.value)
    totpSecret.value = res.totp_secret
    totpUrl.value = res.totp_url
    toast('Account created! Scan QR code and save the secret.', 'success')
  } catch (e: any) {
    toast(e.message, 'error')
  } finally {
    loading.value = false
  }
}

async function doLogin() {
  loading.value = true
  try {
    const res = await api.login(username.value, password.value, totp.value)
    setToken(res.token)
    toast(`Welcome back, ${res.name}!`, 'success')
    close()
    emit('authenticated')
  } catch (e: any) {
    toast(e.message, 'error')
  } finally {
    loading.value = false
  }
}

function switchToLogin() {
  emit('update:mode', 'login')
  totpSecret.value = ''
  totpUrl.value = ''
}
</script>

<template>
  <Teleport to="body">
    <div v-if="show" class="fixed inset-0 z-40 flex items-center justify-center">
      <div class="absolute inset-0 bg-black/60 backdrop-blur-sm" @click="close" />
      <div class="relative w-full max-w-sm p-6 bg-zinc-900 border border-zinc-800 rounded-xl shadow-2xl">

        <!-- Login mode -->
        <template v-if="mode === 'login'">
          <h2 class="text-lg font-bold text-white mb-4">Sign In</h2>
          <form @submit.prevent="doLogin" class="space-y-3">
            <input v-model="username" type="text" placeholder="Username"
              class="w-full px-3 py-2 bg-zinc-800 border border-zinc-700 rounded-lg text-white placeholder-zinc-500 focus:outline-none focus:border-zinc-500 text-sm" />
            <input v-model="password" type="password" placeholder="Password"
              class="w-full px-3 py-2 bg-zinc-800 border border-zinc-700 rounded-lg text-white placeholder-zinc-500 focus:outline-none focus:border-zinc-500 text-sm" />
            <input v-model="totp" type="text" placeholder="TOTP Code"
              class="w-full px-3 py-2 bg-zinc-800 border border-zinc-700 rounded-lg text-white placeholder-zinc-500 focus:outline-none focus:border-zinc-500 text-sm font-mono" />
            <button type="submit" :disabled="loading"
              class="w-full py-2 bg-white text-black font-medium rounded-lg hover:bg-zinc-200 disabled:opacity-50 text-sm transition-colors">
              {{ loading ? 'Signing in...' : 'Sign In' }}
            </button>
          </form>
          <p class="text-zinc-500 text-xs mt-3 text-center">
            No account? <button @click="emit('update:mode', 'register')" class="text-zinc-300 hover:text-white transition-colors">Register</button>
          </p>
        </template>

        <!-- Register mode -->
        <template v-if="mode === 'register'">
          <h2 class="text-lg font-bold text-white mb-4">Create Account</h2>

          <template v-if="totpSecret">
            <div class="space-y-3">
              <div class="bg-zinc-800 border border-zinc-700 rounded-lg p-3">
                <p class="text-[10px] text-zinc-500 mb-1 uppercase tracking-wider">TOTP Secret</p>
                <p class="text-white font-mono text-xs break-all">{{ totpSecret }}</p>
              </div>
              <div class="flex justify-center">
                <canvas ref="qrCanvas" width="160" height="160" class="bg-white p-1.5 rounded"></canvas>
              </div>
              <button @click="switchToLogin"
                class="w-full py-2 bg-white text-black font-medium rounded-lg hover:bg-zinc-200 text-sm transition-colors">
                Continue to Sign In
              </button>
            </div>
          </template>

          <form v-else @submit.prevent="doRegister" class="space-y-3">
            <input v-model="username" type="text" placeholder="Username"
              class="w-full px-3 py-2 bg-zinc-800 border border-zinc-700 rounded-lg text-white placeholder-zinc-500 focus:outline-none focus:border-zinc-500 text-sm" />
            <input v-model="password" type="password" placeholder="Password (min 6 chars)"
              class="w-full px-3 py-2 bg-zinc-800 border border-zinc-700 rounded-lg text-white placeholder-zinc-500 focus:outline-none focus:border-zinc-500 text-sm" />
            <button type="submit" :disabled="loading"
              class="w-full py-2 bg-white text-black font-medium rounded-lg hover:bg-zinc-200 disabled:opacity-50 text-sm transition-colors">
              {{ loading ? 'Creating...' : 'Register' }}
            </button>
          </form>

          <p v-if="!totpSecret" class="text-zinc-500 text-xs mt-3 text-center">
            Have an account? <button @click="emit('update:mode', 'login')" class="text-zinc-300 hover:text-white transition-colors">Sign In</button>
          </p>
        </template>

      </div>
    </div>
  </Teleport>
</template>
