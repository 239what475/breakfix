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
  NSelect,
  useMessage,
} from 'naive-ui'
import {
  CheckmarkCircleOutline,
  EllipseOutline,
  FlashOutline,
  ConstructOutline,
  LogInOutline,
  PlayOutline,
  PowerOutline,
  RefreshOutline,
  SearchOutline,
  SparklesOutline,
  TerminalOutline,
} from '@vicons/ionicons5'
import { api, clearToken, isLoggedIn, setToken, token, type Challenge, type ChallengeDraft, type GenerateDraftResponse, type GenerationJobResponse } from './composables/useApi'
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
const showGenerate = ref(false)
const generateStep = ref<'idea' | 'draft' | 'job'>('idea')
const draftReviewing = ref(false)
const jobStarting = ref(false)
const generateTopic = ref('')
const draftVerdict = ref('')
const draftReason = ref('')
const draftWarnings = ref<string[]>([])
const generationJob = ref<GenerationJobResponse | null>(null)
const editableDraft = ref<ChallengeDraft>({
  title: '',
  difficulty: 'medium',
  tags: [],
  description: '',
  goal: '',
  symptoms: '',
  fault_mechanism: '',
  environment_shape: '',
  acceptance_criteria: '',
  difficulty_reason: '',
  notes: '',
})

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
let visibilityHandler: (() => void) | null = null
let blurHandler: (() => void) | null = null
let generationPollId: number | null = null

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

const draftTagInput = ref('')
const difficultyOptions = [
  { label: 'Easy', value: 'easy' },
  { label: 'Medium', value: 'medium' },
  { label: 'Hard', value: 'hard' },
]
const canReviewDraft = computed(() => generateTopic.value.trim().length >= 8)
const canStartGeneration = computed(() => {
  const draft = editableDraft.value
  return Boolean(
    draft.title.trim() &&
    draft.difficulty.trim() &&
    draft.tags.length &&
    draft.description.trim() &&
    draft.goal.trim() &&
    draft.symptoms.trim() &&
    draft.fault_mechanism.trim() &&
    draft.environment_shape.trim() &&
    draft.acceptance_criteria.trim() &&
    draft.difficulty_reason.trim(),
  )
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
  if (selectedChallenge.value) return 'Review the challenge brief on the right, then launch the lab when ready.'
  return 'Choose a challenge from the sidebar to open its terminal workspace.'
})

onMounted(async () => {
  if (loggedIn.value) {
    await restoreSession()
  }

  visibilityHandler = () => {
    if (document.hidden) {
      terminalHasFocus.value = false
      term?.blur()
    }
  }
  blurHandler = () => {
    terminalHasFocus.value = false
    term?.blur()
  }
  document.addEventListener('visibilitychange', visibilityHandler)
  window.addEventListener('blur', blurHandler)
})

onUnmounted(() => {
  document.removeEventListener('visibilitychange', visibilityHandler!)
  window.removeEventListener('blur', blurHandler!)
  clearGenerationPoll()
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

watch(showGenerate, (value) => {
  if (!value) {
    clearGenerationPoll()
  }
})

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

async function restoreSession() {
  try {
    await loadChallenges({ silent: true })
  } catch {
    clearToken()
    loggedIn.value = false
  }
}

async function loadChallenges(options?: { silent?: boolean }) {
  loading.value = true
  try {
    const result = await api.listChallenges()
    challenges.value = result.challenges ?? []
  } catch (error: any) {
    if (!options?.silent) {
      message.error(error.message)
    }
    throw error
  } finally {
    loading.value = false
  }
}

function openGenerateModal() {
  showGenerate.value = true
  generateStep.value = 'idea'
  generateTopic.value = ''
  draftVerdict.value = ''
  draftReason.value = ''
  draftWarnings.value = []
  generationJob.value = null
  draftTagInput.value = ''
  editableDraft.value = {
    title: '',
    difficulty: 'medium',
    tags: [],
    description: '',
    goal: '',
    symptoms: '',
    fault_mechanism: '',
    environment_shape: '',
    acceptance_criteria: '',
    difficulty_reason: '',
    notes: '',
  }
}

function clearGenerationPoll() {
  if (generationPollId !== null) {
    window.clearTimeout(generationPollId)
    generationPollId = null
  }
}

function queueGenerationPoll(jobId: string, delay = 2500) {
  clearGenerationPoll()
  generationPollId = window.setTimeout(() => {
    generationPollId = null
    void pollGenerationJob(jobId)
  }, delay)
}

async function reviewDraft() {
  if (!canReviewDraft.value) return
  draftReviewing.value = true
  try {
    const result = await api.reviewGenerationDraft(generateTopic.value.trim())
    applyDraftReview(result)
    generateStep.value = 'draft'
  } catch (error: any) {
    message.error(error.message)
  } finally {
    draftReviewing.value = false
  }
}

function applyDraftReview(result: GenerateDraftResponse) {
  draftVerdict.value = result.verdict ?? 'good'
  draftReason.value = result.reason ?? ''
  draftWarnings.value = result.warnings ?? []
  if (result.draft) {
    editableDraft.value = {
      ...result.draft,
      tags: [...result.draft.tags],
      notes: result.draft.notes ?? '',
    }
  }
}

function addDraftTag() {
  const nextTag = draftTagInput.value.trim()
  if (!nextTag) return
  if (!editableDraft.value.tags.includes(nextTag)) {
    editableDraft.value.tags = [...editableDraft.value.tags, nextTag]
  }
  draftTagInput.value = ''
}

function removeDraftTag(tag: string) {
  editableDraft.value.tags = editableDraft.value.tags.filter((item) => item !== tag)
}

async function startGenerationJob() {
  if (!canStartGeneration.value) return
  jobStarting.value = true
  try {
    const result = await api.createGenerationJob({
      ...editableDraft.value,
      tags: [...editableDraft.value.tags],
      notes: editableDraft.value.notes?.trim() || '',
    })
    generationJob.value = result
    generateStep.value = 'job'
    if (result.job_id) {
      queueGenerationPoll(result.job_id, 1000)
    }
  } catch (error: any) {
    message.error(error.message)
  } finally {
    jobStarting.value = false
  }
}

async function pollGenerationJob(jobId: string) {
  try {
    const result = await api.getGenerationJob(jobId)
    generationJob.value = result
    if (result.status === 'success') {
      clearGenerationPoll()
      await loadChallenges()
      if (result.challenge_id) {
        selectedId.value = result.challenge_id
      }
      message.success('Challenge generated')
      if (!inChallenge.value) {
        showGenerate.value = false
      }
      return
    }
    if (result.status === 'failed') {
      clearGenerationPoll()
      message.error(result.message || 'Generation failed')
      return
    }
    queueGenerationPoll(jobId)
  } catch (error: any) {
    clearGenerationPoll()
    message.error(error.message)
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
    sendTerminalResize()
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
    reconnecting.value = false
    term?.write('\r\n\x1b[33mDisconnected\x1b[0m\r\n')
  }

  ws.onerror = () => {
    if (sessionNonce !== terminalSessionNonce) return
    terminalReady.value = false
    terminalDisconnected.value = true
    terminalHasFocus.value = false
    reconnecting.value = false
  }

  term.onData((data) => {
    if (sessionNonce !== terminalSessionNonce) return
    if (ws?.readyState === WebSocket.OPEN) {
      ws.send(JSON.stringify({ type: 'data', data }))
    }
  })

  termKeyHandlerDisposable = term.onKey(() => {
    if (ws?.readyState !== WebSocket.OPEN) {
      terminalHasFocus.value = false
    }
  })

  resizeObserver = new ResizeObserver(() => {
    fitAddon?.fit()
    sendTerminalResize()
  })
  resizeObserver.observe(terminalEl.value)
}

function focusTerminal() {
  if (document.hidden || !document.hasFocus()) return
  term?.focus()
  term?.textarea?.focus({ preventScroll: true })
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
    reconnecting.value = false
    message.error(error.message)
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

    </aside>

    <main class="workspace">
      <header class="workspace-header">
        <div>
          <div class="workspace-kicker">Live terminal</div>
          <h1>{{ stageTitle }}</h1>
          <p>{{ stageSubtitle }}</p>
        </div>

        <div class="workspace-header-side">
          <n-button v-if="loggedIn" type="primary" secondary strong @click="openGenerateModal">
            <template #icon>
              <n-icon :component="ConstructOutline" />
            </template>
            Generate Challenge
          </n-button>
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
        </div>
      </header>

      <section class="workspace-stage">
        <div v-if="!inChallenge" class="terminal-empty">
          <div class="terminal-empty-grid" />
          <div class="terminal-empty-card">
            <template v-if="selectedChallenge">
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
            </template>
            <template v-else>
              <div class="terminal-icon-wrap">
                <n-icon :component="TerminalOutline" size="34" />
              </div>
              <h2>Pick a challenge from the left</h2>
              <p>Browse the challenge catalog, inspect the problem statement, then launch a terminal-based lab from the sidebar.</p>
            </template>
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
            <div v-if="!terminalReady || terminalDisconnected" class="terminal-overlay">
              <div class="terminal-overlay-card">
                <template v-if="terminalDisconnected">
                  <div class="terminal-overlay-title">Terminal disconnected</div>
                  <div class="terminal-overlay-copy">The session is still retained. Reconnect when you are ready.</div>
                  <n-button type="primary" @click="reconnectTerminal">Reconnect terminal</n-button>
                </template>
                <template v-else>
                  <div class="terminal-spinner" />
                  <div>Connecting to the challenge environment...</div>
                </template>
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

    <n-modal
      v-model:show="showAuth"
      :mask-closable="false"
      preset="card"
      class="auth-modal"
      style="width: 520px; max-width: min(92vw, 520px)"
    >
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
            <div class="auth-hint">Enter the 6-digit code from the authenticator app you linked during registration.</div>
            <n-button type="primary" block size="large" :loading="authLoading" @click="doLogin">Sign In</n-button>
          </n-space>
          <n-divider />
          <div class="auth-foot">No account? <button class="auth-link" @click="authMode = 'register'">Register</button></div>
        </template>

        <template v-else>
          <div class="auth-head">
            <div class="auth-kicker">Account setup</div>
            <h3>Create Account</h3>
            <p>Create your login first, then bind the TOTP secret in your authenticator app before signing in.</p>
          </div>

          <template v-if="authTotpSecret">
            <div class="totp-panel">
              <div class="totp-steps">
                <div class="totp-step"><span>1</span> Scan the QR code or copy the secret into your authenticator app.</div>
                <div class="totp-step"><span>2</span> Wait for the app to generate a fresh 6-digit TOTP code.</div>
                <div class="totp-step"><span>3</span> Continue to sign in, then enter that code in the login form.</div>
              </div>
              <div class="totp-secret">
                <span class="totp-label">TOTP Secret</span>
                <code>{{ authTotpSecret }}</code>
              </div>
              <div class="totp-qr-wrap">
                <canvas ref="qrCanvas" width="176" height="176" />
              </div>
              <div class="auth-hint">Keep this secret only if you need to re-add the account later. Otherwise the QR scan is enough.</div>
              <n-button type="primary" block size="large" @click="authMode = 'login'">Continue to Sign In</n-button>
            </div>
          </template>
          <template v-else>
            <n-space vertical size="small">
              <n-input v-model:value="authUsername" placeholder="Username" size="large" />
              <n-input v-model:value="authPassword" type="password" placeholder="Password (min 6 chars)" size="large" />
              <div class="auth-hint">Use a password with at least 6 characters. You will add TOTP in the next step.</div>
              <n-button type="primary" block size="large" :loading="authLoading" @click="doRegister">Register</n-button>
            </n-space>
          </template>

          <n-divider />
          <div class="auth-foot">Have an account? <button class="auth-link" @click="authMode = 'login'">Sign In</button></div>
        </template>
      </n-card>
    </n-modal>

    <n-modal
      v-model:show="showGenerate"
      preset="card"
      class="generate-modal"
      style="width: 820px; max-width: min(94vw, 820px)"
      :mask-closable="!draftReviewing && !jobStarting"
    >
      <n-card :bordered="false" size="small" role="dialog" class="generate-card">
        <div class="generate-head">
          <div class="auth-kicker">Challenge authoring</div>
          <h3>Generate Challenge</h3>
          <p>Draft the scenario with the agent first, then launch the full build and verification pipeline.</p>
        </div>

        <template v-if="generateStep === 'idea'">
          <div class="generate-panel">
            <div class="generate-section-title">Step 1. Describe the idea</div>
            <p class="generate-section-copy">Start with a short operator problem, outage, or debugging scenario. The agent will expand it into a structured challenge brief.</p>
            <n-input
              v-model:value="generateTopic"
              type="textarea"
              :autosize="{ minRows: 6, maxRows: 10 }"
              placeholder="Example: Create a challenge where an on-call engineer must diagnose why nginx is returning 502 even though the backend process looks healthy at first glance."
            />
            <div class="generate-actions">
              <n-button quaternary @click="showGenerate = false">Cancel</n-button>
              <n-button type="primary" :disabled="!canReviewDraft" :loading="draftReviewing" @click="reviewDraft">Review with Agent</n-button>
            </div>
          </div>
        </template>

        <template v-else-if="generateStep === 'draft'">
          <div class="generate-panel">
            <div class="generate-review-summary">
              <div>
                <div class="generate-section-title">Step 2. Review the draft</div>
                <p class="generate-section-copy">{{ draftReason }}</p>
              </div>
              <n-tag :bordered="false" :type="draftVerdict === 'good' ? 'success' : draftVerdict === 'weak' ? 'warning' : 'error'">
                {{ draftVerdict }}
              </n-tag>
            </div>

            <div v-if="draftWarnings.length" class="draft-warning-list">
              <div v-for="warning in draftWarnings" :key="warning" class="draft-warning-item">{{ warning }}</div>
            </div>

            <div class="draft-form-grid">
              <div class="draft-field draft-field-full">
                <label>Title</label>
                <n-input v-model:value="editableDraft.title" placeholder="Challenge title" />
              </div>
              <div class="draft-field">
                <label>Difficulty</label>
                <n-select v-model:value="editableDraft.difficulty" :options="difficultyOptions" />
              </div>
              <div class="draft-field">
                <label>Add Tag</label>
                <div class="tag-editor">
                  <n-input v-model:value="draftTagInput" placeholder="linux" @keydown.enter.prevent="addDraftTag" />
                  <n-button quaternary @click="addDraftTag">Add</n-button>
                </div>
              </div>
              <div class="draft-field draft-field-full">
                <div class="draft-tag-list">
                  <button v-for="tag in editableDraft.tags" :key="tag" class="draft-tag-chip" @click="removeDraftTag(tag)">
                    {{ tag }} <span>x</span>
                  </button>
                </div>
              </div>
              <div class="draft-field draft-field-full">
                <label>Description</label>
                <n-input v-model:value="editableDraft.description" data-testid="draft-description" type="textarea" :autosize="{ minRows: 2, maxRows: 5 }" />
              </div>
              <div class="draft-field draft-field-full">
                <label>Goal</label>
                <n-input v-model:value="editableDraft.goal" data-testid="draft-goal" type="textarea" :autosize="{ minRows: 3, maxRows: 6 }" />
              </div>
              <div class="draft-field draft-field-full">
                <label>Symptoms</label>
                <n-input v-model:value="editableDraft.symptoms" data-testid="draft-symptoms" type="textarea" :autosize="{ minRows: 3, maxRows: 6 }" />
              </div>
              <div class="draft-field draft-field-full">
                <label>Fault Mechanism</label>
                <n-input v-model:value="editableDraft.fault_mechanism" data-testid="draft-fault-mechanism" type="textarea" :autosize="{ minRows: 3, maxRows: 6 }" />
              </div>
              <div class="draft-field draft-field-full">
                <label>Environment Shape</label>
                <n-input v-model:value="editableDraft.environment_shape" data-testid="draft-environment-shape" type="textarea" :autosize="{ minRows: 3, maxRows: 6 }" />
              </div>
              <div class="draft-field draft-field-full">
                <label>Acceptance Criteria</label>
                <n-input v-model:value="editableDraft.acceptance_criteria" data-testid="draft-acceptance-criteria" type="textarea" :autosize="{ minRows: 3, maxRows: 6 }" />
              </div>
              <div class="draft-field draft-field-full">
                <label>Difficulty Reason</label>
                <n-input v-model:value="editableDraft.difficulty_reason" data-testid="draft-difficulty-reason" type="textarea" :autosize="{ minRows: 3, maxRows: 6 }" />
              </div>
              <div class="draft-field draft-field-full">
                <label>Notes</label>
                <n-input v-model:value="editableDraft.notes" data-testid="draft-notes" type="textarea" :autosize="{ minRows: 2, maxRows: 5 }" placeholder="Optional notes for the generator pipeline." />
              </div>
            </div>

            <div class="generate-actions">
              <n-button quaternary @click="generateStep = 'idea'">Back</n-button>
              <n-button quaternary :loading="draftReviewing" @click="reviewDraft">Regenerate Draft</n-button>
              <n-button type="primary" :disabled="!canStartGeneration" :loading="jobStarting" @click="startGenerationJob">Generate Challenge</n-button>
            </div>
          </div>
        </template>

        <template v-else>
          <div class="generate-panel">
            <div class="generate-section-title">Step 3. Build and verify</div>
            <p class="generate-section-copy">The backend is now building, testing, and packaging the challenge. This can take several minutes.</p>
            <div class="job-status-card" :data-state="generationJob?.status || 'queued'">
              <div class="job-status-head">
                <div class="job-status-title">{{ generationJob?.status || 'queued' }}</div>
                <n-tag v-if="generationJob?.challenge_id" :bordered="false" type="success">{{ generationJob?.challenge_id }}</n-tag>
              </div>
              <p class="job-status-copy">{{ generationJob?.message || 'Waiting for the generation controller to start the job.' }}</p>
              <div class="job-meta">
                <span v-if="generationJob?.job_id">Job {{ generationJob.job_id }}</span>
                <span v-if="generationJob?.started_at">Started {{ generationJob.started_at }}</span>
                <span v-if="generationJob?.completed_at">Completed {{ generationJob.completed_at }}</span>
              </div>
            </div>
            <div class="generate-actions">
              <n-button quaternary :disabled="generationJob?.status === 'running' || generationJob?.status === 'queued'" @click="generateStep = 'draft'">Back to Draft</n-button>
              <n-button v-if="generationJob?.status === 'success'" type="primary" @click="showGenerate = false">Open Challenge</n-button>
            </div>
          </div>
        </template>
      </n-card>
    </n-modal>
  </div>
</template>
