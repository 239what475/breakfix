<script setup lang="ts">
// All logic lives here — inside <n-message-provider> which is in parent
import { ref, computed, onMounted, onUnmounted, nextTick, watch, h } from 'vue'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import QRCode from 'qrcode'
import {
  NLayout, NLayoutHeader, NLayoutSider, NLayoutContent, NLayoutFooter,
  NModal, NInput, NButton, NTag, NSpace, NDivider, NDropdown, NAvatar,
  NIcon, NCard, useMessage,
} from 'naive-ui'
import { LogInOutline, PowerOutline, PlayOutline, CheckmarkCircleOutline, RefreshOutline, ArrowBackOutline, TerminalOutline } from '@vicons/ionicons5'
import { api, setToken, clearToken, isLoggedIn, token, type Challenge } from './composables/useApi'
import '@xterm/xterm/css/xterm.css'

const message = useMessage()

// ── Auth ──
const loggedIn = ref(isLoggedIn())
const showAuth = ref(false)
const authMode = ref<'login' | 'register'>('login')
const authLoading = ref(false)
const authUsername = ref('')
const authPassword = ref('')
const authTotp = ref('')
const authTotpSecret = ref('')
const authTotpUrl = ref('')
const qrCanvas = ref<HTMLCanvasElement>()

// ── Challenges ──
const challenges = ref<Challenge[]>([])
const selectedId = ref<string | null>(null)
const loading = ref(false)

// ── Terminal ──
const inChallenge = ref(false)
const challengeTitle = ref('')
const terminalEl = ref<HTMLDivElement>()
const submitResult = ref<'pass' | 'fail' | null>(null)
const submitOutput = ref('')
let term: Terminal | null = null
let ws: WebSocket | null = null
let fitAddon: FitAddon | null = null

const selectedChallenge = computed(() => challenges.value.find(c => c.id === selectedId.value))
const sidebarMode = computed(() => inChallenge.value && selectedChallenge.value ? 'description' : 'list')

onMounted(async () => { if (loggedIn.value) await loadChallenges() })
onUnmounted(() => { term?.dispose(); ws?.close() })

watch(authTotpUrl, async (url) => {
  await nextTick()
  if (qrCanvas.value && url) await QRCode.toCanvas(qrCanvas.value, url, { width: 160 })
})

function openAuth(mode: 'login' | 'register') {
  authMode.value = mode
  authUsername.value = ''; authPassword.value = ''; authTotp.value = ''
  authTotpSecret.value = ''; authTotpUrl.value = ''; authLoading.value = false
  showAuth.value = true
}
async function doRegister() {
  authLoading.value = true
  try {
    const res = await api.register(authUsername.value, authPassword.value)
    authTotpSecret.value = res.totp_secret; authTotpUrl.value = res.totp_url
    message.success('Account created')
  } catch (e: any) { message.error(e.message) }
  finally { authLoading.value = false }
}
async function doLogin() {
  authLoading.value = true
  try {
    const res = await api.login(authUsername.value, authPassword.value, authTotp.value)
    setToken(res.token); loggedIn.value = true; showAuth.value = false
    message.success(`Welcome, ${res.name}`)
    await loadChallenges()
  } catch (e: any) { message.error(e.message) }
  finally { authLoading.value = false }
}
function doLogout() {
  clearToken(); loggedIn.value = false; selectedId.value = null; exitChallenge(); message.info('Signed out')
}
async function loadChallenges() {
  loading.value = true
  try { challenges.value = (await api.listChallenges()).challenges || [] } catch { /* */ }
  finally { loading.value = false }
}
async function startChallenge() {
  if (!selectedId.value) return
  try {
    const res = await api.startChallenge(selectedId.value)
    challengeTitle.value = res.challenge_title; inChallenge.value = true; submitResult.value = null
    await nextTick(); initTerminal(selectedId.value)
  } catch (e: any) { message.error(e.message) }
}
function initTerminal(challengeId: string) {
  if (!terminalEl.value) return
  term?.dispose(); ws?.close()
  term = new Terminal({ cursorBlink: true, fontSize: 14, fontFamily: "'JetBrains Mono','Fira Code',monospace", theme: { background: '#0d1117', foreground: '#e6edf3', cursor: '#58a6ff' } })
  fitAddon = new FitAddon(); term.loadAddon(fitAddon); term.open(terminalEl.value); fitAddon.fit()
  const proto = location.protocol === 'https:' ? 'wss:' : 'ws:'
  const tok = token()
  ws = new WebSocket(`${proto}//${location.host}/api/challenges/${challengeId}/terminal${tok ? '?token=' + encodeURIComponent(tok) : ''}`)
  ws.onmessage = (e) => { try { const m = JSON.parse(e.data); if (m.type === 'data' && term) term.write(new Uint8Array(m.data)) } catch { if (term) term.write(e.data) } }
  ws.onclose = () => term?.write('\r\n\x1b[33mDisconnected\x1b[0m\r\n')
  term.onData((d) => { if (ws?.readyState === WebSocket.OPEN) ws.send(JSON.stringify({ type: 'data', data: Array.from(new TextEncoder().encode(d)) })) })
  new ResizeObserver(() => fitAddon?.fit()).observe(terminalEl.value)
}
function exitChallenge() { inChallenge.value = false; challengeTitle.value = ''; submitResult.value = null; term?.dispose(); term = null; ws?.close(); ws = null }
async function doSubmit() {
  if (!selectedId.value) return
  try { const res = await api.submitChallenge(selectedId.value); submitResult.value = res.passed ? 'pass' : 'fail'; submitOutput.value = res.output } catch (e: any) { message.error(e.message) }
}
async function doReset() {
  if (!selectedId.value) return
  try { await api.resetChallenge(selectedId.value); submitResult.value = null; if (inChallenge.value) { exitChallenge(); await startChallenge() } } catch (e: any) { message.error(e.message) }
}
const diffColors: Record<string, string> = { easy: '#3fb950', medium: '#d29922', hard: '#f85149' }
</script>

<template>
  <n-layout style="height:100vh">
    <!-- Header -->
    <n-layout-header bordered style="height:40px;display:flex;align-items:center;justify-content:space-between;padding:0 16px">
      <div style="display:flex;align-items:center;gap:10px">
        <span style="font-weight:600;font-size:14px;color:#e6edf3">● Breakfix</span>
        <span style="font-size:11px;color:#484f58">SRE Practice</span>
      </div>
      <n-space v-if="loggedIn" size="small">
        <n-dropdown trigger="click" :options="[{label:'Sign Out',key:'logout', icon:()=>h(NIcon,{component:PowerOutline})}]" @select="doLogout">
          <n-avatar round size="small" style="background:#30363d;cursor:pointer" />
        </n-dropdown>
      </n-space>
      <n-space v-else size="small">
        <n-button quaternary size="small" @click="openAuth('login')">Sign In</n-button>
        <n-button size="small" @click="openAuth('register')">Register</n-button>
      </n-space>
    </n-layout-header>

    <n-layout has-sider style="flex:1;overflow:hidden">
      <!-- Sidebar -->
      <n-layout-sider bordered width="260" style="background:#0d1117">
        <!-- List mode -->
        <template v-if="sidebarMode === 'list'">
          <div style="padding:12px;border-bottom:1px solid #30363d">
            <input placeholder="Search..." style="width:100%;background:#0d1117;border:1px solid #30363d;border-radius:6px;height:32px;padding:0 10px;color:#e6edf3;font-size:13px;outline:none" />
          </div>
          <div v-if="loading" style="padding:20px;text-align:center;color:#8b949e;font-size:13px">Loading...</div>
          <div v-else-if="challenges.length === 0" style="padding:24px 16px;text-align:center;color:#8b949e;font-size:12px">No challenges yet</div>
          <div v-for="ch in challenges" :key="ch.id"
            :style="{padding:'10px 14px',borderBottom:'1px solid #30363d40',cursor:'pointer',transition:'background 0.15s',background:selectedId===ch.id?'#1c2128':'transparent',borderLeft:selectedId===ch.id?'2px solid #58a6ff':'2px solid transparent'}"
            @click="selectedId = ch.id">
            <div style="display:flex;align-items:center;gap:6px;margin-bottom:4px">
              <n-tag :bordered="false" size="tiny" :color="{color:diffColors[ch.difficulty]||'#8b949e'}" style="font-size:10px">{{ ch.difficulty }}</n-tag>
              <n-tag v-if="ch.solved" :bordered="false" size="tiny" type="success" style="font-size:10px">solved</n-tag>
              <n-tag v-if="ch.active" :bordered="false" size="tiny" type="warning" style="font-size:10px">running</n-tag>
            </div>
            <div style="font-size:13px;color:#e6edf3;margin-bottom:4px">{{ ch.title }}</div>
            <div style="display:flex;gap:4px;flex-wrap:wrap">
              <n-tag v-for="t in ch.tags" :key="t" :bordered="false" size="tiny" style="background:#30363d;color:#8b949e;font-size:10px">{{ t }}</n-tag>
            </div>
          </div>
        </template>
        <!-- Description mode -->
        <template v-if="sidebarMode === 'description' && selectedChallenge">
          <div style="padding:14px;border-bottom:1px solid #30363d;display:flex;align-items:center;gap:8px">
            <n-button quaternary size="small" @click="exitChallenge"><template #icon><n-icon :component="ArrowBackOutline" /></template></n-button>
            <span style="font-weight:600;font-size:14px;color:#e6edf3">{{ challengeTitle }}</span>
          </div>
          <div style="padding:14px;display:flex;flex-direction:column;gap:12px">
            <n-space size="small">
              <n-tag :bordered="false" size="small" :color="{color:diffColors[selectedChallenge.difficulty]||'#8b949e'}">{{ selectedChallenge.difficulty }}</n-tag>
              <n-tag v-for="t in selectedChallenge.tags" :key="t" :bordered="false" size="small" style="background:#30363d;color:#8b949e">{{ t }}</n-tag>
            </n-space>
            <n-divider style="margin:4px 0" />
            <div style="font-size:12px;color:#8b949e;line-height:1.6;white-space:pre-wrap">{{ (selectedChallenge as any).description || 'Connect to the terminal and solve the challenge. Submit when done.' }}</div>
            <n-divider style="margin:4px 0" />
            <n-space vertical size="small">
              <n-button type="primary" block @click="doSubmit"><template #icon><n-icon :component="CheckmarkCircleOutline" /></template>Submit</n-button>
              <n-button quaternary block @click="doReset"><template #icon><n-icon :component="RefreshOutline" /></template>Reset</n-button>
            </n-space>
          </div>
        </template>
      </n-layout-sider>

      <!-- Main content -->
      <n-layout-content>
        <div v-if="!inChallenge" style="height:100%;display:flex;flex-direction:column;align-items:center;justify-content:center;gap:16px;padding:32px">
          <n-icon :component="TerminalOutline" size="48" color="#30363d" />
          <div style="font-size:15px;color:#8b949e">Select a challenge to begin</div>
          <div style="font-size:12px;color:#484f58;text-align:center;max-width:360px;line-height:1.6">Browse the challenge list and click Start Challenge to open a terminal session.</div>
          <n-button v-if="selectedId && loggedIn" type="primary" @click="startChallenge"><template #icon><n-icon :component="PlayOutline" /></template>Start Challenge</n-button>
          <n-button v-else-if="!loggedIn" @click="openAuth('login')"><template #icon><n-icon :component="LogInOutline" /></template>Sign In to Start</n-button>
        </div>
        <div v-else ref="terminalEl" style="height:100%;overflow:hidden" />
      </n-layout-content>
    </n-layout>

    <!-- Result bar -->
    <n-layout-footer v-if="submitResult" bordered :style="{background:submitResult==='pass'?'#3fb95015':'#f8514915',borderColor:submitResult==='pass'?'#3fb95040':'#f8514940'}">
      <div style="display:flex;align-items:center;justify-content:space-between;padding:6px 14px">
        <span :style="{color:submitResult==='pass'?'#3fb950':'#f85149',fontSize:'13px',fontWeight:500}">{{ submitResult === 'pass' ? '✓ PASSED' : '✗ FAILED' }}</span>
        <span style="font-size:11px;color:#8b949e">{{ submitOutput }}</span>
      </div>
    </n-layout-footer>
  </n-layout>

  <!-- Auth Modal -->
  <n-modal v-model:show="showAuth" :mask-closable="false" style="width:400px;max-width:90vw">
    <n-card :bordered="false" size="small" role="dialog">
      <template v-if="authMode==='login'">
        <h3 style="font-size:16px;font-weight:600;color:#e6edf3;margin:0 0 16px">Sign In</h3>
        <n-space vertical size="small">
          <n-input v-model:value="authUsername" placeholder="Username" size="medium" />
          <n-input v-model:value="authPassword" type="password" placeholder="Password" size="medium" />
          <n-input v-model:value="authTotp" placeholder="TOTP Code" size="medium" />
          <n-button type="primary" block @click="doLogin" :loading="authLoading">Sign In</n-button>
        </n-space>
        <n-divider />
        <div style="text-align:center;font-size:12px;color:#8b949e">No account? <n-button text size="small" @click="authMode='register'">Register</n-button></div>
      </template>
      <template v-if="authMode==='register'">
        <h3 style="font-size:16px;font-weight:600;color:#e6edf3;margin:0 0 16px">Create Account</h3>
        <template v-if="authTotpSecret">
          <n-space vertical size="small">
            <div style="background:#0d1117;border:1px solid #30363d;border-radius:6px;padding:10px">
              <div style="font-size:10px;color:#484f58;text-transform:uppercase;margin-bottom:4px">TOTP Secret</div>
              <div style="font-family:monospace;font-size:12px;word-break:break-all;color:#e6edf3">{{ authTotpSecret }}</div>
            </div>
            <div style="display:flex;justify-content:center"><canvas ref="qrCanvas" width="160" height="160" style="background:white;padding:6px;border-radius:4px" /></div>
            <n-button type="primary" block @click="authMode='login'">Continue to Sign In</n-button>
          </n-space>
        </template>
        <n-space v-else vertical size="small">
          <n-input v-model:value="authUsername" placeholder="Username" size="medium" />
          <n-input v-model:value="authPassword" type="password" placeholder="Password (min 6 chars)" size="medium" />
          <n-button type="primary" block @click="doRegister" :loading="authLoading">Register</n-button>
        </n-space>
        <n-divider />
        <div style="text-align:center;font-size:12px;color:#8b949e">Have an account? <n-button text size="small" @click="authMode='login'">Sign In</n-button></div>
      </template>
    </n-card>
  </n-modal>
</template>
