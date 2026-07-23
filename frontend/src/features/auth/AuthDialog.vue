<script setup lang="ts">
import { nextTick, ref, watch } from "vue";
import QRCode from "qrcode";
import { api, setToken } from "../../api/client";
import "../../styles/dialog.css";
import "./auth.css";

const props = defineProps<{
  open: boolean;
  initialMode: "login" | "register";
}>();
const emit = defineEmits<{ close: []; authenticated: [name: string] }>();

const mode = ref<"login" | "register">("login");
const username = ref("");
const password = ref("");
const totp = ref("");
const secret = ref("");
const totpURL = ref("");
const error = ref("");
const busy = ref(false);
const qr = ref<HTMLCanvasElement>();

watch(
  () => props.open,
  async (open) => {
    if (!open) return;
    mode.value = props.initialMode;
    username.value = "";
    password.value = "";
    totp.value = "";
    secret.value = "";
    totpURL.value = "";
    error.value = "";
    await nextTick();
  },
);

watch(totpURL, async (value) => {
  await nextTick();
  if (value && qr.value)
    await QRCode.toCanvas(qr.value, value, { width: 172, margin: 1 });
});

async function register() {
  busy.value = true;
  error.value = "";
  try {
    const result = await api.register(username.value, password.value);
    secret.value = result.totp_secret;
    totpURL.value = result.totp_url;
  } catch (err) {
    error.value = err instanceof Error ? err.message : "Registration failed";
  } finally {
    busy.value = false;
  }
}

async function login() {
  busy.value = true;
  error.value = "";
  try {
    const result = await api.login(username.value, password.value, totp.value);
    setToken(result.token);
    emit("authenticated", result.name);
    emit("close");
  } catch (err) {
    error.value = err instanceof Error ? err.message : "Login failed";
  } finally {
    busy.value = false;
  }
}
</script>

<template>
  <div v-if="open" class="dialog-backdrop" @mousedown.self="emit('close')">
    <section class="dialog auth-dialog" aria-modal="true" role="dialog">
      <button
        class="icon-button dialog-close"
        aria-label="Close"
        @click="emit('close')"
      >
        x
      </button>
      <template v-if="mode === 'login'">
        <p class="eyebrow">Account</p>
        <h2>Sign in</h2>
        <p class="dialog-copy">
          Connect to your existing challenge environments.
        </p>
        <label
          >Username<input v-model="username" autocomplete="username"
        /></label>
        <label
          >Password<input
            v-model="password"
            type="password"
            autocomplete="current-password"
        /></label>
        <label
          >Authenticator code<input
            v-model="totp"
            inputmode="numeric"
            autocomplete="one-time-code"
        /></label>
        <p v-if="error" class="form-error">{{ error }}</p>
        <button class="primary-button" :disabled="busy" @click="login">
          {{ busy ? "Signing in..." : "Sign in" }}
        </button>
        <p class="dialog-foot">
          No account?
          <button
            class="text-button"
            @click="
              mode = 'register';
              error = '';
            "
          >
            Create one
          </button>
        </p>
      </template>
      <template v-else-if="!secret">
        <p class="eyebrow">Account</p>
        <h2>Create account</h2>
        <p class="dialog-copy">
          Create an account, then bind an authenticator before signing in.
        </p>
        <label
          >Username<input v-model="username" autocomplete="username"
        /></label>
        <label
          >Password<input
            v-model="password"
            type="password"
            autocomplete="new-password"
        /></label>
        <p v-if="error" class="form-error">{{ error }}</p>
        <button class="primary-button" :disabled="busy" @click="register">
          {{ busy ? "Creating..." : "Continue" }}
        </button>
        <p class="dialog-foot">
          Already have an account?
          <button
            class="text-button"
            @click="
              mode = 'login';
              error = '';
            "
          >
            Sign in
          </button>
        </p>
      </template>
      <template v-else>
        <p class="eyebrow">Authenticator</p>
        <h2>Bind your code</h2>
        <p class="dialog-copy">
          Scan this code with your authenticator, then sign in with its
          six-digit code.
        </p>
        <div class="totp-setup">
          <canvas ref="qr" width="172" height="172" /><code>{{ secret }}</code>
        </div>
        <button
          class="primary-button"
          @click="
            mode = 'login';
            secret = '';
            totpURL = '';
          "
        >
          Continue to sign in
        </button>
      </template>
    </section>
  </div>
</template>
