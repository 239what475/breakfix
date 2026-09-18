<script setup lang="ts">
import { PanelLeftClose, PanelLeftOpen, RefreshCw } from "lucide-vue-next";
import { computed, nextTick, onUnmounted, ref, watch } from "vue";
import { api } from "../../api/client";
import type { DocumentationPageResponse, DocumentationPracticeDetail, DocumentationPracticeSummary } from "../../api/generated";
import { documentationSource, libraryPathOf, urlPathOf } from "./documentation";
import { renderDocumentMarkdown } from "./markdown";
import DocumentationToc, { type TreeEntry } from "./DocumentationToc.vue";
import PracticePanel from "./PracticePanel.vue";
import "./documentation.css";

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

// The practice line: one published practice can hang from a heading anchor.
// Opening it collapses the outline into the reader's reserved rail slot, and
// closing it restores the outline exactly as the reader left it.
const practices = ref<DocumentationPracticeSummary[]>([]);
const activePracticeAnchor = ref<string | null>(null);
const practiceDetail = ref<DocumentationPracticeDetail | null>(null);
const practiceDetailLoading = ref(false);
const practiceDetailFailed = ref(false);
const tocCollapsedBeforePractice = ref<boolean | null>(null);

const practiceOpen = computed(() => activePracticeAnchor.value !== null);

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
  resetPracticeState();
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
  // The practice index is a per-page pull; a reader without a published
  // practice simply renders none, so failures stay silent here.
  try {
    const practiceList = await api.getDocumentationPractices(libraryPath);
    if (currentLibraryPath.value === libraryPath) practices.value = practiceList.practices;
  } catch {
    practices.value = [];
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

// The practice entry rides the rendered headings: one anchor button per
// published practice, inserted after the markdown lands in the article and
// re-inserted whenever the rendered body is replaced.
function decoratePracticeHeadings() {
  const container = article.value;
  if (!container) return;
  container.querySelectorAll(".practice-anchor-button").forEach((button) => button.remove());
  for (const practice of practices.value) {
    const heading = container.querySelector(`[id="${CSS.escape(practice.anchor)}"]`);
    if (!heading || heading.querySelector(".practice-anchor-button")) continue;
    const button = document.createElement("button");
    button.type = "button";
    button.className = "practice-anchor-button";
    button.dataset.anchor = practice.anchor;
    button.setAttribute("aria-label", `Start practice: ${practice.title}`);
    button.title = practice.title;
    button.textContent = "Practice";
    heading.appendChild(button);
  }
}

function scrollHeadingIntoView(anchor: string) {
  const target = article.value?.querySelector(`[id="${CSS.escape(anchor)}"]`);
  target?.scrollIntoView({ block: "start" });
}

function handleArticleClick(event: MouseEvent) {
  const button = (event.target as HTMLElement | null)?.closest?.(".practice-anchor-button");
  if (!(button instanceof HTMLElement)) return;
  const anchor = button.getAttribute("data-anchor");
  if (anchor) void openPractice(anchor);
}

async function openPractice(anchor: string) {
  const summary = practices.value.find((item) => item.anchor === anchor);
  if (!summary || activePracticeAnchor.value === anchor) return;
  activePracticeAnchor.value = anchor;
  practiceDetail.value = null;
  practiceDetailFailed.value = false;
  // The outline gives its column to the practice rail; the reader restores
  // the original outline state when the panel closes.
  if (tocCollapsedBeforePractice.value === null) tocCollapsedBeforePractice.value = tocCollapsed.value;
  tocCollapsed.value = true;
  await nextTick();
  scrollHeadingIntoView(anchor);
  practiceDetailLoading.value = true;
  try {
    const detail = await api.getDocumentationPractice(summary.practice_id);
    if (activePracticeAnchor.value !== anchor) return;
    practiceDetail.value = detail;
    practiceDetailLoading.value = false;
  } catch {
    if (activePracticeAnchor.value !== anchor) return;
    practiceDetailLoading.value = false;
    practiceDetailFailed.value = true;
  }
}

async function closePractice() {
  const anchor = activePracticeAnchor.value;
  if (anchor === null) return;
  activePracticeAnchor.value = null;
  practiceDetail.value = null;
  practiceDetailFailed.value = false;
  if (tocCollapsedBeforePractice.value !== null) {
    tocCollapsed.value = tocCollapsedBeforePractice.value;
    tocCollapsedBeforePractice.value = null;
  }
  await nextTick();
  scrollHeadingIntoView(anchor);
}

function resetPracticeState() {
  activePracticeAnchor.value = null;
  practiceDetail.value = null;
  practiceDetailLoading.value = false;
  practiceDetailFailed.value = false;
  practices.value = [];
  if (tocCollapsedBeforePractice.value !== null) {
    tocCollapsed.value = tocCollapsedBeforePractice.value;
    tocCollapsedBeforePractice.value = null;
  }
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

watch([body, practices], async () => {
  await nextTick();
  decoratePracticeHeadings();
});

readLocation();
void loadTreeRoots();
void loadPage(current.value.hash);
window.addEventListener("popstate", handlePopState);
onUnmounted(() => {
  window.removeEventListener("popstate", handlePopState);
  observer?.disconnect();
});
</script>

<template>
  <section class="documentation-page" aria-label="Kubernetes documentation">
    <!-- The grid reserves a right column for the practice rail; opening a
         practice collapses the outline and renders the panel in the rail. -->
    <div class="documentation-reader" :class="{ 'toc-collapsed': tocCollapsed, 'practice-open': practiceOpen }">
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
             from markdown-it's own rules plus classed wrappers. The practice
             anchor buttons are attached after render, never by the parser. -->
        <!-- eslint-disable-next-line vue/no-v-html -->
        <article v-else-if="page" ref="article" class="documentation-article" @click="handleArticleClick" v-html="body"></article>
        <div v-else class="documentation-empty">
          <h2>Documentation</h2>
          <p>Choose a page from the outline to start reading.</p>
        </div>
      </div>
      <div class="documentation-rail">
        <PracticePanel
          v-if="practiceOpen"
          :detail="practiceDetail"
          :loading="practiceDetailLoading"
          :failed="practiceDetailFailed"
          @close="closePractice"
        />
      </div>
    </div>
  </section>
</template>
