<script setup lang="ts">
import { computed } from "vue";

const props = defineProps<{ source: string }>();
type Block = {
  kind: "heading" | "paragraph" | "list" | "code";
  value: string;
  level?: number;
  items?: string[];
};

const blocks = computed<Block[]>(() => {
  const lines = props.source.replace(/\r/g, "").split("\n");
  const result: Block[] = [];
  let code: string[] | null = null;
  let list: string[] | null = null;
  let paragraph: string[] = [];
  const flushParagraph = () => {
    if (paragraph.length)
      result.push({ kind: "paragraph", value: paragraph.join(" ") });
    paragraph = [];
  };
  const flushList = () => {
    if (list?.length) result.push({ kind: "list", value: "", items: list });
    list = null;
  };
  for (const line of lines) {
    if (line.startsWith("```")) {
      flushParagraph();
      flushList();
      if (code) {
        result.push({ kind: "code", value: code.join("\n") });
        code = null;
      } else code = [];
      continue;
    }
    if (code) {
      code.push(line);
      continue;
    }
    const heading = /^(#{1,3})\s+(.+)$/.exec(line);
    if (heading) {
      flushParagraph();
      flushList();
      result.push({
        kind: "heading",
        value: heading[2],
        level: heading[1].length,
      });
      continue;
    }
    const bullet = /^[-*]\s+(.+)$/.exec(line);
    if (bullet) {
      flushParagraph();
      (list ??= []).push(bullet[1]);
      continue;
    }
    if (!line.trim()) {
      flushParagraph();
      flushList();
      continue;
    }
    flushList();
    paragraph.push(line.trim());
  }
  flushParagraph();
  flushList();
  if (code) result.push({ kind: "code", value: code.join("\n") });
  return result;
});
</script>

<template>
  <article class="markdown-document">
    <template v-for="(block, index) in blocks" :key="index">
      <h1 v-if="block.kind === 'heading' && block.level === 1">
        {{ block.value }}
      </h1>
      <h2 v-else-if="block.kind === 'heading' && block.level === 2">
        {{ block.value }}
      </h2>
      <h3 v-else-if="block.kind === 'heading'">{{ block.value }}</h3>
      <p v-else-if="block.kind === 'paragraph'">{{ block.value }}</p>
      <ul v-else-if="block.kind === 'list'">
        <li v-for="item in block.items" :key="item">{{ item }}</li>
      </ul>
      <pre v-else><code>{{ block.value }}</code></pre>
    </template>
  </article>
</template>
