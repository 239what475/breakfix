<script setup lang="ts">
import { computed, h, nextTick, onMounted, onUnmounted, ref, watch } from 'vue'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import QRCode from 'qrcode'
import {
  NAvatar,
  NButton,
  NCard,
  NDivider,
  NDropdown,
  NIcon,
  NInput,
  NModal,
  NSpace,
  NTag,
  useMessage,
} from 'naive-ui'
import {
  CheckmarkCircleOutline,
  EllipseOutline,
  FlashOutline,
  LogInOutline,
  PlayOutline,
  PowerOutline,
  RefreshOutline,
  SearchOutline,
  SparklesOutline,
  TerminalOutline,
} from '@vicons/ionicons5'
import { api, clearToken, isLoggedIn, setToken, token, type Challenge } from './composables/useApi'
import '@xterm/xterm/css/xterm.css'

const message = useMessage()

const loggedIn = ref(isLoggedIn())
const loading = ref(false)
const challengeStarting = ref(false)
const challengeSubmitting = ref(false)
const challengeResetting = ref(false)
const inChallenge = ref(false)
const searchQuery = ref('')
const challenges = ref<Challenge[]>([])
const selectedId = ref<string | null>(null)
const challengeTitle = ref('')
const terminalEl = ref<HTMLDivElement>()
const qrCanvas = ref<HTMLCanvasElement>()
const submitResult = ref<'pass' | 'fail' | null>(null)
const submitOutput = ref('')
const terminalReady = ref(false)
const terminalDisconnected = ref(false)
const terminalHasFocus = ref(false)
const reconnecting = ref(false)

const showAuth = ref(false)
const authMode = ref<'login' | 'register'>('login')
const authLoading = ref(false)
const authUsername = ref('')
const authPassword = ref('')
const authTotp = ref('')
const authTotpSecret = ref('')
const authTotpUrl = ref('')

let term: Terminal | null = null
let ws: WebSocket | null = null
let fitAddon: FitAddon | null = null
let resizeObserver: ResizeObserver | null = null
let textareaFocusHandler: (() => void) | null = null
let textareaBlurHandler: (() => void) | null = null
let termKeyHandlerDisposable: { dispose: () => void } | null = null
let terminalSessionNonce = 0
let reconnectTimeoutId: number | null = null
let visibilityHandler: (() => void) | null = null
let focusHandler: (() => void) | null = null
let onlineHandler: (() => void) | null = null

const diffColors: Record<string, string> = {
  easy: '#51d88a',
  medium: '#f5b942',
  hard: '#ff6b6b',
}

const selectedChallenge = computed(() => challenges.value.find((challenge) => challenge.id === selectedId.value) ?? null)

const filteredChallenges = computed(() => {
  const query = searchQuery.value.trim().toLowerCase()
  if (!query) return challenges.value
  return challenges.value.filter((challenge) => {
    const haystack = [
      challenge.title,
      challenge.difficulty,
      challenge.type,
      challenge.description,
      ...challenge.tags,
    ]
      .join(' ')
      .toLowerCase()
    return haystack.includes(query)
  })
})

const challengeStats = computed(() => {
  const total = challenges.value.length
  const solved = challenges.value.filter((challenge) => challenge.solved).length
  const active = challenges.value.filter((challenge) => challenge.active).length
  return { total, solved, active }
})

const primaryActionLabel = computed(() => {
  if (!loggedIn.value) return 'Sign In to Start'
  if (!selectedChallenge.value) return 'Select a Challenge'
  if (selectedChallenge.value.active) return 'Resume Session'
  return 'Start Challenge'
})

const stageTitle = computed(() => {
  if (inChallenge.value && selectedChallenge.value) {
    return challengeTitle.value || selectedChallenge.value.title
  }
  if (selectedChallenge.value) {
    return selectedChallenge.value.title
  }
  return 'Terminal Workspace'
})

const stageSubtitle = computed(() => {
  if (inChallenge.value) {
    if (reconnecting.value) return 'Connection lost. Reattaching to your tmux session when the browser becomes active.'
    if (terminalDisconnected.value) return 'Session disconnected. Resume to reconnect to the existing tmux session.'
    if (!terminalReady.value) return 'Provisioning your lab environment and attaching the terminal.'
    return 'Interactive tmux session attached to the current challenge environment.'
  }
  if (!loggedIn.value) return 'Authenticate to browse labs, open terminals, and submit fixes.'
  if (selectedChallenge.value) return 'Review the brief on the left, then launch the lab when ready.'
  return 'Choose a challenge from the sidebar to open its terminal workspace.'
})

onMounted(async () => {
  if (loggedIn.value) {
    await loadChallenges()
  }

  visibilityHandler = () => {
    if (!document.hidden) {
      void reconnectTerminal()
    }
  }
  focusHandler = () => {
    void reconnectTerminal()
  }
  onlineHandler = () => {
    void reconnectTerminal()
  }
  document.addEventListener('visibilitychange', visibilityHandler)
  window.addEventListener('focus', focusHandler)
  window.addEventListener('online', onlineHandler)
})

onUnmounted(() => {
  document.removeEventListener('visibilitychange', visibilityHandler!)
  window.removeEventListener('focus', focusHandler!)
  window.removeEventListener('online', onlineHandler!)
  clearReconnectTimer()
  disposeTerminal()
})

watch(authTotpUrl, async (url) => {
  await nextTick()
  if (qrCanvas.value && url) {
    await QRCode.toCanvas(qrCanvas.value, url, { width: 176, margin: 1 })
  }
})

watch(challenges, (value) => {
  if (!value.length) {
    selectedId.value = null
    return
  }
  if (!selectedId.value || !value.some((challenge) => challenge.id === selectedId.value)) {
    selectedId.value = value[0].id
  }
}, { immediate: true })

function difficultyColor(level: string) {
  return diffColors[level] ?? '#8ca3b8'
}

function openAuth(mode: 'login' | 'register') {
  authMode.value = mode
  authUsername.value = ''
  authPassword.value = ''
  authTotp.value = ''
  authTotpSecret.value = ''
  authTotpUrl.value = ''
  authLoading.value = false
  showAuth.value = true
}

async function doRegister() {
  authLoading.value = true
  try {
    const result = await api.register(authUsername.value, authPassword.value)
    authTotpSecret.value = result.totp_secret
    authTotpUrl.value = result.totp_url
    message.success('Account created')
  } catch (error: any) {
    message.error(error.message)
  } finally {
    authLoading.value = false
  }
}

async function doLogin() {
  authLoading.value = true
  try {
    const result = await api.login(authUsername.value, authPassword.value, authTotp.value)
    setToken(result.token)
    loggedIn.value = true
    showAuth.value = false
    message.success(`Welcome, ${result.name}`)
    await loadChallenges()
  } catch (error: any) {
    message.error(error.message)
  } finally {
    authLoading.value = false
  }
}

function doLogout() {
  clearToken()
  loggedIn.value = false
  challenges.value = []
  selectedId.value = null
  exitChallenge()
  message.info('Signed out')
}

async function loadChallenges() {
  loading.value = true
  try {
    const result = await api.listChallenges()
    challenges.value = result.challenges ?? []
  } catch (error: any) {
    message.error(error.message)
  } finally {
    loading.value = false
  }
}

async function handlePrimaryAction() {
  if (!loggedIn.value) {
    openAuth('login')
    return
  }
  if (!selectedId.value) return
  await startChallenge()
}

async function startChallenge() {
  if (!selectedId.value) return
  challengeStarting.value = true
  terminalReady.value = false
  terminalDisconnected.value = false
  reconnecting.value = false
  submitResult.value = null
  submitOutput.value = ''

  try {
    const result = await api.startChallenge(selectedId.value)
    challengeTitle.value = result.challenge_title
    inChallenge.value = true
    await nextTick()
    initTerminal(selectedId.value)
  } catch (error: any) {
    message.error(error.message)
  } finally {
    challengeStarting.value = false
  }
}

function initTerminal(challengeId: string) {
  if (!terminalEl.value) return

  disposeTerminal()
  terminalReady.value = false
  terminalDisconnected.value = false
  reconnecting.value = false
  const sessionNonce = ++terminalSessionNonce

  term = new Terminal({
    cursorBlink: true,
    fontSize: 14,
    fontFamily: "'JetBrains Mono', 'SFMono-Regular', Consolas, monospace",
    letterSpacing: 0.2,
    lineHeight: 1.2,
    convertEol: true,
    theme: {
      background: '#08111d',
      foreground: '#d8e3ef',
      cursor: '#8bd3ff',
      black: '#08111d',
      brightBlack: '#36506d',
      red: '#ff7b72',
      green: '#51d88a',
      yellow: '#f5b942',
      blue: '#7cc6ff',
      magenta: '#d2a8ff',
      cyan: '#7ce2ff',
      white: '#d8e3ef',
    },
  })
  fitAddon = new FitAddon()
  term.loadAddon(fitAddon)
  term.open(terminalEl.value)
  fitAddon.fit()
  if (term.textarea) {
    textareaFocusHandler = () => { terminalHasFocus.value = true }
    textareaBlurHandler = () => { terminalHasFocus.value = false }
    term.textarea.addEventListener('focus', textareaFocusHandler)
    term.textarea.addEventListener('blur', textareaBlurHandler)
  }

  const proto = location.protocol === 'https:' ? 'wss:' : 'ws:'
  const currentToken = token()
  const query = currentToken ? `?token=${encodeURIComponent(currentToken)}` : ''
  ws = new WebSocket(`${proto}//${location.host}/api/challenges/${challengeId}/terminal${query}`)

  ws.onopen = () => {
    if (sessionNonce !== terminalSessionNonce) return
    terminalReady.value = true
    terminalDisconnected.value = false
    reconnecting.value = false
    clearReconnectTimer()
    sendTerminalResize()
    requestAnimationFrame(() => focusTerminal())
  }

  ws.onmessage = (event) => {
    if (sessionNonce !== terminalSessionNonce) return
    try {
      const messageData = JSON.parse(event.data)
      if (messageData.type === 'data' && term) {
        term.write(messageData.data)
      }
    } catch {
      term?.write(event.data)
    }
  }

  ws.onclose = () => {
    if (sessionNonce !== terminalSessionNonce) return
    terminalReady.value = false
    terminalDisconnected.value = true
    terminalHasFocus.value = false
    reconnecting.value = inChallenge.value
    term?.write('\r\n\x1b[33mDisconnected\x1b[0m\r\n')
    scheduleReconnect()
  }

  ws.onerror = () => {
    if (sessionNonce !== terminalSessionNonce) return
    terminalReady.value = false
    terminalDisconnected.value = true
    terminalHasFocus.value = false
    reconnecting.value = inChallenge.value
  }

  term.onData((data) => {
    if (sessionNonce !== terminalSessionNonce) return
    if (ws?.readyState === WebSocket.OPEN) {
      ws.send(JSON.stringify({ type: 'data', data }))
    }
  })

  termKeyHandlerDisposable = term.onKey(() => {
    if (ws?.readyState !== WebSocket.OPEN) {
      focusTerminal()
    }
  })

  resizeObserver = new ResizeObserver(() => {
    fitAddon?.fit()
    sendTerminalResize()
  })
  resizeObserver.observe(terminalEl.value)
  requestAnimationFrame(() => focusTerminal())
}

function focusTerminal() {
  term?.focus()
  term?.textarea?.focus()
}

function sendTerminalResize() {
  if (!term || ws?.readyState !== WebSocket.OPEN) return
  ws.send(JSON.stringify({
    type: 'resize',
    cols: term.cols,
    rows: term.rows,
  }))
}

function disposeTerminal() {
  terminalSessionNonce += 1
  clearReconnectTimer()
  resizeObserver?.disconnect()
  resizeObserver = null
  termKeyHandlerDisposable?.dispose()
  termKeyHandlerDisposable = null
  if (term?.textarea && textareaFocusHandler) {
    term.textarea.removeEventListener('focus', textareaFocusHandler)
  }
  if (term?.textarea && textareaBlurHandler) {
    term.textarea.removeEventListener('blur', textareaBlurHandler)
  }
  textareaFocusHandler = null
  textareaBlurHandler = null
  fitAddon = null
  term?.dispose()
  term = null
  ws?.close()
  ws = null
  terminalHasFocus.value = false
}

function exitChallenge() {
  inChallenge.value = false
  challengeTitle.value = ''
  terminalReady.value = false
  terminalDisconnected.value = false
  reconnecting.value = false
  submitResult.value = null
  disposeTerminal()
}

async function doSubmit() {
  if (!selectedId.value) return
  challengeSubmitting.value = true
  try {
    const result = await api.submitChallenge(selectedId.value)
    submitResult.value = result.passed ? 'pass' : 'fail'
    submitOutput.value = result.output
  } catch (error: any) {
    message.error(error.message)
  } finally {
    challengeSubmitting.value = false
  }
}

async function doReset() {
  if (!selectedId.value) return
  challengeResetting.value = true
  try {
    await api.resetChallenge(selectedId.value)
    submitResult.value = null
    submitOutput.value = ''
    if (inChallenge.value) {
      await startChallenge()
    }
  } catch (error: any) {
    message.error(error.message)
  } finally {
    challengeResetting.value = false
  }
}

function statusTone(challenge: Challenge) {
  if (challenge.active) return 'running'
  if (challenge.solved) return 'solved'
  return 'idle'
}

function clearReconnectTimer() {
  if (reconnectTimeoutId !== null) {
    window.clearTimeout(reconnectTimeoutId)
    reconnectTimeoutId = null
  }
}

function scheduleReconnect() {
  if (!inChallenge.value || !selectedId.value || reconnectTimeoutId !== null) return
  reconnectTimeoutId = window.setTimeout(() => {
    reconnectTimeoutId = null
    void reconnectTerminal()
  }, document.hidden ? 5000 : 1200)
}

async function reconnectTerminal() {
  if (!inChallenge.value || !selectedId.value || ws?.readyState === WebSocket.OPEN || challengeStarting.value) return
  reconnecting.value = true
  terminalDisconnected.value = false

  try {
    const result = await api.startChallenge(selectedId.value)
    challengeTitle.value = result.challenge_title
    await nextTick()
    initTerminal(selectedId.value)
  } catch (error: any) {
    terminalDisconnected.value = true
    reconnecting.value = true
    message.error(error.message)
    scheduleReconnect()
  }
}

</script>

<template>
  <div class="app-shell">
    <aside class="sidebar">
      <div class="brand-panel">
        <div class="brand-mark">
          <span class="brand-orb" />
          <div>
            <div class="brand-title">Breakfix</div>
            <div class="brand-subtitle">SRE terminal labs</div>
          </div>
        </div>
        <div class="brand-badges">
          <span class="metric-pill">Challenges {{ challengeStats.total }}</span>
          <span class="metric-pill">Solved {{ challengeStats.solved }}</span>
          <span class="metric-pill">Active {{ challengeStats.active }}</span>
        </div>
      </div>

      <div class="sidebar-toolbar">
        <label class="search-box">
          <n-icon :component="SearchOutline" size="16" />
          <input v-model="searchQuery" class="search-input" type="text" placeholder="Search by title, tag, or difficulty" />
        </label>

        <div v-if="loggedIn" class="account-row">
          <div class="account-copy">
            <div class="account-label">Workspace</div>
            <div class="account-value">Authenticated</div>
          </div>
          <n-dropdown
            trigger="click"
            :options="[{ label: 'Sign Out', key: 'logout', icon: () => h(NIcon, { component: PowerOutline }) }]"
            @select="doLogout"
          >
            <n-avatar round size="small" class="account-avatar" />
          </n-dropdown>
        </div>
        <div v-else class="guest-actions">
          <n-button quaternary strong size="small" @click="openAuth('login')">Sign In</n-button>
          <n-button type="primary" size="small" @click="openAuth('register')">Register</n-button>
        </div>
      </div>

      <div class="challenge-list">
        <div v-if="loading" class="empty-state compact">Loading challenge catalog...</div>
        <template v-else-if="filteredChallenges.length">
          <button
            v-for="challenge in filteredChallenges"
            :key="challenge.id"
            class="challenge-card"
            :class="{ selected: selectedId === challenge.id }"
            @click="selectedId = challenge.id"
          >
            <div class="challenge-card-top">
              <div class="challenge-card-title">{{ challenge.title }}</div>
              <span class="status-dot" :data-tone="statusTone(challenge)" />
            </div>
            <div class="challenge-card-meta">
              <n-tag :bordered="false" size="small" :color="{ color: difficultyColor(challenge.difficulty), textColor: '#08111d' }">
                {{ challenge.difficulty }}
              </n-tag>
              <n-tag v-if="challenge.active" :bordered="false" size="small" type="warning">running</n-tag>
              <n-tag v-else-if="challenge.solved" :bordered="false" size="small" type="success">solved</n-tag>
              <span class="challenge-type">{{ challenge.type }}</span>
            </div>
            <p class="challenge-card-desc">{{ challenge.description || 'Open the lab to inspect and repair the environment.' }}</p>
            <div class="challenge-tags">
              <span v-for="tag in challenge.tags" :key="tag" class="tag-chip">{{ tag }}</span>
            </div>
          </button>
        </template>
        <div v-else class="empty-state compact">No challenges match the current filter.</div>
      </div>

      <div class="challenge-brief" v-if="selectedChallenge">
        <div class="brief-header">
          <div>
            <div class="brief-kicker">Challenge brief</div>
            <h2>{{ selectedChallenge.title }}</h2>
          </div>
          <n-icon :component="SparklesOutline" size="18" />
        </div>

        <div class="brief-tags">
          <n-tag :bordered="false" size="small" :color="{ color: difficultyColor(selectedChallenge.difficulty), textColor: '#08111d' }">
            {{ selectedChallenge.difficulty }}
          </n-tag>
          <n-tag v-for="tag in selectedChallenge.tags" :key="tag" :bordered="false" size="small" class="brief-tag">
            {{ tag }}
          </n-tag>
        </div>

        <p class="brief-description">
          {{ selectedChallenge.description || 'Connect to the terminal, inspect the environment, apply a fix, and submit for verification.' }}
        </p>

        <div class="brief-actions">
          <n-button
            block
            type="primary"
            size="large"
            :disabled="!selectedId"
            :loading="challengeStarting"
            @click="handlePrimaryAction"
          >
            <template #icon>
              <n-icon :component="loggedIn ? PlayOutline : LogInOutline" />
            </template>
            {{ primaryActionLabel }}
          </n-button>
          <div class="secondary-actions">
            <n-button block quaternary size="large" :disabled="!loggedIn || !selectedId" :loading="challengeSubmitting" @click="doSubmit">
              <template #icon>
                <n-icon :component="CheckmarkCircleOutline" />
              </template>
              Submit
            </n-button>
            <n-button block quaternary size="large" :disabled="!loggedIn || !selectedId" :loading="challengeResetting" @click="doReset">
              <template #icon>
                <n-icon :component="RefreshOutline" />
              </template>
              Reset
            </n-button>
          </div>
        </div>
      </div>
    </aside>

    <main class="workspace">
      <header class="workspace-header">
        <div>
          <div class="workspace-kicker">Live terminal</div>
          <h1>{{ stageTitle }}</h1>
          <p>{{ stageSubtitle }}</p>
        </div>

        <div class="workspace-status">
          <span class="status-badge" :data-state="loggedIn ? 'online' : 'offline'">
            <n-icon :component="EllipseOutline" size="10" />
            {{ loggedIn ? 'Authenticated' : 'Guest mode' }}
          </span>
          <span class="status-badge" :data-state="inChallenge ? 'running' : 'idle'">
            <n-icon :component="FlashOutline" size="14" />
            {{ inChallenge ? 'Terminal attached' : 'Waiting to launch' }}
          </span>
        </div>
      </header>

      <section class="workspace-stage">
        <div v-if="!inChallenge" class="terminal-empty">
          <div class="terminal-empty-grid" />
          <div class="terminal-empty-card">
            <div class="terminal-icon-wrap">
              <n-icon :component="TerminalOutline" size="34" />
            </div>
            <h2>{{ selectedChallenge ? selectedChallenge.title : 'Pick a challenge from the left' }}</h2>
            <p>
              {{
                selectedChallenge
                  ? 'The right side becomes your live shell after launch. Keep the brief visible on the left while you work.'
                  : 'Browse the challenge catalog, inspect the problem statement, then launch a terminal-based lab from the sidebar.'
              }}
            </p>
            <n-button type="primary" size="large" :disabled="!selectedId" :loading="challengeStarting" @click="handlePrimaryAction">
              <template #icon>
                <n-icon :component="loggedIn ? PlayOutline : LogInOutline" />
              </template>
              {{ primaryActionLabel }}
            </n-button>
          </div>
        </div>

        <div v-else class="terminal-frame" @pointerdown="focusTerminal">
          <div class="terminal-topbar">
            <div class="terminal-lights">
              <span />
              <span />
              <span />
            </div>
            <div class="terminal-session">{{ selectedChallenge?.id || 'session' }}</div>
            <div class="terminal-actions">
              <n-button text @click="doReset">Reset</n-button>
              <n-button text @click="doSubmit">Submit</n-button>
            </div>
          </div>
          <div class="terminal-body">
            <div ref="terminalEl" class="terminal-surface" @pointerdown.stop="focusTerminal" />
            <div v-if="!terminalReady && !terminalDisconnected" class="terminal-overlay">
              <div class="terminal-overlay-card">
                <div class="terminal-spinner" />
                <div>Connecting to the challenge environment...</div>
              </div>
            </div>
          </div>
        </div>
      </section>

      <footer v-if="submitResult" class="result-bar" :data-result="submitResult">
        <div class="result-title">
          {{ submitResult === 'pass' ? 'Verification passed' : 'Verification failed' }}
        </div>
        <div class="result-output">{{ submitOutput }}</div>
      </footer>
    </main>

    <n-modal v-model:show="showAuth" :mask-closable="false" style="width: 440px; max-width: 92vw">
      <n-card :bordered="false" size="small" role="dialog" class="auth-card">
        <template v-if="authMode === 'login'">
          <div class="auth-head">
            <div class="auth-kicker">Authentication</div>
            <h3>Sign In</h3>
            <p>Use your password and authenticator code to access the terminal workspace.</p>
          </div>
          <n-space vertical size="small">
            <n-input v-model:value="authUsername" placeholder="Username" size="large" />
            <n-input v-model:value="authPassword" type="password" placeholder="Password" size="large" />
            <n-input v-model:value="authTotp" placeholder="TOTP Code" size="large" />
            <n-button type="primary" block size="large" :loading="authLoading" @click="doLogin">Sign In</n-button>
          </n-space>
          <n-divider />
          <div class="auth-foot">No account? <button class="auth-link" @click="authMode = 'register'">Register</button></div>
        </template>

        <template v-else>
          <div class="auth-head">
            <div class="auth-kicker">Account setup</div>
            <h3>Create Account</h3>
            <p>Create your login first, then bind the TOTP secret in your authenticator app.</p>
          </div>

          <template v-if="authTotpSecret">
            <div class="totp-panel">
              <div class="totp-secret">
                <span class="totp-label">TOTP Secret</span>
                <code>{{ authTotpSecret }}</code>
              </div>
              <div class="totp-qr-wrap">
                <canvas ref="qrCanvas" width="176" height="176" />
              </div>
              <n-button type="primary" block size="large" @click="authMode = 'login'">Continue to Sign In</n-button>
            </div>
          </template>
          <template v-else>
            <n-space vertical size="small">
              <n-input v-model:value="authUsername" placeholder="Username" size="large" />
              <n-input v-model:value="authPassword" type="password" placeholder="Password (min 6 chars)" size="large" />
              <n-button type="primary" block size="large" :loading="authLoading" @click="doRegister">Register</n-button>
            </n-space>
          </template>

          <n-divider />
          <div class="auth-foot">Have an account? <button class="auth-link" @click="authMode = 'login'">Sign In</button></div>
        </template>
      </n-card>
    </n-modal>
  </div>
</template>
