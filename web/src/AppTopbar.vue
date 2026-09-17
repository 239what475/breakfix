<script setup lang="ts">
import { LogOut, Menu, X } from "lucide-vue-next";
import { computed, ref } from "vue";

const props = withDefaults(
  defineProps<{
    active?: "operations" | "my-space" | "documentation" | "admin" | "none";
    showNavigation?: boolean;
    loggedIn: boolean;
    isAdmin?: boolean;
    accountName?: string;
  }>(),
  {
    active: "none",
    showNavigation: true,
    isAdmin: false,
  },
);

const emit = defineEmits<{ operations: []; mySpace: []; documentation: []; admin: []; login: []; register: []; logout: [] }>();
const initials = computed(() => props.accountName?.slice(0, 1).toUpperCase() || "?");
const mobileNavigationOpen = ref(false);

function navigateMobile(target: "operations" | "mySpace" | "documentation" | "admin") {
	mobileNavigationOpen.value = false;
	if (target === "operations") {
		emit("operations");
    return;
  }
	if (target === "documentation") {
		emit("documentation");
		return;
	}
	if (target === "admin") {
		emit("admin");
		return;
	}
  emit("mySpace");
}
</script>

<template>
  <header class="app-topbar">
    <div class="app-topbar-start"><button class="brand brand-button" type="button" aria-label="Open operations scenarios" @click="navigateMobile('operations')"><span class="brand-symbol">B</span><span>breakfix</span></button></div>
    <nav v-if="showNavigation" class="app-global-nav" aria-label="Primary">
      <button v-if="loggedIn" :class="{ active: active === 'operations' }" :aria-current="active === 'operations' ? 'page' : undefined" type="button" @click="emit('operations')">Operations</button>
      <button v-if="loggedIn" :class="{ active: active === 'my-space' }" :aria-current="active === 'my-space' ? 'page' : undefined" type="button" @click="emit('mySpace')">My space</button>
      <button :class="{ active: active === 'documentation' }" :aria-current="active === 'documentation' ? 'page' : undefined" type="button" @click="emit('documentation')">Documentation</button>
      <button v-if="loggedIn && isAdmin" :class="{ active: active === 'admin' }" :aria-current="active === 'admin' ? 'page' : undefined" type="button" @click="emit('admin')">管理</button>
    </nav>
    <nav v-if="showNavigation && mobileNavigationOpen" class="app-mobile-nav" aria-label="Mobile primary">
      <button v-if="loggedIn" :class="{ active: active === 'operations' }" :aria-current="active === 'operations' ? 'page' : undefined" type="button" @click="navigateMobile('operations')">Operations</button>
      <button v-if="loggedIn" :class="{ active: active === 'my-space' }" :aria-current="active === 'my-space' ? 'page' : undefined" type="button" @click="navigateMobile('mySpace')">My space</button>
      <button :class="{ active: active === 'documentation' }" :aria-current="active === 'documentation' ? 'page' : undefined" type="button" @click="navigateMobile('documentation')">Documentation</button>
      <button v-if="loggedIn && isAdmin" :class="{ active: active === 'admin' }" :aria-current="active === 'admin' ? 'page' : undefined" type="button" @click="navigateMobile('admin')">管理</button>
    </nav>
    <div class="app-topbar-end">
      <button v-if="showNavigation" class="icon-button app-mobile-nav-toggle" type="button" title="Navigation" aria-label="Navigation" :aria-expanded="mobileNavigationOpen" @click="mobileNavigationOpen = !mobileNavigationOpen"><X v-if="mobileNavigationOpen" :size="16" aria-hidden="true" /><Menu v-else :size="16" aria-hidden="true" /></button>
      <template v-if="loggedIn">
        <span class="app-account-avatar" aria-hidden="true">{{ initials }}</span><span class="app-account-name">{{ accountName || "Account" }}</span><button class="icon-button app-logout-button" type="button" title="Sign out" aria-label="Sign out" @click="emit('logout')"><LogOut :size="15" aria-hidden="true" /></button>
      </template>
      <template v-else><button class="text-button" type="button" @click="emit('login')">Sign in</button><button class="compact-button app-register-button" type="button" @click="emit('register')">Register</button></template>
    </div>
  </header>
</template>
