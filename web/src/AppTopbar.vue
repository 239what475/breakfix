<script setup lang="ts">
import { LogOut, Menu, X } from "lucide-vue-next";
import { computed, ref } from "vue";

const props = withDefaults(
  defineProps<{
    active?: "catalog" | "my-space" | "studio" | "none";
    showNavigation?: boolean;
    loggedIn: boolean;
    accountName?: string;
  }>(),
  {
    active: "none",
    showNavigation: true,
  },
);

const emit = defineEmits<{ catalog: []; mySpace: []; studio: []; login: []; register: []; logout: [] }>();
const initials = computed(() => props.accountName?.slice(0, 1).toUpperCase() || "?");
const mobileNavigationOpen = ref(false);

function navigateMobile(target: "catalog" | "mySpace") {
  mobileNavigationOpen.value = false;
  if (target === "catalog") {
    emit("catalog");
    return;
  }
  emit("mySpace");
}
</script>

<template>
  <header class="app-topbar">
    <div class="app-topbar-start"><button class="brand brand-button" type="button" aria-label="Open catalog" @click="navigateMobile('catalog')"><span class="brand-symbol">B</span><span>breakfix</span></button></div>
    <nav v-if="showNavigation" class="app-global-nav" aria-label="Primary">
      <button :class="{ active: active === 'catalog' }" :aria-current="active === 'catalog' ? 'page' : undefined" type="button" @click="emit('catalog')">Catalog</button>
      <button :class="{ active: active === 'my-space' }" :aria-current="active === 'my-space' ? 'page' : undefined" type="button" @click="emit('mySpace')">My space</button>
      <button :class="{ active: active === 'studio' }" :aria-current="active === 'studio' ? 'page' : undefined" type="button" @click="emit('studio')">Challenge studio</button>
    </nav>
    <nav v-if="showNavigation && mobileNavigationOpen" class="app-mobile-nav" aria-label="Mobile primary">
      <button :class="{ active: active === 'catalog' }" :aria-current="active === 'catalog' ? 'page' : undefined" type="button" @click="navigateMobile('catalog')">Catalog</button>
      <button :class="{ active: active === 'my-space' }" :aria-current="active === 'my-space' ? 'page' : undefined" type="button" @click="navigateMobile('mySpace')">My space</button>
    </nav>
    <div class="app-topbar-end">
      <template v-if="loggedIn">
        <button class="icon-button app-mobile-nav-toggle" type="button" title="Navigation" aria-label="Navigation" :aria-expanded="mobileNavigationOpen" @click="mobileNavigationOpen = !mobileNavigationOpen"><X v-if="mobileNavigationOpen" :size="16" aria-hidden="true" /><Menu v-else :size="16" aria-hidden="true" /></button><span class="app-account-avatar" aria-hidden="true">{{ initials }}</span><span class="app-account-name">{{ accountName || "Account" }}</span><button class="icon-button app-logout-button" type="button" title="Sign out" aria-label="Sign out" @click="emit('logout')"><LogOut :size="15" aria-hidden="true" /></button>
      </template>
      <template v-else><button class="text-button" type="button" @click="emit('login')">Sign in</button><button class="compact-button app-register-button" type="button" @click="emit('register')">Register</button></template>
    </div>
  </header>
</template>
