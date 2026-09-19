<script setup lang="ts">
import { computed, ref } from "vue";
import { CheckCircle2, ChevronDown, ChevronRight, FlaskConical, MoreHorizontal, RefreshCw } from "lucide-vue-next";
import { clock, shortId } from "./format";

const props = defineProps<{
	active: boolean;
	refreshRequest: number;
	corpus: ReturnType<typeof import("./admin").useAdminCorpus>;
	runWorkflowAction: (kind: "force-fail" | "restart", id: string, reason: string) => Promise<boolean>;
}>();

const view = ref<"corpus" | "batches">("corpus");

const confirm = ref<false | { kind: "pause" | "resume" | "cancel" | "retry-failed" | "rescue"; id: string; label: string; reason: string }>(false);
const rescue = ref<false | { kind: "force-fail" | "restart"; id: string; label: string; reason: string }>(false);
const confirmError = ref("");
const showCreate = ref(false);

const selectedBatch = computed(() => props.corpus.selectedBatch.value);
const progress = computed(() => {
	const batch = selectedBatch.value;
	if (!batch?.counts) return null;
	const c = batch.counts;
	const done = (c.Published ?? 0) + (c.NoPractice ?? 0) + (c.Rejected ?? 0) + (c.Failed ?? 0) + (c.Skipped ?? 0) + (c.Cancelled ?? 0);
	return { done, total: batch.total_items };
});

function submitConfirm() {
	if (!confirm.value) return;
	const reason = confirm.value.reason.trim();
	if (!reason || reason.length > 500) {
		confirmError.value = "Reason 必填且不超过 500 字";
		return;
	}
	const current = confirm.value;
	if (!current) return;
	const { kind, id } = current;
	void (async () => {
		let ok: boolean;
		if (kind === "rescue" && rescue.value) {
			ok = await props.corpus.runWorkflowAction(rescue.value.kind, id, reason);
		} else if (kind === "rescue") {
			ok = false;
		} else {
			ok = await props.corpus.runBatchAction(kind, id, reason);
		}
		if (ok) {
			confirm.value = false;
			rescue.value = false;
		} else {
			confirmError.value = "操作失败,请稍后重试";
		}
	})();
}

function workflowBadge(state: string): { label: string; tone: "ok" | "danger" | "warn" | "info" | "muted" } {
	switch (state) {
		case "Published": return { label: "已发布", tone: "ok" };
		case "Failed": return { label: "失败", tone: "danger" };
		case "Rejected": return { label: "被否决", tone: "warn" };
		case "NoPractice": return { label: "无实践", tone: "muted" };
		default: return { label: "进行中", tone: "info" };
	}
}
</script>

<template>
	<div class="admin-corpus">
		<nav class="admin-tabs" aria-label="Corpus views">
			<button :class="{ active: view === 'corpus' }" type="button" @click="view = 'corpus'">语料树</button>
			<button :class="{ active: view === 'batches' }" type="button" @click="view = 'batches'">批次</button>
		</nav>

		<p v-if="props.corpus.corpusError.value" class="admin-error">{{ props.corpus.corpusError.value }}</p>

		<section v-show="view === 'corpus'" aria-label="Corpus tree">
			<form class="admin-corpus-filters" @submit.prevent="props.corpus.applyCorpusFilters()">
				<label class="admin-corpus-search">
					<span>标题搜索</span>
					<input v-model="props.corpus.searchInput.value" type="search" placeholder="按页面标题搜索" aria-label="Search page titles" />
				</label>
				<label class="admin-corpus-check">
					<input v-model="props.corpus.failuresOnly.value" type="checkbox" />
					<span>只看失败/卡住</span>
				</label>
				<button class="compact-button" type="submit">应用</button>
				<button class="icon-button" type="button" aria-label="Refresh corpus" @click="props.corpus.refresh()"><RefreshCw :size="14" aria-hidden="true" /></button>
			</form>

			<div class="admin-corpus-tree" role="tree" aria-label="Documentation sections">
				<template v-for="root in props.corpus.treeRoots.value" :key="root.path">
					<div class="admin-corpus-section" role="treeitem" :aria-expanded="!!root.loaded">
						<button class="admin-corpus-section-toggle" type="button" @click="props.corpus.loadTreeSection(root)">
							<ChevronRight v-if="!root.loaded" :size="14" aria-hidden="true" />
							<ChevronDown v-else :size="14" aria-hidden="true" />
							<span>{{ root.title }}</span>
							<small>{{ root.path }}</small>
						</button>
						<label class="admin-corpus-check">
							<input type="checkbox" :checked="props.corpus.checkedSections.value.includes(root.path)" @change="props.corpus.toggleSectionChecked(root)" />
							<span>纳入批次</span>
						</label>
					</div>
					<template v-if="root.loaded">
						<div v-for="child in root.children" :key="child.path" class="admin-corpus-section admin-corpus-section-child">
							<span class="admin-corpus-section-path">{{ child.title }}</span>
							<label class="admin-corpus-check">
								<input type="checkbox" :checked="props.corpus.checkedSections.value.includes(child.path)" @change="props.corpus.toggleSectionChecked(child)" />
								<span>纳入批次</span>
							</label>
						</div>
					</template>
				</template>
			</div>

			<ul class="admin-corpus-pages" aria-label="Corpus pages">
				<li v-for="row in props.corpus.corpusRows.value" :key="row.page_path" class="admin-corpus-page">
					<button class="admin-corpus-page-toggle" type="button" :aria-expanded="!!props.corpus.pageRows.value[row.page_path]" @click="props.corpus.togglePageRow(row.page_path)">
						<strong>{{ row.title || row.page_path }}</strong>
						<small>{{ row.page_path }}</small>
						<span class="admin-corpus-rollup">
							<span class="badge-ok">已发布 {{ row.published }}/{{ row.total }}</span>
							<span class="badge-danger" v-if="row.failed">失败 {{ row.failed }}</span>
							<span class="badge-info" v-if="row.in_progress">进行中 {{ row.in_progress }}</span>
							<span class="badge-muted" v-if="row.no_practice">无实践 {{ row.no_practice }}</span>
							<span class="badge-warn" v-if="row.stuck">卡住 {{ row.stuck }}</span>
						</span>
					</button>
					<div v-if="props.corpus.pageRows.value[row.page_path]" class="admin-corpus-workflows">
						<div v-for="entry in props.corpus.pageRows.value[row.page_path]" :key="entry.workflow.id" class="admin-corpus-workflow">
							<button class="admin-corpus-workflow-toggle" type="button" @click="props.corpus.expandWorkflow(entry)">
								<span class="badge" :class="`badge-${workflowBadge(entry.workflow.state).tone}`">{{ workflowBadge(entry.workflow.state).label }}</span>
								<span class="admin-corpus-anchor">{{ entry.workflow.anchor || "(页面级)" }}</span>
								<small>{{ shortId(entry.workflow.id) }} · {{ clock(entry.workflow.updated_at) }}</small>
							</button>
							<div v-if="!entry.workflow.stuck.flag && entry.workflow.state !== 'Published'" class="admin-row-menu">
								<button class="icon-button" type="button" aria-label="工作流操作" @click="rescue = { kind: entry.workflow.state === 'Failed' || entry.workflow.state === 'Rejected' ? 'restart' : 'force-fail', id: entry.workflow.id, label: entry.workflow.anchor || entry.workflow.page_path || entry.workflow.id, reason: '' }; confirm = { kind: 'rescue', id: entry.workflow.id, label: entry.workflow.id, reason: '' }">
									<MoreHorizontal :size="15" aria-hidden="true" />
								</button>
							</div>
						</div>
					</div>
				</li>
			</ul>
			<button v-if="props.corpus.corpusCursor.value" class="compact-button" type="button" :disabled="props.corpus.corpusLoadingMore.value" @click="props.corpus.loadMoreCorpus()">加载更多</button>
		</section>

		<section v-show="view === 'batches'" aria-label="Documentation batches">
			<div class="admin-corpus-batch-actions">
				<button class="compact-button" type="button" @click="showCreate = !showCreate">
					<FlaskConical :size="14" aria-hidden="true" />发起批次
				</button>
			</div>

			<div v-if="showCreate" class="admin-corpus-create">
				<p>从语料树勾选分区发起,或一键覆盖全库。批次受理后由服务端调度器按并发阀值铺开,失败是条目级,可整批暂停/取消。</p>
				<p v-if="props.corpus.checkedSections.value.length">已选 {{ props.corpus.checkedSections.value.length }} 个分区: {{ props.corpus.checkedSections.value.join(", ") }}</p>
				<p v-if="props.corpus.createError.value" class="admin-error">{{ props.corpus.createError.value }}</p>
				<div class="admin-confirm-actions">
					<button class="compact-button" type="button" :disabled="props.corpus.creating.value || !props.corpus.checkedSections.value.length" @click="props.corpus.createBatch({ kind: 'sections', sections: [...props.corpus.checkedSections.value] })">按所选分区发起</button>
					<button class="compact-button" type="button" :disabled="props.corpus.creating.value" @click="props.corpus.createBatch({ kind: 'full' })">全库批次</button>
					<button class="text-button" type="button" @click="showCreate = false">收起</button>
				</div>
			</div>

			<ul class="admin-batch-list" aria-label="Batches">
				<li v-for="batch in props.corpus.batches.value" :key="batch.id" class="admin-batch-row" :class="{ active: props.corpus.selectedBatchId.value === batch.id }">
					<button class="admin-batch-toggle" type="button" @click="props.corpus.openBatch(batch.id)">
						<strong>{{ shortId(batch.id) }}</strong>
						<span class="badge" :class="batch.state === 'Running' ? 'badge-info' : batch.state === 'Completed' ? 'badge-ok' : batch.state === 'Cancelled' ? 'badge-muted' : batch.state === 'Paused' ? 'badge-warn' : 'badge-neutral'">{{ batch.state }}</span>
						<small>{{ batch.total_items }} 条目 · 并发 {{ batch.concurrency }} · {{ clock(batch.created_at) }}</small>
					</button>
					<div class="admin-batch-actions">
						<button v-if="batch.state === 'Running'" class="compact-button" type="button" @click="confirm = { kind: 'pause', id: batch.id, label: batch.id, reason: '' }; confirmError = ''">暂停</button>
						<button v-if="batch.state === 'Paused'" class="compact-button" type="button" @click="confirm = { kind: 'resume', id: batch.id, label: batch.id, reason: '' }; confirmError = ''">恢复</button>
						<button v-if="!['Completed', 'Cancelled'].includes(batch.state)" class="compact-button" type="button" @click="confirm = { kind: 'cancel', id: batch.id, label: batch.id, reason: '' }; confirmError = ''">取消</button>
						<button v-if="['Running', 'Paused'].includes(batch.state) && (batch.counts?.Failed ?? 0) > 0" class="compact-button" type="button" @click="confirm = { kind: 'retry-failed', id: batch.id, label: batch.id, reason: '' }; confirmError = ''">重试失败页</button>
					</div>
				</li>
			</ul>

			<div v-if="selectedBatch" class="admin-batch-detail">
				<header>
					<h2>{{ shortId(selectedBatch.id) }}<span class="badge badge-info">{{ selectedBatch.state }}</span></h2>
					<small>并发 {{ selectedBatch.concurrency }} · 发起人 {{ selectedBatch.created_by }} · {{ clock(selectedBatch.created_at) }}</small>
				</header>
				<p v-if="progress" class="admin-batch-progress">
					<CheckCircle2 :size="14" aria-hidden="true" /> {{ progress.done }}/{{ progress.total }} 条目已收尾
					<template v-if="selectedBatch.counts">
						· 已发布 {{ selectedBatch.counts.Published ?? 0 }}
						· 失败 {{ selectedBatch.counts.Failed ?? 0 }}
						· 进行中 {{ (selectedBatch.counts.Scheduled ?? 0) + (selectedBatch.counts.Running ?? 0) + (selectedBatch.counts.Pending ?? 0) }}
						· 无实践 {{ selectedBatch.counts.NoPractice ?? 0 }}
						· 跳过 {{ selectedBatch.counts.Skipped ?? 0 }}
					</template>
				</p>
				<div class="admin-batch-item-filters" role="group" aria-label="Filter batch items">
					<button v-for="state in ['Pending', 'Scheduled', 'Running', 'Failed', 'Published', 'Skipped']" :key="state" class="compact-button" :class="{ active: props.corpus.batchItemsState.value === state }" type="button" @click="props.corpus.filterBatchItems(state)">{{ state }}</button>
				</div>
				<ul class="admin-batch-items" aria-label="Batch items">
					<li v-for="item in props.corpus.batchItems.value" :key="item.id">
						<span class="badge" :class="`badge-${workflowBadge(item.state).tone}`">{{ item.state }}</span>
						<span>{{ item.page_path }}</span>
						<span class="admin-corpus-anchor">#{{ item.anchor }}</span>
						<small v-if="item.detail">{{ item.detail }}</small>
					</li>
				</ul>
				<button v-if="props.corpus.batchItemsCursor.value" class="compact-button" type="button" @click="props.corpus.loadMoreBatchItems()">加载更多条目</button>
			</div>
		</section>

		<div v-if="confirm" class="dialog-backdrop" @click.self="confirm = false; rescue = false">
			<div class="dialog" role="dialog" aria-modal="true" aria-labelledby="corpus-confirm-title">
				<h2 id="corpus-confirm-title">{{ rescue ? '工作流救援' : '批次操作' }}确认</h2>
				<dl class="admin-confirm-summary">
					<template v-if="rescue">
						<div><dt>操作</dt><dd>{{ rescue.kind === 'restart' ? 'Restart 重启' : 'Force-fail 强制失败' }}</dd></div>
						<div><dt>工作流</dt><dd>{{ rescue.id }}</dd></div>
					</template>
					<template v-else>
						<div><dt>操作</dt><dd>{{ confirm.kind }}</dd></div>
						<div><dt>批次</dt><dd>{{ confirm.id }}</dd></div>
					</template>
				</dl>
				<label class="admin-confirm-reason">
					<span>Reason(必填,≤500 字,写入人操作审计)</span>
					<textarea v-model="confirm.reason" rows="3" maxlength="500" placeholder="说明原因"></textarea>
				</label>
				<p v-if="confirmError" class="admin-error">{{ confirmError }}</p>
				<div class="admin-confirm-actions">
					<button class="text-button" type="button" @click="confirm = false; rescue = false">取消</button>
					<button class="danger-button compact-button" :disabled="props.corpus.busy.value || !confirm.reason.trim()" @click="submitConfirm">确认执行</button>
				</div>
			</div>
		</div>
	</div>
</template>
