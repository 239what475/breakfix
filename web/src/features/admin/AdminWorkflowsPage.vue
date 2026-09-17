<script setup lang="ts">
import { computed, ref } from "vue";
import { AlertTriangle, RefreshCw, X } from "lucide-vue-next";
import { queueFlagCounts, useAdminWorkflows } from "./admin";
import { toActiveRef } from "./refs";
import type { AdminDocumentationWorkflow } from "../../api/generated";
import "../../styles/dialog.css";
import "./admin.css";

const props = defineProps<{ active: boolean }>();
const { workflows, detail, queue, loading, error, busy, refresh, openDetail, runAction } = useAdminWorkflows(toActiveRef(props));

const flagCounts = computed(() => (queue.value ? queueFlagCounts(queue.value.items) : {}));

const confirm = ref<{
	kind: "force-fail" | "restart";
	workflow: AdminDocumentationWorkflow;
	reason: string;
}>();
const confirmError = ref("");

const confirmSummary = computed(() => {
	if (!confirm.value) return [];
	const from = confirm.value.workflow.state;
	const to = confirm.value.kind === "force-fail" ? "Failed" : "Planning";
	return [
		["操作", confirm.value.kind === "force-fail" ? "强制失败 (force-fail)" : "重启 (restart)"],
		["工作流", confirm.value.workflow.id],
		["状态变更", `${from} → ${to}`],
		["ledger 条目", `kind=${confirm.value.kind === "force-fail" ? "admin.force_fail" : "admin.restart"}, owner_role=admin`],
		["人审计", `${confirm.value.kind === "force-fail" ? "documentation.workflow.force_fail" : "documentation.workflow.restart"}(操作者、reason、from_state、to_state)`],
	] as const;
});

function openConfirm(kind: "force-fail" | "restart", workflow: AdminDocumentationWorkflow) {
	confirm.value = { kind, workflow, reason: "" };
	confirmError.value = "";
}

function closeConfirm() {
	confirm.value = undefined;
	confirmError.value = "";
}

async function submitConfirm() {
	if (!confirm.value) return;
	const reason = confirm.value.reason.trim();
	if (!reason || reason.length > 500) {
		confirmError.value = "Reason 必填且不超过 500 字。";
		return;
	}
	const done = await runAction(confirm.value.kind, confirm.value.workflow.id, reason);
	if (done) closeConfirm();
}

const dwell = (seconds: number) => {
	if (seconds < 90) return `${seconds}s`;
	if (seconds < 5400) return `${Math.round(seconds / 60)}m`;
	return `${Math.round(seconds / 3600)}h`;
};

const clock = (value: string) => new Date(value).toLocaleString();

const workflowStateClass = (state: string) => `workflow-state state-${state.toLowerCase()}`;
</script>

<template>
	<section class="admin-section" aria-labelledby="admin-workflows-title">
		<div class="space-section-heading">
			<div><p class="eyebrow">Documentation control plane</p><h2 id="admin-workflows-title">Documentation workflows</h2></div>
			<button class="icon-button" type="button" title="Refresh" aria-label="Refresh" @click="refresh()"><RefreshCw :size="15" aria-hidden="true" /></button>
		</div>
		<p v-if="error" class="admin-error">{{ error }}</p>

		<article v-if="queue" class="admin-queue-card">
			<h3>队列积压摘要</h3>
			<div class="admin-queue-grid">
				<div class="admin-queue-cell"><span class="admin-queue-number">{{ queue.summary.by_state["queued"] ?? 0 }}</span><span>queued</span></div>
				<div class="admin-queue-cell"><span class="admin-queue-number">{{ queue.summary.by_state["running"] ?? 0 }}</span><span>running</span></div>
				<div class="admin-queue-cell"><span class="admin-queue-number">{{ queue.summary.by_state["completed"] ?? 0 }}</span><span>completed</span></div>
				<div class="admin-queue-cell"><span class="admin-queue-number">{{ queue.summary.by_state["failed"] ?? 0 }}</span><span>failed</span></div>
				<div v-for="(count, flag) in flagCounts" :key="flag" class="admin-queue-cell flagged">
					<span class="admin-queue-number">{{ count }}</span><span class="admin-flag" :class="{ high: flag === 'attempt-high' }">{{ flag }}</span>
				</div>
			</div>
			<p v-if="queue.items.length" class="admin-queue-items">
				<span v-for="item in queue.items" :key="item.action_key" class="admin-queue-item">
					<code>{{ item.phase }}</code> {{ item.state }} · attempt {{ item.attempt }}<template v-if="item.flag"> · <strong>{{ item.flag }}</strong></template>
				</span>
			</p>
		</article>

		<p v-if="loading" class="space-empty">Loading workflows...</p>
		<p v-else-if="!workflows.length" class="space-empty">当前没有 documentation workflow。点火后在此观察。</p>
		<div v-else class="admin-workflow-list">
			<article v-for="workflow in workflows" :key="workflow.id" class="admin-workflow-row">
				<div class="admin-workflow-main">
					<div class="admin-workflow-title">
						<h3>{{ workflow.id }}</h3>
						<span class="workflow-state" :class="workflowStateClass(workflow.state)">{{ workflow.state }}</span>
						<span v-if="workflow.stuck.flag" class="admin-stuck-badge"><AlertTriangle :size="12" aria-hidden="true" />{{ workflow.stuck.reason }}</span>
					</div>
					<p>revision {{ workflow.revision }} · state_version {{ workflow.state_version }} · dwell {{ dwell(workflow.dwell_seconds) }} · {{ clock(workflow.updated_at) }}</p>
				</div>
				<div class="admin-workflow-actions">
					<button class="text-button" type="button" @click="openDetail(workflow.id)">详情</button>
					<button class="compact-button" type="button" @click="openConfirm('force-fail', workflow)">Force-fail</button>
					<button class="compact-button" type="button" @click="openConfirm('restart', workflow)">Restart</button>
				</div>
			</article>
		</div>

		<article v-if="detail" class="admin-workflow-detail">
			<div class="admin-detail-head">
				<div>
					<h3>{{ detail.id }}</h3>
					<p>
						<span class="workflow-state" :class="workflowStateClass(detail.state)">{{ detail.state }}</span>
						revision {{ detail.revision }} · state_version {{ detail.state_version }} · dwell {{ dwell(detail.dwell_seconds) }}
						<template v-if="detail.stuck.flag"> · <span class="admin-stuck-badge"><AlertTriangle :size="12" aria-hidden="true" />{{ detail.stuck.reason }}</span></template>
					</p>
				</div>
				<button class="icon-button" type="button" title="Close detail" aria-label="Close detail" @click="detail = undefined"><X :size="15" aria-hidden="true" /></button>
			</div>

			<h4>Ledger 时间线</h4>
			<ol v-if="detail.ledger.length" class="admin-ledger">
				<li v-for="entry in detail.ledger" :key="entry.id">
					<div><code>{{ entry.kind }}</code> <span class="admin-ledger-id">{{ entry.id }}</span></div>
					<p>owner_role={{ entry.owner_role }}<template v-if="entry.policy_version"> · policy={{ entry.policy_version }}</template> · {{ clock(entry.created_at) }}</p>
				</li>
			</ol>
			<p v-else class="space-empty">ledger 为空。</p>

			<h4>AgentRun 审计</h4>
			<ul v-if="detail.agent_audits.length" class="admin-audits">
				<li v-for="auditRow in detail.agent_audits" :key="auditRow.run_id">
					<div><code>{{ auditRow.role }}</code> <span class="admin-ledger-id">{{ auditRow.run_id }}</span></div>
					<p>model={{ auditRow.model }} · prompt={{ auditRow.prompt_version }} · tool={{ auditRow.tool_version }} · policy={{ auditRow.policy_version }} · {{ clock(auditRow.created_at) }}</p>
				</li>
			</ul>
			<p v-else class="space-empty">尚无 AgentRun 审计。</p>

			<h4>发布清单</h4>
			<p v-if="detail.publication" class="admin-publication">
				<code>{{ detail.publication.id }}</code> · digest {{ detail.publication.manifest_digest }} · {{ clock(detail.publication.created_at) }}
			</p>
			<p v-else class="space-empty">尚未发布。</p>
		</article>

		<div v-if="confirm" class="dialog-backdrop" @click.self="closeConfirm">
			<div class="dialog" role="dialog" aria-modal="true" aria-labelledby="admin-confirm-title">
				<button class="icon-button dialog-close" type="button" aria-label="Close" @click="closeConfirm"><X :size="15" aria-hidden="true" /></button>
				<h2 id="admin-confirm-title">{{ confirm.kind === "force-fail" ? "强制失败确认" : "重启确认" }}</h2>
				<p class="dialog-copy">该操作将写入以下审计记录:</p>
				<dl class="admin-confirm-summary">
					<template v-for="[label, value] in confirmSummary" :key="label">
						<dt>{{ label }}</dt>
						<dd><code>{{ value }}</code></dd>
					</template>
				</dl>
				<label class="admin-confirm-reason">
					<span>Reason(必填,≤500 字,写入 ledger 与人审计)</span>
					<textarea v-model="confirm.reason" rows="3" maxlength="500" placeholder="说明解卡/重启原因"></textarea>
				</label>
				<p v-if="confirmError" class="admin-error">{{ confirmError }}</p>
				<div class="admin-confirm-actions">
					<button class="text-button" type="button" :disabled="busy" @click="closeConfirm">取消</button>
					<button class="compact-button" type="button" :disabled="busy || !confirm.reason.trim()" @click="submitConfirm">{{ busy ? "执行中..." : "确认执行" }}</button>
				</div>
			</div>
		</div>
	</section>
</template>
