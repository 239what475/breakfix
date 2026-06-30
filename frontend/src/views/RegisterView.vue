<script setup lang="ts">
import { ref } from 'vue'
import { api } from '../composables/useApi'

const username = ref('')
const password = ref('')
const error = ref('')
const loading = ref(false)
const result = ref<{ totp_secret: string; totp_url: string } | null>(null)

async function doRegister() {
  error.value = ''
  loading.value = true
  try {
    result.value = await api.register(username.value, password.value)
  } catch (e: any) {
    error.value = e.message
  } finally {
    loading.value = false
  }
}
</script>

<template>
  <div class="min-h-screen flex items-center justify-center bg-zinc-950">
    <div class="w-full max-w-sm p-8 space-y-6">
      <div class="text-center">
        <h1 class="text-2xl font-bold text-white tracking-tight">Create Account</h1>
        <p class="text-zinc-500 mt-1 text-sm">Breakfix SRE Platform</p>
      </div>

      <div v-if="result" class="space-y-4 text-center">
        <div class="bg-zinc-900 border border-zinc-800 rounded-lg p-4">
          <p class="text-xs text-zinc-500 mb-2 font-mono">TOTP Secret</p>
          <p class="text-white font-mono text-sm break-all">{{ result.totp_secret }}</p>
        </div>
        <div class="bg-zinc-900 border border-zinc-800 rounded-lg p-4">
          <p class="text-xs text-zinc-500 mb-2">Scan QR Code</p>
          <div class="bg-white p-2 rounded inline-block">
            <img :src="`https://api.qrserver.com/v1/create-qr-code/?size=180x180&data=${encodeURIComponent(result.totp_url)}`"
              width="180" height="180" alt="TOTP QR Code" />
          </div>
        </div>
        <p class="text-zinc-500 text-sm">Save this secret, then</p>
        <router-link to="/login"
          class="block w-full py-2.5 bg-white text-black font-medium rounded-lg hover:bg-zinc-200 text-sm transition-colors text-center">
          Go to Login
        </router-link>
      </div>

      <form v-else @submit.prevent="doRegister" class="space-y-4">
        <div>
          <input v-model="username" type="text" placeholder="Username"
            class="w-full px-3 py-2.5 bg-zinc-900 border border-zinc-800 rounded-lg text-white placeholder-zinc-600 focus:outline-none focus:border-zinc-600 text-sm" />
        </div>
        <div>
          <input v-model="password" type="password" placeholder="Password (min 6 chars)"
            class="w-full px-3 py-2.5 bg-zinc-900 border border-zinc-800 rounded-lg text-white placeholder-zinc-600 focus:outline-none focus:border-zinc-600 text-sm" />
        </div>
        <div v-if="error" class="text-red-400 text-sm">{{ error }}</div>
        <button type="submit" :disabled="loading"
          class="w-full py-2.5 bg-white text-black font-medium rounded-lg hover:bg-zinc-200 disabled:opacity-50 text-sm transition-colors">
          {{ loading ? 'Creating...' : 'Register' }}
        </button>
      </form>
    </div>
  </div>
</template>
