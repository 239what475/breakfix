<script setup lang="ts">
import { ref } from "vue";
import { KeyRound, RefreshCw, X } from "lucide-vue-next";
import { useAdminUsers } from "./admin";
import { toActiveRef, toLoggedInRef } from "./refs";
import { api } from "../../api/client";
import type { AdminUser } from "../../api/generated";
import "../../styles/dialog.css";
import "./admin.css";

const props = defineProps<{ active: boolean; loggedIn: boolean }>();
const { users, loading, error, refresh } = useAdminUsers(toActiveRef(props), toLoggedInRef(props));

const reset = ref<{ user: AdminUser; password: string }>();
const resetError = ref("");
const resetBusy = ref(false);
// The rotated secret is shown exactly once and never re-fetched.
const rotated = ref<{ user: AdminUser; secret: string; url: string }>();

const clock = (value: string) => new Date(value).toLocaleString();
function openReset(user: AdminUser) {
	reset.value = { user, password: "" };
	resetError.value = "";
	rotated.value = undefined;
}

function closeReset() {
	reset.value = undefined;
	resetError.value = "";
}

async function submitReset() {
	if (!reset.value) return;
	resetBusy.value = true;
	resetError.value = "";
	try {
		const response = await api.resetAdminUserTOTP(reset.value.user.id, reset.value.password);
		rotated.value = { user: reset.value.user, secret: response.totp_secret, url: response.totp_url };
		reset.value = undefined;
	} catch (cause) {
		resetError.value = cause instanceof Error ? cause.message : "TOTP 重置失败";
	} finally {
		resetBusy.value = false;
	}
}
</script>

<template>
	<section class="admin-section" aria-labelledby="admin-users-title">
		<div class="space-section-heading">
			<div><p class="eyebrow">Identity</p><h2 id="admin-users-title">用户账号</h2></div>
			<button class="icon-button" type="button" title="Refresh" aria-label="Refresh" @click="refresh()"><RefreshCw :size="15" aria-hidden="true" /></button>
		</div>
		<p v-if="error" class="admin-error">{{ error }}</p>
		<p v-if="loading" class="space-empty">Loading accounts...</p>
		<p v-else-if="!users.length" class="space-empty">暂无用户。</p>
		<div v-else class="admin-workflow-list">
			<article v-for="user in users" :key="user.id" class="admin-workflow-row">
				<div class="admin-workflow-main">
					<div class="admin-workflow-title">
						<h3>{{ user.name }}</h3>
						<span class="workflow-state" :class="user.role === 'admin' ? 'state-failed' : 'state-planning'">{{ user.role }}</span>
					</div>
					<p>subject {{ user.subject }} · 创建于 {{ clock(user.created_at) }}</p>
				</div>
				<div class="admin-workflow-actions">
					<button class="compact-button" type="button" @click="openReset(user)"><KeyRound :size="13" aria-hidden="true" />重置 TOTP</button>
				</div>
			</article>
		</div>

		<div v-if="rotated" class="dialog-backdrop" @click.self="rotated = undefined">
			<div class="dialog" role="dialog" aria-modal="true" aria-labelledby="admin-rotated-title">
				<button class="icon-button dialog-close" type="button" aria-label="Close" @click="rotated = undefined"><X :size="15" aria-hidden="true" /></button>
				<h2 id="admin-rotated-title">新 TOTP 已生效</h2>
				<p class="dialog-copy">{{ rotated.user.name }} 的旧验证器已失效。新 secret 只在此展示一次,关闭后无法再次查看。</p>
				<dl class="admin-confirm-summary">
					<dt>secret</dt><dd><code class="admin-secret">{{ rotated.secret }}</code></dd>
				</dl>
				<dl class="admin-confirm-summary">
					<dt>otpauth URL</dt><dd><code class="admin-secret">{{ rotated.url }}</code></dd>
				</dl>
				<div class="admin-confirm-actions">
					<button class="compact-button" type="button" @click="rotated = undefined">我已保存</button>
				</div>
			</div>
		</div>

		<div v-if="reset" class="dialog-backdrop" @click.self="closeReset">
			<div class="dialog" role="dialog" aria-modal="true" aria-labelledby="admin-reset-title">
				<button class="icon-button dialog-close" type="button" aria-label="Close" @click="closeReset"><X :size="15" aria-hidden="true" /></button>
				<h2 id="admin-reset-title">重置 {{ reset.user.name }} 的 TOTP</h2>
				<p class="dialog-copy">输入你(操作者)自己的密码确认身份。重置后目标用户的旧验证器立即失效,新 secret 只展示一次。</p>
				<label class="admin-confirm-reason">
					<span>操作者密码</span>
					<input v-model="reset.password" type="password" autocomplete="current-password" />
				</label>
				<p v-if="resetError" class="admin-error">{{ resetError }}</p>
				<div class="admin-confirm-actions">
					<button class="text-button" type="button" :disabled="resetBusy" @click="closeReset">取消</button>
					<button class="compact-button" type="button" :disabled="resetBusy || !reset.password" @click="submitReset">{{ resetBusy ? "重置中..." : "确认重置" }}</button>
				</div>
			</div>
		</div>
	</section>
</template>
