<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from "vue";
import {
	AlertTriangle,
	Bot,
	Boxes,
	CircleDot,
	Compass,
	FileText,
	FlaskConical,
	MoreHorizontal,
	Package,
	Rocket,
	RotateCcw,
	Shield,
	ShieldCheck,
	X,
	Zap,
	type LucideIcon,
} from "lucide-vue-next";
import WorkflowStepper from "./WorkflowStepper.vue";
import { clock, dwell, relative, shortId } from "./format";
import type {
	AdminDocumentationAgentAudit,
	AdminDocumentationLedgerEntry,
	AdminDocumentationWorkflow,
	AdminDocumentationWorkflowDetail,
} from "../../api/generated";
import "../../styles/dialog.css";
import "./admin.css";

const props = defineProps<{
	workflows: AdminDocumentationWorkflow[];
	detail?: AdminDocumentationWorkflowDetail;
	loading: boolean;
	busy: boolean;
	expandRequest?: { id: string; nonce: number };
	openDetail: (id: string) => void;
	closeDetail: () => void;
	runAction: (kind: "force-fail" | "restart", id: string, reason: string) => Promise<boolean>;
}>();

// The stepper ladder covers the nine walking states; terminal outcomes that
// never walked it are expressed by badge + meta only.
const STAGE_STATES = new Set([
	"Planning",
	"PlanReviewing",
	"Generating",
	"ArtifactReviewing",
	"MaterializingArtifact",
	"Verifying",
	"VerificationReviewing",
	"Publishing",
	"Published",
]);

const LEDGER_ICONS: Record<string, { icon: LucideIcon; tone?: "red" | "amber" }> = {
	"document-context": { icon: FileText },
	"learning-unit-plan": { icon: Compass },
	"plan-gate": { icon: Shield },
	"practice-candidate": { icon: Package },
	"artifact-gate": { icon: ShieldCheck },
	"runnable-revision": { icon: Boxes },
	"verification-report": { icon: FlaskConical },
	"verification-review": { icon: FlaskConical },
	"admin.force_fail": { icon: Zap, tone: "red" },
	"admin.restart": { icon: RotateCcw, tone: "amber" },
	publication: { icon: Rocket },
};

const ledgerIcon = (kind: string) => LEDGER_ICONS[kind] ?? { icon: CircleDot };

const expandedId = ref<string>();
const openMenuId = ref<string>();

const confirm = ref<{
	kind: "force-fail" | "restart";
	workflow: AdminDocumentationWorkflow;
	reason: string;
}>();
const confirmError = ref("");

const detailFor = (id: string) => (props.detail?.id === id ? props.detail : undefined);
const canForceFail = (workflow: AdminDocumentationWorkflow) => !["Failed", "Rejected", "Published", "NoPractice"].includes(workflow.state);
const canRestart = (workflow: AdminDocumentationWorkflow) => workflow.state === "Failed" || workflow.state === "Rejected";
const hasMenu = (workflow: AdminDocumentationWorkflow) => canForceFail(workflow) || canRestart(workflow);

function toggleRow(id: string) {
	if (expandedId.value === id) {
		expandedId.value = undefined;
		props.closeDetail();
		return;
	}
	expandedId.value = id;
	props.openDetail(id);
}

function menuAction(kind: "force-fail" | "restart", workflow: AdminDocumentationWorkflow) {
	openMenuId.value = undefined;
	openConfirm(kind, workflow);
}

function onPointerDown(event: Event) {
	if (!openMenuId.value) return;
	if (!(event.target instanceof Element) || !event.target.closest(".admin-row-menu")) openMenuId.value = undefined;
}

function onKeydown(event: KeyboardEvent) {
	if (event.key === "Escape" && openMenuId.value) openMenuId.value = undefined;
}

onMounted(() => {
	document.addEventListener("pointerdown", onPointerDown);
	document.addEventListener("keydown", onKeydown);
});
onUnmounted(() => {
	document.removeEventListener("pointerdown", onPointerDown);
	document.removeEventListener("keydown", onKeydown);
});

// The stuck banner (or a later request) can target a row from outside the
// list; each request carries a nonce so repeating the same id still expands.
watch(
	() => props.expandRequest,
	(request) => {
		if (!request) return;
		expandedId.value = request.id;
		props.openDetail(request.id);
		document.querySelector(`[data-workflow-id="${request.id}"]`)?.scrollIntoView({ block: "center", behavior: "smooth" });
	},
);

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
	const done = await props.runAction(confirm.value.kind, confirm.value.workflow.id, reason);
	if (done) closeConfirm();
}

const ledgerMeta = (entry: AdminDocumentationLedgerEntry) =>
	`owner_role=${entry.owner_role}${entry.policy_version ? ` · policy=${entry.policy_version}` : ""} · ${shortId(entry.digest)}`;

const auditMeta = (audit: AdminDocumentationAgentAudit) =>
	`model=${audit.model} · prompt=${audit.prompt_version} · tool=${audit.tool_version} · policy=${audit.policy_version}`;

const workflowStateClass = (state: string) => `workflow-state state-${state.toLowerCase()}`;
</script>

<template>
	<section class="admin-section" aria-labelledby="admin-workflows-title">
		<h2 id="admin-workflows-title" class="admin-section-title">Documentation workflows</h2>

		<p v-if="loading" class="admin-empty">Loading workflows...</p>
		<p v-else-if="!workflows.length" class="admin-empty">当前没有 documentation workflow。点火后在此观察。</p>
		<div v-else class="admin-workflow-list">
			<article
				v-for="workflow in workflows"
				:key="workflow.id"
				class="admin-workflow-row"
				:class="{ expanded: expandedId === workflow.id }"
				:data-workflow-id="workflow.id"
			>
				<div class="admin-workflow-summary">
					<button class="admin-workflow-toggle" type="button" :aria-expanded="expandedId === workflow.id" @click="toggleRow(workflow.id)">
						<div class="admin-workflow-main">
							<div class="admin-workflow-title">
								<h3>{{ workflow.id }}</h3>
								<span class="workflow-state" :class="workflowStateClass(workflow.state)">{{ workflow.state }}</span>
								<span v-if="workflow.stuck.flag" class="admin-stuck-badge"><AlertTriangle :size="12" aria-hidden="true" />{{ workflow.stuck.reason }}</span>
							</div>
							<WorkflowStepper
								v-if="STAGE_STATES.has(workflow.state)"
								:state="workflow.state"
								:dwell-seconds="workflow.dwell_seconds"
								:stuck="workflow.stuck.flag"
							/>
							<p class="admin-workflow-meta">revision {{ workflow.revision }} · state_version {{ workflow.state_version }} · dwell {{ dwell(workflow.dwell_seconds) }} · {{ clock(workflow.updated_at) }}</p>
						</div>
					</button>
					<div v-if="hasMenu(workflow)" class="admin-row-menu">
						<button
							class="icon-button"
							type="button"
							aria-haspopup="menu"
							:aria-expanded="openMenuId === workflow.id"
							aria-label="工作流操作"
							@click="openMenuId = openMenuId === workflow.id ? undefined : workflow.id"
						><MoreHorizontal :size="16" aria-hidden="true" /></button>
						<div v-if="openMenuId === workflow.id" class="admin-menu" role="menu">
							<button v-if="canForceFail(workflow)" class="admin-menu-item danger" role="menuitem" type="button" @click="menuAction('force-fail', workflow)">Force-fail 强制失败</button>
							<button v-if="canRestart(workflow)" class="admin-menu-item danger" role="menuitem" type="button" @click="menuAction('restart', workflow)">Restart 重启</button>
						</div>
					</div>
				</div>

				<div v-if="expandedId === workflow.id" class="admin-workflow-expand">
					<template v-if="detailFor(workflow.id)">
						<dl class="admin-detail-meta">
							<div><dt>revision</dt><dd>{{ detailFor(workflow.id)!.revision }}</dd></div>
							<div><dt>state_version</dt><dd>{{ detailFor(workflow.id)!.state_version }}</dd></div>
							<div><dt>dwell</dt><dd>{{ dwell(detailFor(workflow.id)!.dwell_seconds) }}</dd></div>
							<div><dt>更新时间</dt><dd>{{ clock(detailFor(workflow.id)!.updated_at) }}</dd></div>
							<div v-if="detailFor(workflow.id)!.stuck.flag"><dt>stuck 原因</dt><dd>{{ detailFor(workflow.id)!.stuck.reason }}<template v-if="detailFor(workflow.id)!.stuck.failure_code">({{ detailFor(workflow.id)!.stuck.failure_class }}/{{ detailFor(workflow.id)!.stuck.failure_code }})</template></dd></div>
						</dl>

						<h3 class="admin-detail-heading">时间线</h3>
						<ul v-if="detailFor(workflow.id)!.ledger.length" class="admin-history-list">
							<li v-for="entry in detailFor(workflow.id)!.ledger" :key="entry.id" class="admin-history-row">
								<span class="admin-history-icon" :class="ledgerIcon(entry.kind).tone"><component :is="ledgerIcon(entry.kind).icon" :size="18" aria-hidden="true" /></span>
								<div class="admin-history-main">
									<div class="admin-history-title"><strong>{{ entry.kind }}</strong><code>{{ shortId(entry.id) }}</code></div>
									<p>{{ ledgerMeta(entry) }}</p>
								</div>
								<span class="admin-history-time">{{ relative(entry.created_at) }}</span>
							</li>
						</ul>
						<p v-else class="admin-empty">ledger 为空。</p>

						<h3 class="admin-detail-heading">AgentRun 审计</h3>
						<ul v-if="detailFor(workflow.id)!.agent_audits.length" class="admin-history-list">
							<li v-for="auditRow in detailFor(workflow.id)!.agent_audits" :key="auditRow.run_id" class="admin-history-row">
								<span class="admin-history-icon"><Bot :size="18" aria-hidden="true" /></span>
								<div class="admin-history-main">
									<div class="admin-history-title"><strong>{{ auditRow.role }}</strong><code>{{ shortId(auditRow.run_id) }}</code></div>
									<p>{{ auditMeta(auditRow) }}</p>
								</div>
								<span class="admin-history-time">{{ relative(auditRow.created_at) }}</span>
							</li>
						</ul>
						<p v-else class="admin-empty">尚无 AgentRun 审计。</p>

						<h3 class="admin-detail-heading">发布清单</h3>
						<dl v-if="detailFor(workflow.id)!.publication" class="admin-detail-meta">
							<div><dt>publication</dt><dd>{{ detailFor(workflow.id)!.publication!.id }}</dd></div>
							<div><dt>manifest digest</dt><dd>{{ detailFor(workflow.id)!.publication!.manifest_digest }}</dd></div>
							<div><dt>发布时间</dt><dd>{{ clock(detailFor(workflow.id)!.publication!.created_at) }}</dd></div>
						</dl>
						<p v-else class="admin-empty">尚未发布。</p>
					</template>
					<p v-else class="admin-empty">Loading detail...</p>
				</div>
			</article>
		</div>

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
					<button class="danger-button compact-button" type="button" :disabled="busy || !confirm.reason.trim()" @click="submitConfirm">{{ busy ? "执行中..." : "确认执行" }}</button>
				</div>
			</div>
		</div>
	</section>
</template>
