<script setup lang="ts">
import { useToast } from '../composables/toast'

const { toasts } = useToast()
</script>

<template>
  <div class="fixed top-4 right-4 z-50 flex flex-col gap-2 pointer-events-none">
    <div v-for="t in toasts" :key="t.id"
      :class="['px-4 py-3 rounded-lg text-sm font-medium shadow-lg pointer-events-auto transition-all duration-300',
               t.type === 'success' ? 'bg-emerald-600 text-white' :
               t.type === 'error' ? 'bg-red-600 text-white' :
               'bg-zinc-700 text-zinc-200']">
      {{ t.message }}
    </div>
  </div>
</template>
