<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { AlertTriangle, Ban, RefreshCw, X } from "lucide-vue-next";
import { useAdminEnvironments, isStuckEnvironment } from "./admin";
import { clock, relative, shortId } from "./format";
import { toActiveRef, toLoggedInRef } from "./refs";
import type { AdminEnvironment, AdminRunnableActionItem } from "../../api/generated";
import "../../styles/dialog.css";
import "./admin.css";

const props = defineProps<{ active: boolean; loggedIn: boolean; refreshRequest: number }>();
const { environments, reaps, actions, actionSummary, playgroundMaxActive, loading, error, releasing, refresh, release } = useAdminEnvironments(
	toActiveRef(props),
	toLoggedInRef(props),
);

watch(
	() => props.refreshRequest,
	(request, previous) => {
		if (request !== previous && props.active && props.loggedIn) void refresh();
	},
);

// The counting mirrors the server-side capacity gate: live phases only, so
// Draining sessions still hold their slot while the Reaper catches up.
const livePlaygroundPhases = new Set(["Pending", "Provisioning", "Ready", "Draining"]);
const playgroundActive = computed(
	() => environments.value.filter((e) => e.content_kind === "playground" && livePlaygroundPhases.has(e.phase)).length,
);
const phaseCounts = computed(() => {
	const counts = new Map<string, number>();
	for (const environment of environments.value) counts.set(environment.phase, (counts.get(environment.phase) ?? 0) + 1);
	return counts;
});
const occupiedUsers = computed(() => new Set(environments.value.map((e) => e.user).filter(Boolean)).size);
const todayStart = computed(() => new Date().setHours(0, 0, 0, 0));
const createdToday = computed(
	() => environments.value.filter((e) => new Date(e.created_at).getTime() >= todayStart.value).length,
);

const phaseFilter = ref("");
const phases = computed(() => [...phaseCounts.value.keys()].sort());
const filtered = computed(() =>
	phaseFilter.value ? environments.value.filter((e) => e.phase === phaseFilter.value) : environments.value,
);

const phaseLabels: Record<string, string> = { Pending: "Pending", Provisioning: "Provisioning", Ready: "Ready", Draining: "Draining", Failed: "Failed", Released: "Released" };
function phaseLabel(phase: string) {
	return phaseLabels[phase] ?? phase;
}

function kindLabel(environment: AdminEnvironment) {
	if (environment.content_kind) return environment.content_kind;
	return environment.purpose;
}

function expiryLabel(environment: AdminEnvironment) {
	if (!environment.expires_at) return "—";
	const remaining = new Date(environment.expires_at).getTime() - Date.now();
	if (remaining <= 0) return "已到期";
	const minutes = Math.ceil(remaining / 60000);
	return minutes > 1 ? `${minutes} min` : "<1 min";
}

function failureText(environment: AdminEnvironment) {
	if (!environment.failure) return "";
	const where = environment.failure.message || environment.failure.reason;
	return `${environment.failure.component}: ${where}`;
}

// The summary chips follow the queue lifecycle order regardless of how the
// server's count map happens to serialize; unknown states sort last.
const actionStateOrder = ["queued", "running", "completed", "failed"];
const actionStateCounts = computed(() =>
	Object.entries(actionSummary.value.by_state)
		.filter(([, count]) => count > 0)
		.sort((a, b) => {
			const left = actionStateOrder.indexOf(a[0]);
			const right = actionStateOrder.indexOf(b[0]);
			return (left === -1 ? actionStateOrder.length : left) - (right === -1 ? actionStateOrder.length : right) || a[0].localeCompare(b[0]);
		}),
);

function failureTitle(action: AdminRunnableActionItem) {
	return action.failure_class || action.failure_summary ? `${action.failure_class}: ${action.failure_summary}` : undefined;
}

// Live rows usually point into the future, so the label counts down instead
// of borrowing relative(), which only knows how to look back; terminal rows
// render their em dash directly in the template.
function nextRunLabel(action: AdminRunnableActionItem) {
	const seconds = Math.max(0, Math.round((new Date(action.next_run_at).getTime() - Date.now()) / 1000));
	if (seconds < 60) return `${seconds}s 后`;
	if (seconds < 5400) return `${Math.round(seconds / 60)}m 后`;
	return `${Math.round(seconds / 3600)}h 后`;
}

// The release verb is a confirm-guarded nudge onto the existing drain path;
// the audit ledger already records it server-side.
const releaseTarget = ref<AdminEnvironment>();

async function confirmRelease() {
	if (!releaseTarget.value) return;
	const name = releaseTarget.value.name;
	releaseTarget.value = undefined;
	await release(name);
}
</script>

<template>
	<section class="admin-section" aria-labelledby="admin-environments-title">
		<h2 id="admin-environments-title" class="admin-section-title">环境观测</h2>
		<p v-if="error" class="admin-error">{{ error }}</p>
		<p v-if="loading && !environments.length" class="admin-empty">Loading environments...</p>

		<div class="admin-overview">
			<article class="admin-overview-card">
				<p class="admin-overview-label">Playground 占用</p>
				<p class="admin-overview-value">{{ playgroundActive }}<small v-if="playgroundMaxActive !== null"> / {{ playgroundMaxActive }}</small></p>
			</article>
			<article class="admin-overview-card">
				<p class="admin-overview-label">占用用户</p>
				<p class="admin-overview-value">{{ occupiedUsers }}</p>
			</article>
			<article class="admin-overview-card">
				<p class="admin-overview-label">今日创建</p>
				<p class="admin-overview-value">{{ createdToday }}</p>
			</article>
			<article class="admin-overview-card admin-overview-phases">
				<p class="admin-overview-label">按相位</p>
				<p class="admin-overview-value">
					<span v-for="phase in phases" :key="phase" class="admin-phase-chip" :data-phase="phase">{{ phaseLabel(phase) }} {{ phaseCounts.get(phase) }}</span>
				</p>
			</article>
		</div>

		<div class="admin-table-toolbar">
			<label class="admin-filter">
				<span>相位</span>
				<select v-model="phaseFilter">
					<option value="">全部</option>
					<option v-for="phase in phases" :key="phase" :value="phase">{{ phaseLabel(phase) }}</option>
				</select>
			</label>
			<button class="icon-button" type="button" title="Refresh" aria-label="Refresh" @click="refresh()"><RefreshCw :size="15" aria-hidden="true" /></button>
		</div>

		<p v-if="!loading && !filtered.length" class="admin-empty">当前过滤条件下没有环境。</p>
		<div v-else-if="filtered.length" class="admin-table-wrap">
			<table class="admin-table admin-environment-table">
				<thead>
					<tr><th scope="col">环境</th><th scope="col">用户</th><th scope="col">类型</th><th scope="col">相位</th><th scope="col">创建</th><th scope="col">到期</th><th scope="col"><span class="visually-hidden">操作</span></th></tr>
				</thead>
				<tbody>
					<tr v-for="environment in filtered" :key="environment.name" :class="{ 'admin-row-stuck': isStuckEnvironment(environment) }">
						<td>
							<span class="admin-mono">{{ shortId(environment.name) }}</span>
							<span v-if="isStuckEnvironment(environment)" class="admin-stuck-flag"><AlertTriangle :size="13" aria-hidden="true" />卡住 &gt;10m</span>
							<span v-if="failureText(environment)" class="admin-failure-note">{{ failureText(environment) }}</span>
						</td>
						<td><span class="admin-mono">{{ environment.user ? shortId(environment.user) : "—" }}</span></td>
						<td><span class="admin-role-badge" :class="kindLabel(environment)">{{ kindLabel(environment) }}</span></td>
						<td><span class="admin-phase-badge" :data-phase="environment.phase">{{ phaseLabel(environment.phase) }}</span></td>
						<td><span :title="clock(environment.created_at)">{{ relative(environment.created_at) }}</span></td>
						<td>{{ expiryLabel(environment) }}</td>
						<td class="admin-row-actions">
							<button
								class="text-button admin-release-button"
								type="button"
								:disabled="releasing === environment.name || environment.phase === 'Draining' || environment.phase === 'Released'"
								@click="releaseTarget = environment"
							>
								<Ban :size="13" aria-hidden="true" />{{ releasing === environment.name ? "释放中..." : "强制释放" }}
							</button>
						</td>
					</tr>
				</tbody>
			</table>
		</div>

		<h3 class="admin-subsection-title">回收队列</h3>
		<p class="admin-subsection-note">环境拆除的持久队列;失败行指数退避重试(封顶 5 分钟)且永不放弃,`succeeded` 为已完成。资源已不存在的拆除计为成功;长期未成功的行通常需要运维在集群侧修复或删除底层资源,下一轮自动收口。</p>
		<p v-if="!reaps.length" class="admin-empty">队列为空。</p>
		<div v-else class="admin-table-wrap">
			<table class="admin-table admin-reap-table">
				<thead>
					<tr><th scope="col">reap key</th><th scope="col">状态</th><th scope="col">尝试</th><th scope="col">上次错误</th><th scope="col">下次尝试</th><th scope="col">更新</th></tr>
				</thead>
				<tbody>
					<tr v-for="reap in reaps" :key="reap.reap_key" :class="{ 'admin-row-stuck': reap.state !== 'succeeded' && reap.attempt >= 4 }">
						<td><span class="admin-mono">{{ shortId(reap.reap_key) }}</span></td>
						<td><span class="admin-phase-badge" :data-state="reap.state">{{ reap.state }}</span></td>
						<td>{{ reap.attempt }}</td>
						<td><span class="admin-failure-note" :title="reap.last_error">{{ reap.last_error || "—" }}</span></td>
						<td><span :title="clock(reap.next_attempt_at)">{{ relative(reap.next_attempt_at) }}</span></td>
						<td><span :title="clock(reap.updated_at)">{{ relative(reap.updated_at) }}</span></td>
					</tr>
				</tbody>
			</table>
		</div>

		<h3 class="admin-subsection-title">动作队列</h3>
		<p class="admin-subsection-note">内容物化与验证的持久动作队列;`failed` 为显式终态——infra 失败指数退避重试,8 次预算耗尽后记 `attempts-exhausted`,artifact 失败立即失败。重新安装对 infra 失败自动开新周期,artifact 失败缓存 fail-fast(相同内容以相同方式失败);高亮行为服务端 `attempt-high` 旗标(尝试预算过半,≥4/8)。</p>
		<div v-if="actionStateCounts.length" class="admin-action-summary">
			<span v-for="[state, count] in actionStateCounts" :key="state" class="admin-phase-chip" :data-state="state">{{ state }} {{ count }}</span>
		</div>
		<p v-if="!actions.length" class="admin-empty">队列为空。</p>
		<div v-else class="admin-table-wrap">
			<table class="admin-table admin-action-table">
				<thead>
					<tr><th scope="col">动作 key</th><th scope="col">phase</th><th scope="col">状态</th><th scope="col">尝试</th><th scope="col">失败</th><th scope="col">下次运行</th></tr>
				</thead>
				<tbody>
					<tr v-for="action in actions" :key="action.action_key" :class="{ 'admin-row-stuck': action.flag === 'attempt-high' }">
						<td><span class="admin-mono">{{ shortId(action.action_key) }}</span></td>
						<td><span class="admin-mono">{{ action.phase }}</span></td>
						<td><span class="admin-phase-badge" :data-state="action.state">{{ action.state }}</span></td>
						<td>{{ action.attempt }}</td>
						<td><span class="admin-failure-note" :title="failureTitle(action)">{{ action.failure_code || "—" }}</span></td>
						<td>
							<template v-if="action.state === 'completed' || action.state === 'failed'">—</template>
							<span v-else :title="clock(action.next_run_at)">{{ nextRunLabel(action) }}</span>
						</td>
					</tr>
				</tbody>
			</table>
		</div>

		<div v-if="releaseTarget" class="dialog-backdrop" @click.self="releaseTarget = undefined">
			<div class="dialog" role="dialog" aria-modal="true" aria-labelledby="admin-release-title">
				<button class="icon-button dialog-close" type="button" aria-label="Close" @click="releaseTarget = undefined"><X :size="15" aria-hidden="true" /></button>
				<h2 id="admin-release-title">强制释放 {{ releaseTarget.name }}</h2>
				<p class="dialog-copy">请求进入既有 Draining 路径,由控制器与回收器完成清理;该操作会写入审计日志。用户 {{ releaseTarget.user || "未知" }} 的会话将终止。</p>
				<div class="admin-confirm-actions">
					<button class="text-button" type="button" @click="releaseTarget = undefined">取消</button>
					<button class="compact-button danger-button" type="button" @click="confirmRelease">确认释放</button>
				</div>
			</div>
		</div>
	</section>
</template>
