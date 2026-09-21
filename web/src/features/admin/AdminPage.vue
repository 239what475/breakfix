<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { Boxes, RefreshCw, ScrollText, ShieldCheck, Users } from "lucide-vue-next";
import AdminEnvironmentsPage from "./AdminEnvironmentsPage.vue";
import AdminAuditPage from "./AdminAuditPage.vue";
import AdminUsersPage from "./AdminUsersPage.vue";
import "./admin.css";

type AdminSection = "users" | "audit" | "environments";
const props = defineProps<{ active: boolean; loggedIn: boolean; section: AdminSection }>();
const emit = defineEmits<{ navigate: [section: AdminSection] }>();

const tab = ref<AdminSection>(props.section);

watch(
  () => props.section,
  (value) => {
    tab.value = value;
  },
);

const heading = computed(() => ({ users: "账号管理", audit: "人操作审计", environments: "环境观测" })[tab.value]);

// Users and audit own their fetches; the header refresh nudges them through
// the same counter pattern AppShell uses for My space.
const refreshRequest = ref(0);

function refreshConsole() {
  refreshRequest.value += 1;
}
</script>

<template>
  <section class="admin-page">
    <div class="admin-layout">
      <aside class="admin-sidebar">
        <div class="admin-identity">
          <span class="admin-avatar"><ShieldCheck :size="17" aria-hidden="true" /></span>
          <div><strong>管理控制面</strong><small>role: admin</small></div>
        </div>
        <nav class="admin-tabs desktop-tabs" aria-label="Admin sections">
          <button :class="{ active: tab === 'users' }" type="button" @click="emit('navigate', 'users')"><Users :size="16" aria-hidden="true" />用户</button>
          <button :class="{ active: tab === 'audit' }" type="button" @click="emit('navigate', 'audit')"><ScrollText :size="16" aria-hidden="true" />审计</button>
          <button :class="{ active: tab === 'environments' }" type="button" @click="emit('navigate', 'environments')"><Boxes :size="16" aria-hidden="true" />环境</button>
        </nav>
      </aside>
      <main class="admin-content">
        <nav class="admin-tabs mobile-tabs" aria-label="Admin sections">
          <button :class="{ active: tab === 'users' }" type="button" @click="emit('navigate', 'users')">用户</button>
          <button :class="{ active: tab === 'audit' }" type="button" @click="emit('navigate', 'audit')">审计</button>
          <button :class="{ active: tab === 'environments' }" type="button" @click="emit('navigate', 'environments')">环境</button>
        </nav>
        <header class="admin-content-header">
          <div><p class="eyebrow">Admin console</p><h1>{{ heading }}</h1></div>
          <div class="admin-content-actions">
            <button class="icon-button" type="button" title="Refresh" aria-label="Refresh" @click="refreshConsole"><RefreshCw :size="15" aria-hidden="true" /></button>
          </div>
        </header>
        <AdminUsersPage v-show="tab === 'users'" :active="props.active && tab === 'users'" :logged-in="props.loggedIn" :refresh-request="refreshRequest" />
        <AdminAuditPage v-show="tab === 'audit'" :active="props.active && tab === 'audit'" :logged-in="props.loggedIn" :refresh-request="refreshRequest" />
        <AdminEnvironmentsPage v-show="tab === 'environments'" :active="props.active && tab === 'environments'" :logged-in="props.loggedIn" :refresh-request="refreshRequest" />
      </main>
    </div>
  </section>
</template>
