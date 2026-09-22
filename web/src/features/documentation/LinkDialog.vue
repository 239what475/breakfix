<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { X } from "lucide-vue-next";
import type { AdminDocumentationLinkInput, DocumentationLink } from "../../api/generated";
import "../../styles/dialog.css";

const props = defineProps<{ link: DocumentationLink | null; otherTitles?: string[]; submitError?: string }>();
const emit = defineEmits<{
	close: [];
	save: [input: AdminDocumentationLinkInput];
	del: [];
}>();

// The dialog starts from its target's current values; create mode starts
// blank with embedding on, matching the server default.
const title = ref("");
const url = ref("");
const embed = ref(true);
const error = ref("");

watch(
	() => props.link,
	(target) => {
		title.value = target?.title ?? "";
		url.value = target?.url ?? "";
		embed.value = target?.embed ?? true;
		error.value = "";
	},
	{ immediate: true },
);

// Server-side rejections (a lost connection, a removed entry) surface in the
// same slot as the local validation copy.
const shownError = computed(() => error.value || props.submitError || "");

// Titles are deliberately non-unique, but an accidental duplicate deserves a
// heads-up; the admin can still save it.
const duplicateTitle = computed(() => {
	const candidate = title.value.trim().toLowerCase();
	if (!candidate) return false;
	return (props.otherTitles ?? []).some((title) => title.trim().toLowerCase() === candidate);
});

const editing = computed(() => props.link !== null);

function isHttpUrl(raw: string): boolean {
	try {
		const parsed = new URL(raw);
		return parsed.protocol === "http:" || parsed.protocol === "https:";
	} catch {
		return false;
	}
}

// Same rules the server enforces: a non-empty title and an absolute
// http(s) URL. Anything else is refused before a request is spent.
function validate(): string | null {
	if (!title.value.trim()) return "Name is required.";
	if (!isHttpUrl(url.value.trim())) {
		return "URL must be an absolute http or https address.";
	}
	return null;
}

function save() {
	const invalid = validate();
	if (invalid) {
		error.value = invalid;
		return;
	}
	emit("save", { title: title.value.trim(), url: url.value.trim(), embed: embed.value });
}
</script>

<template>
	<div class="dialog-backdrop" @click.self="emit('close')">
		<div class="dialog" role="dialog" aria-modal="true" :aria-labelledby="editing ? 'doc-link-edit-title' : 'doc-link-add-title'">
			<button class="icon-button dialog-close" type="button" aria-label="Close" @click="emit('close')"><X :size="15" aria-hidden="true" /></button>
			<h2 v-if="editing" id="doc-link-edit-title">Edit documentation link</h2>
			<h2 v-else id="doc-link-add-title">Add documentation link</h2>
			<p class="dialog-copy">
				Entries are shared with every visitor. Whether the target allows iframe framing cannot be detected from here — verify it in the browser and record the verdict below; the open-in-new-window entry is always available as a fallback.
			</p>
			<form @submit.prevent="save">
				<label>Name
					<input v-model="title" type="text" name="title" placeholder="Kubernetes documentation" />
				</label>
				<label>URL
					<input v-model="url" type="text" name="url" placeholder="https://kubernetes.io/docs" />
				</label>
				<label class="doc-link-embed-toggle">
					<input v-model="embed" type="checkbox" name="embed" />
					<span>Allow embedding (show it in an iframe on this page)</span>
				</label>
				<p v-if="duplicateTitle" class="dialog-foot">Another link already uses this name.</p>
				<p v-if="shownError" class="form-error">{{ shownError }}</p>
				<div class="dialog-actions">
					<button v-if="editing" class="text-button danger-text-button" type="button" @click="emit('del')">Delete</button>
					<button class="text-button" type="button" @click="emit('close')">Cancel</button>
					<button class="compact-button" type="submit">Save</button>
				</div>
			</form>
		</div>
	</div>
</template>
