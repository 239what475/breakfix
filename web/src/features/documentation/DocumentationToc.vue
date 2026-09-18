<script setup lang="ts">
import { ChevronDown, ChevronRight } from "lucide-vue-next";

export type TreeEntry = {
  title: string;
  path: string;
  hasChildren: boolean;
  children?: TreeEntry[];
};

defineProps<{
  entries: TreeEntry[];
  expanded: Set<string>;
  currentPath: string;
}>();

const emit = defineEmits<{
  toggle: [entry: TreeEntry];
  open: [entry: TreeEntry];
}>();
</script>

<template>
  <ul class="documentation-toc-list">
    <li v-for="entry in entries" :key="entry.path">
      <div class="documentation-toc-row" :class="{ active: currentPath === entry.path }">
        <button
          v-if="entry.hasChildren"
          class="documentation-toc-expand"
          type="button"
          :aria-expanded="expanded.has(entry.path)"
          :aria-label="`Toggle ${entry.title} section`"
          @click="emit('toggle', entry)"
        >
          <component :is="expanded.has(entry.path) ? ChevronDown : ChevronRight" :size="13" aria-hidden="true" />
        </button>
        <span v-else class="documentation-toc-leaf" aria-hidden="true"></span>
        <button class="documentation-toc-link" type="button" @click="emit('open', entry)">{{ entry.title }}</button>
      </div>
      <DocumentationToc
        v-if="entry.children && entry.children.length"
        v-show="expanded.has(entry.path)"
        :entries="entry.children"
        :expanded="expanded"
        :current-path="currentPath"
        @toggle="(child) => emit('toggle', child)"
        @open="(child) => emit('open', child)"
      />
    </li>
  </ul>
</template>
