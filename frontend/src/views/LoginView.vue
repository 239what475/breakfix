<script setup lang="ts">
import { ref } from 'vue'
import { useRouter } from 'vue-router'
import { api, setToken } from '../composables/useApi'

const router = useRouter()
const username = ref('')
const password = ref('')
const totp = ref('')
const error = ref('')
const loading = ref(false)

async function doLogin() {
  error.value = ''
  loading.value = true
  try {
    const res = await api.login(username.value, password.value, totp.value)
    setToken(res.token)
    router.push('/')
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
        <h1 class="text-2xl font-bold text-white tracking-tight">Breakfix</h1>
        <p class="text-zinc-500 mt-1 text-sm">SRE/DevOps Interview Practice</p>
      </div>
      <form @submit.prevent="doLogin" class="space-y-4">
        <div>
          <input v-model="username" type="text" placeholder="Username"
            class="w-full px-3 py-2.5 bg-zinc-900 border border-zinc-800 rounded-lg text-white placeholder-zinc-600 focus:outline-none focus:border-zinc-600 text-sm" />
        </div>
        <div>
          <input v-model="password" type="password" placeholder="Password"
            class="w-full px-3 py-2.5 bg-zinc-900 border border-zinc-800 rounded-lg text-white placeholder-zinc-600 focus:outline-none focus:border-zinc-600 text-sm" />
        </div>
        <div>
          <input v-model="totp" type="text" placeholder="TOTP Code"
            class="w-full px-3 py-2.5 bg-zinc-900 border border-zinc-800 rounded-lg text-white placeholder-zinc-600 focus:outline-none focus:border-zinc-600 text-sm font-mono" />
        </div>
        <div v-if="error" class="text-red-400 text-sm">{{ error }}</div>
        <button type="submit" :disabled="loading"
          class="w-full py-2.5 bg-white text-black font-medium rounded-lg hover:bg-zinc-200 disabled:opacity-50 text-sm transition-colors">
          {{ loading ? 'Signing in...' : 'Sign in' }}
        </button>
      </form>
      <p class="text-center text-zinc-600 text-sm">
        No account? <router-link to="/register" class="text-zinc-400 hover:text-white transition-colors">Register</router-link>
      </p>
    </div>
  </div>
</template>
