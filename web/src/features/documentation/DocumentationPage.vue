<script setup lang="ts">
import { ExternalLink, PanelLeftClose, PanelLeftOpen, Plus, RefreshCw, Settings } from "lucide-vue-next";
import { computed, onMounted, onUnmounted, ref } from "vue";
import { api } from "../../api/client";
import type { AdminDocumentationLinkInput, DocumentationLink } from "../../api/generated";
import LinkDialog from "./LinkDialog.vue";
import "./documentation.css";

defineProps<{ isAdmin?: boolean }>();

const loading = ref(false);
const loadFailed = ref(false);
const links = ref<DocumentationLink[]>([]);
const selectedKey = ref<string>();
const listCollapsed = ref(false);
const drawerOpen = ref(false);
const busy = ref(false);

const selected = computed(() => links.value.find((link) => link.key === selectedKey.value));

// Keep-alive pool: iframes stay mounted under v-show so switching between
// the most recent documents never reloads them; the pool caps at five and
// drops the least recently used document when full. Only embeddable entries
// enter it — a URL-card entry would burn a slot on a frame that never shows.
const KEEP_ALIVE_LIMIT = 5;
const frames = ref<DocumentationLink[]>([]);

function trackFrame(link: DocumentationLink) {
	const index = frames.value.findIndex((frame) => frame.key === link.key);
	if (index >= 0) {
		if (index !== frames.value.length - 1) {
			const [frame] = frames.value.splice(index, 1);
			frames.value.push(frame);
		}
		// Always mirror an admin edit so the iframe src follows the change.
		frames.value[frames.value.length - 1] = link;
		return;
	}
	frames.value.push(link);
	while (frames.value.length > KEEP_ALIVE_LIMIT) {
		frames.value.shift();
	}
}

function untrackFrame(key: string) {
	frames.value = frames.value.filter((frame) => frame.key !== key);
}

function select(link: DocumentationLink) {
	selectedKey.value = link.key;
	if (link.embed) trackFrame(link);
	drawerOpen.value = false;
	if (window.location.search !== `?doc=${encodeURIComponent(link.key)}`) {
		window.history.pushState({ documentation: true }, "", `/documentation?doc=${encodeURIComponent(link.key)}`);
	}
}

function readLocation(): DocumentationLink | null {
	const key = new URLSearchParams(window.location.search).get("doc");
	if (!key) return null;
	return links.value.find((link) => link.key === key) ?? null;
}

function syncFromLocation() {
	const link = readLocation();
	if (link) {
		select(link);
		return;
	}
	selectedKey.value = undefined;
	drawerOpen.value = false;
}

async function loadLinks() {
	loading.value = true;
	loadFailed.value = false;
	try {
		const response = await api.listDocumentationLinks();
		links.value = response.links;
		loading.value = false;
		// A ?doc=<key> pointing at a removed entry falls back to the empty
		// state rather than an arbitrary link.
		if (!selected.value) syncFromLocation();
	} catch {
		loading.value = false;
		loadFailed.value = true;
	}
}

// ---------------------------------------------------------------------------
// Admin mutations. The local list is reconciled from the server responses so
// the dialog and the iframe pool never drift from the stored records.

const dialogLink = ref<DocumentationLink | null>(null);
const dialogOpen = ref(false);
const submitError = ref("");
// Titles the dialog warns about duplicating: every entry except the one
// currently being edited.
const otherTitles = computed(() =>
	links.value.filter((link) => link.key !== dialogLink.value?.key).map((link) => link.title),
);

function openCreate() {
	dialogLink.value = null;
	submitError.value = "";
	dialogOpen.value = true;
}

function openEdit(link: DocumentationLink) {
	dialogLink.value = link;
	submitError.value = "";
	dialogOpen.value = true;
}

async function saveLink(input: AdminDocumentationLinkInput) {
	if (busy.value) return;
	busy.value = true;
	try {
		if (dialogLink.value) {
			const updated = await api.updateDocumentationLink(dialogLink.value.key, input);
			links.value = links.value.map((link) => (link.key === updated.key ? updated : link));
			if (updated.embed) trackFrame(updated);
			else untrackFrame(updated.key);
		} else {
			const created = await api.createDocumentationLink(input);
			links.value = [...links.value, created];
			select(created);
		}
		dialogOpen.value = false;
	} catch {
		// The dialog stays open with every value intact; the failure shows
		// in its error slot so nothing the admin typed is lost.
		submitError.value = "Saving the link failed. Try again.";
	} finally {
		busy.value = false;
	}
}

async function deleteLink() {
	const target = dialogLink.value;
	if (!target || busy.value) return;
	busy.value = true;
	try {
		await api.deleteDocumentationLink(target.key);
		dialogOpen.value = false;
		untrackFrame(target.key);
		links.value = links.value.filter((link) => link.key !== target.key);
		if (selectedKey.value === target.key) {
			// Back to the empty state; a wrong auto-selection would invite a
			// second delete click on the wrong row.
			selectedKey.value = undefined;
			window.history.pushState({ documentation: true }, "", "/documentation");
		}
	} catch {
		// Mirror the save path: the dialog stays open and the failure lands in
		// its error slot, so a failed delete is never silent.
		submitError.value = "Deleting the link failed. Try again.";
	} finally {
		busy.value = false;
	}
}

function handlePopState() {
	if (window.location.pathname === "/documentation") syncFromLocation();
}

onMounted(() => {
	window.addEventListener("popstate", handlePopState);
	void loadLinks();
});

onUnmounted(() => {
	window.removeEventListener("popstate", handlePopState);
});
</script>

<template>
  <section class="documentation-page" aria-label="Documentation">
    <div class="documentation-shell" :class="{ 'list-collapsed': listCollapsed }">
      <nav class="documentation-list" :class="{ open: drawerOpen }" aria-label="Documentation links">
        <div class="documentation-list-head">
          <span>Documentation</span>
          <button class="compact-button" type="button" aria-label="Close documentation list" @click="drawerOpen = false">×</button>
        </div>
        <ul class="documentation-list-items">
          <li v-for="link in links" :key="link.key" class="documentation-list-item" :class="{ active: link.key === selectedKey }">
            <button class="documentation-list-link" type="button" :title="link.url" @click="select(link)">
              <span class="documentation-list-title">{{ link.title }}</span>
            </button>
            <button v-if="isAdmin" class="compact-button documentation-item-settings" type="button" :aria-label="`Settings for ${link.title}`" @click="openEdit(link)">
              <Settings :size="13" aria-hidden="true" />
            </button>
          </li>
          <li v-if="isAdmin" class="documentation-list-item documentation-list-add">
            <button class="documentation-list-link" type="button" @click="openCreate">
              <Plus :size="14" aria-hidden="true" /> <span>Add documentation link</span>
            </button>
          </li>
        </ul>
      </nav>
      <div v-if="drawerOpen" class="documentation-list-backdrop" @click="drawerOpen = false"></div>

      <div class="documentation-body">
        <div class="documentation-toolbar">
          <button
            class="compact-button documentation-list-toggle"
            type="button"
            :aria-label="listCollapsed ? 'Expand documentation list' : 'Collapse documentation list'"
            @click="listCollapsed = !listCollapsed"
          >
            <component :is="listCollapsed ? PanelLeftOpen : PanelLeftClose" :size="14" aria-hidden="true" />
          </button>
          <button class="compact-button documentation-list-mobile" type="button" @click="drawerOpen = true">
            <PanelLeftOpen :size="14" aria-hidden="true" /> Contents
          </button>
          <span class="documentation-toolbar-title" :title="selected?.url">{{ selected ? selected.title : "Documentation" }}</span>
          <a v-if="selected" class="documentation-open-external" :href="selected.url" target="_blank" rel="noopener noreferrer">
            <ExternalLink :size="13" aria-hidden="true" /> Open in new window
          </a>
        </div>

        <!-- v-show, not v-if: the pool must stay mounted while an embed=false
             entry shows its URL card, or every kept-alive frame reloads. -->
        <div v-show="selected && selected.embed" class="documentation-frames">
          <iframe
            v-for="frame in frames"
            v-show="frame.key === selectedKey"
            :key="frame.key"
            class="documentation-frame"
            :src="frame.url"
            :title="frame.title"
            referrerpolicy="no-referrer"
          ></iframe>
        </div>
        <div v-if="selected && !selected.embed" class="documentation-external">
          <p>This site cannot be verified to allow embedding.</p>
          <a class="documentation-external-card" :href="selected.url" target="_blank" rel="noopener noreferrer">
            <ExternalLink :size="15" aria-hidden="true" />
            <span class="documentation-external-title">{{ selected.title }}</span>
            <span class="documentation-external-url">{{ selected.url }}</span>
          </a>
        </div>
        <div v-else-if="!selected && loadFailed" class="documentation-empty documentation-empty-error">
          <strong>The documentation list is unavailable.</strong>
          <button class="compact-button" type="button" @click="loadLinks">
            <RefreshCw :size="14" aria-hidden="true" /> Retry
          </button>
        </div>
        <div v-else-if="!selected" class="documentation-empty">
          <h2>Documentation</h2>
          <p v-if="loading">Loading documentation...</p>
          <p v-else-if="!links.length">No documentation has been added yet.</p>
          <p v-else>Choose a document from the list to start reading.</p>
        </div>
      </div>
    </div>

    <LinkDialog v-if="dialogOpen" :link="dialogLink" :other-titles="otherTitles" :submit-error="submitError" @close="dialogOpen = false" @save="saveLink" @del="deleteLink" />
  </section>
</template>
