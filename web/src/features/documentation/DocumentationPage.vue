<script setup lang="ts">
import { PanelLeftClose, PanelLeftOpen, RefreshCw } from "lucide-vue-next";
import { computed, nextTick, onUnmounted, ref, watch } from "vue";
import { api } from "../../api/client";
import type { DocumentationPageResponse } from "../../api/generated";
import { documentationSource, libraryPathOf, urlPathOf } from "./documentation";
import { renderDocumentMarkdown } from "./markdown";
import DocumentationToc, { type TreeEntry } from "./DocumentationToc.vue";
import BlankScenarioToolbar from "./BlankScenarioToolbar.vue";
import BlankScenarioPanel from "./BlankScenarioPanel.vue";
import { useBlankScenario } from "./useBlankScenario";
import "./documentation.css";

const props = defineProps<{ authSignal?: number }>();
const emit = defineEmits<{
  "request-auth": [];
  notice: [text: string, kind?: "error" | "info"];
}>();

const entryUrlPath: string = documentationSource.entryPath;
const loading = ref(false);
const failed = ref(false);
const page = ref<DocumentationPageResponse | null>(null);
const body = ref("");
const current = ref({ path: entryUrlPath, hash: "" });
const roots = ref<TreeEntry[]>([]);
const expanded = ref<Set<string>>(new Set());
const tocFailed = ref(false);
const tocOpen = ref(false);
const tocCollapsed = ref(false);
const article = ref<HTMLElement>();

// The practice ground is page-independent: one blank scenario per reader on
// the pinned library, driven from the right-side toolbar and shared by every
// documentation page.
const notify = (text: string, kind?: "error" | "info") => emit("notice", text, kind);
const scenario = useBlankScenario(notify, () => emit("request-auth"));

watch(
  () => props.authSignal,
  () => void scenario.resumeAfterAuth(),
);

const currentLibraryPath = computed(() => libraryPathOf(current.value.path));

function validPath(path: unknown): path is string {
  return typeof path === "string" && (path === entryUrlPath.slice(0, -1) || path.startsWith(entryUrlPath));
}

function normalizedHash(value: unknown): string | null {
  if (value === "") return "";
  if (typeof value !== "string") return null;
  const hash = value.startsWith("#") ? value : `#${value}`;
  return /^#[^\s]*$/.test(hash) ? hash : null;
}

function readerUrl() {
  const params = new URLSearchParams({
    source: documentationSource.source,
    version: documentationSource.version,
    path: current.value.path,
  });
  if (current.value.hash) params.set("hash", current.value.hash.slice(1));
  return `/documentation?${params.toString()}`;
}

function replaceReaderUrl() {
  window.history.replaceState({ documentation: true }, "", readerUrl());
}

function readLocation() {
  const params = new URLSearchParams(window.location.search);
  const source = params.get("source");
  const version = params.get("version");
  const path = params.get("path");
  const hash = normalizedHash(params.get("hash") || "");
  if (source === documentationSource.source && version === documentationSource.version && validPath(path) && hash !== null) {
    current.value = { path, hash };
  } else {
    current.value = { path: entryUrlPath, hash: "" };
    replaceReaderUrl();
  }
}

async function loadPage(scrollHash: string) {
  const libraryPath = currentLibraryPath.value;
  if (!libraryPath || libraryPath === "docs") {
    // A section root is not a page: keep the location and show the outline.
    page.value = null;
    body.value = "";
    return;
  }
  loading.value = true;
  failed.value = false;
  try {
    const fetched = await api.getDocumentationPage(libraryPath);
    page.value = fetched;
    body.value = await renderDocumentMarkdown(fetched.markdown, fetched.anchors);
    loading.value = false;
    await nextTick();
    revealAnchor(scrollHash);
  } catch {
    loading.value = false;
    failed.value = true;
  }
}

function revealAnchor(hash: string) {
  if (!hash) {
    article.value?.scrollIntoView({ block: "start" });
    return;
  }
  const target = article.value?.querySelector(`[id="${CSS.escape(hash.replace(/^#/, ""))}"]`);
  target?.scrollIntoView({ block: "start" });
}

function navigate(urlPath: string) {
  current.value = { path: urlPathOf(libraryPathOf(urlPath)), hash: "" };
  window.history.pushState({ documentation: true }, "", readerUrl());
  tocOpen.value = false;
  void loadPage("");
}

function retry() {
  void loadPage(current.value.hash);
}

async function ensureChildren(entry: TreeEntry) {
  if (!entry.hasChildren || entry.children) return;
  try {
    const response = await api.getDocumentationTree(entry.path);
    entry.children = response.nodes.map((node) => ({
      title: node.title,
      path: node.path,
      hasChildren: node.has_children,
    }));
  } catch {
    tocFailed.value = true;
  }
}

async function toggleEntry(entry: TreeEntry) {
  await ensureChildren(entry);
  const next = new Set(expanded.value);
  if (next.has(entry.path)) next.delete(entry.path);
  else next.add(entry.path);
  expanded.value = next;
}

async function openEntry(entry: TreeEntry) {
  await ensureChildren(entry);
  expanded.value = new Set([...expanded.value, entry.path]);
  navigate(entry.path);
}

async function loadTreeRoots() {
  try {
    const response = await api.getDocumentationTree();
    roots.value = response.nodes.map((node) => ({
      title: node.title,
      path: node.path,
      hasChildren: node.has_children,
    }));
    // Open the top-level section containing the current page so the reader
    // starts in context instead of a bare outline.
    for (const root of roots.value) {
      if (current.value.path.startsWith(`${root.path}/`)) {
        expanded.value = new Set([...expanded.value, root.path]);
        await ensureChildren(root);
      }
    }
  } catch {
    tocFailed.value = true;
  }
}

function handlePopState() {
  readLocation();
  void loadPage(current.value.hash);
}

let observer: IntersectionObserver | undefined;

function observeHeadings() {
  observer?.disconnect();
  observer = new IntersectionObserver(
    (entries) => {
      const visible = entries
        .filter((entry) => entry.isIntersecting)
        .sort((left, right) => left.boundingClientRect.top - right.boundingClientRect.top)[0];
      const id = visible?.target.getAttribute("id");
      if (!id || current.value.hash === `#${id}`) return;
      current.value = { ...current.value, hash: `#${id}` };
      replaceReaderUrl();
    },
    { rootMargin: "-72px 0px -55% 0px", threshold: 0 },
  );
  article.value?.querySelectorAll("h1[id], h2[id], h3[id], h4[id], h5[id], h6[id]").forEach((heading) => {
    observer?.observe(heading);
  });
}

watch(body, async () => {
  await nextTick();
  observeHeadings();
});

readLocation();
void loadTreeRoots();
void loadPage(current.value.hash);
void scenario.refresh();
window.addEventListener("popstate", handlePopState);
onUnmounted(() => {
  window.removeEventListener("popstate", handlePopState);
  observer?.disconnect();
});
</script>

<template>
  <section class="documentation-page" aria-label="Kubernetes documentation">
    <!-- The grid reserves a rail column for the blank practice panel plus a
         slim vertical toolbar on the right edge, shared by every page. -->
    <div class="documentation-reader" :class="{ 'toc-collapsed': tocCollapsed, 'scenario-open': scenario.sessionOpen.value }">
      <button
        class="compact-button documentation-toc-toggle"
        type="button"
        :aria-label="tocCollapsed ? 'Expand documentation outline' : 'Collapse documentation outline'"
        @click="tocCollapsed = !tocCollapsed"
      >
        <component :is="tocCollapsed ? PanelLeftOpen : PanelLeftClose" :size="14" aria-hidden="true" />
      </button>
      <button class="compact-button documentation-toc-mobile" type="button" @click="tocOpen = true">
        <PanelLeftOpen :size="14" aria-hidden="true" /> Contents
      </button>

      <nav class="documentation-toc" :class="{ open: tocOpen }" aria-label="Documentation outline">
        <div class="documentation-toc-head">
          <span>Documentation</span>
          <button class="compact-button" type="button" aria-label="Close documentation outline" @click="tocOpen = false">×</button>
        </div>
        <p v-if="tocFailed" class="documentation-toc-error">The outline is unavailable.</p>
        <DocumentationToc
          :entries="roots"
          :expanded="expanded"
          :current-path="currentLibraryPath"
          @toggle="toggleEntry"
          @open="openEntry"
        />
      </nav>
      <div v-if="tocOpen" class="documentation-toc-backdrop" @click="tocOpen = false"></div>

      <div class="documentation-body">
        <div v-if="loading" class="documentation-state">Loading documentation...</div>
        <div v-else-if="failed" class="documentation-state documentation-state-error">
          <strong>Documentation is unavailable.</strong>
          <button class="compact-button" type="button" @click="retry">
            <RefreshCw :size="14" aria-hidden="true" /> Retry
          </button>
        </div>
        <!-- Rendered from library markdown with html:false; all markup comes
             from markdown-it's own rules plus classed wrappers. -->
        <!-- eslint-disable-next-line vue/no-v-html -->
        <article v-else-if="page" ref="article" class="documentation-article" v-html="body"></article>
        <div v-else class="documentation-empty">
          <h2>Documentation</h2>
          <p>Choose a page from the outline to start reading.</p>
        </div>
      </div>
      <div class="documentation-rail">
        <BlankScenarioPanel
          v-if="scenario.sessionOpen.value"
          :state="scenario.state.value"
          :starting="scenario.starting.value"
          :stopping="scenario.stopping.value"
          :resetting="scenario.resetting.value"
          :runtime="scenario.runtime.value"
          @create="scenario.createFor()"
          @reset="scenario.reset()"
          @close="scenario.close()"
        />
      </div>
      <BlankScenarioToolbar
        :state="scenario.state.value"
        :starting="scenario.starting.value"
        :stopping="scenario.stopping.value"
        :resetting="scenario.resetting.value"
        @create="scenario.createFor()"
        @reset="scenario.reset()"
        @close="scenario.close()"
      />
    </div>
  </section>
</template>
