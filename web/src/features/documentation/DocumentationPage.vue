<script setup lang="ts">
import { RefreshCw } from "lucide-vue-next";
import { onMounted, onUnmounted, ref } from "vue";
import { documentationSource } from "./documentation";
import "./documentation.css";

type DocumentLocation = {
  type: "breakfix:document-location";
  source: string;
  version: string;
  locale: string;
  path: string;
  hash: string;
};

const frame = ref<HTMLIFrameElement>();
const loading = ref(true);
const failed = ref(false);
const current = ref<{ path: string; hash: string }>({ path: documentationSource.entryPath, hash: "" });
const frameSrc = ref("");
const reloadKey = ref(0);

const normalizedOrigin = new URL(documentationSource.origin).origin;

function validPath(path: unknown): path is string {
  return typeof path === "string" && (path === documentationSource.entryPath.slice(0, -1) || path.startsWith(documentationSource.entryPath));
}

function normalizedHash(value: unknown): string | null {
  if (value === "") return "";
  if (typeof value !== "string") return null;
  const hash = value.startsWith("#") ? value : `#${value}`;
  return /^#[^\s]*$/.test(hash) ? hash : null;
}

function readLocation() {
  const params = new URLSearchParams(window.location.search);
  const source = params.get("source");
  const version = params.get("version");
  const path = params.get("path");
  const hashValue = params.get("hash") || "";
  const hash = normalizedHash(hashValue);
  if (source === documentationSource.source && version === documentationSource.version && validPath(path) && hash !== null) {
    current.value = { path, hash };
  } else {
    current.value = { path: documentationSource.entryPath, hash: "" };
    replaceReaderUrl();
  }
  frameSrc.value = iframeUrl();
}

function readerUrl() {
  const params = new URLSearchParams({ source: documentationSource.source, version: documentationSource.version, path: current.value.path });
  if (current.value.hash) params.set("hash", current.value.hash.slice(1));
  return `/documentation?${params.toString()}`;
}

function replaceReaderUrl() {
  window.history.replaceState({ documentation: true }, "", readerUrl());
}

function iframeUrl() {
  return `${normalizedOrigin}${current.value.path}${current.value.hash}`;
}

function isDocumentLocation(value: unknown): value is DocumentLocation {
  if (!value || typeof value !== "object") return false;
  const message = value as Partial<DocumentLocation>;
  return message.type === "breakfix:document-location" &&
    message.source === documentationSource.source &&
    message.version === documentationSource.version &&
    message.locale === documentationSource.locale &&
    validPath(message.path) &&
    typeof message.hash === "string" &&
    (message.hash === "" || /^#[^\s]*$/.test(message.hash));
}

function handleMessage(event: MessageEvent) {
  if (event.origin !== normalizedOrigin) {
    if (import.meta.env.DEV) console.debug("[documentation] ignored message from unexpected origin", event.origin);
    return;
  }
  if (event.source !== frame.value?.contentWindow) {
    if (import.meta.env.DEV) console.debug("[documentation] ignored message from unexpected window");
    return;
  }
  if (!isDocumentLocation(event.data)) {
    if (import.meta.env.DEV) console.debug("[documentation] ignored malformed message");
    return;
  }
  current.value = { path: event.data.path, hash: event.data.hash };
  replaceReaderUrl();
}

function handleLoad() {
  loading.value = false;
  failed.value = false;
}

function handleError() {
  loading.value = false;
  failed.value = true;
}

function retry() {
  loading.value = true;
  failed.value = false;
  reloadKey.value += 1;
}

function handlePopState() {
  readLocation();
  loading.value = true;
  failed.value = false;
  reloadKey.value += 1;
}

readLocation();

onMounted(() => {
  window.addEventListener("message", handleMessage);
  window.addEventListener("popstate", handlePopState);
});
onUnmounted(() => {
  window.removeEventListener("message", handleMessage);
  window.removeEventListener("popstate", handlePopState);
});
</script>

<template>
  <section class="documentation-page" aria-label="Kubernetes documentation">
    <div class="documentation-frame-wrap">
      <div v-if="loading" class="documentation-state">Loading documentation...</div>
      <div v-if="failed" class="documentation-state documentation-state-error">
        <strong>Documentation is unavailable.</strong>
        <button class="compact-button" type="button" @click="retry"><RefreshCw :size="14" aria-hidden="true" /> Retry</button>
      </div>
      <iframe
        :key="reloadKey"
        ref="frame"
        class="documentation-frame"
        :src="frameSrc"
        title="Kubernetes documentation"
        :aria-busy="loading"
        @load="handleLoad"
        @error="handleError"
      />
    </div>
  </section>
</template>
