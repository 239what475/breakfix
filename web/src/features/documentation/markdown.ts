import MarkdownIt from "markdown-it";
import type Token from "markdown-it/lib/token.mjs";
import {
  createHighlighterCore,
  type HighlighterCore,
} from "shiki/core";
import { createOnigurumaEngine } from "shiki/engine/oniguruma";

export type DocumentAnchorReference = {
  id: string;
  level: number;
  title: string;
};

// The fence languages observed across the whole corpus: bare fences dominate
// and fall back to plain text; everything else is covered by these nine.
const fenceLanguages: Record<string, string> = {
  bash: "bash",
  sh: "bash",
  shell: "bash",
  console: "console",
  yaml: "yaml",
  yml: "yaml",
  json: "json",
  go: "go",
  powershell: "powershell",
  ps1: "powershell",
  toml: "toml",
  http: "http",
  markdown: "markdown",
  md: "markdown",
};

const alertKinds = new Set(["note", "caution", "warning", "tip", "important"]);

let highlighterPromise: Promise<HighlighterCore> | undefined;

function getHighlighter(): Promise<HighlighterCore> {
  highlighterPromise ??= createHighlighterCore({
    engine: createOnigurumaEngine(() => import("shiki/wasm")),
    themes: [import("shiki/themes/github-light.mjs")],
    langs: [
      import("shiki/langs/bash.mjs"),
      import("shiki/langs/console.mjs"),
      import("shiki/langs/yaml.mjs"),
      import("shiki/langs/json.mjs"),
      import("shiki/langs/go.mjs"),
      import("shiki/langs/powershell.mjs"),
      import("shiki/langs/toml.mjs"),
      import("shiki/langs/http.mjs"),
      import("shiki/langs/markdown.mjs"),
    ],
  }).catch((error) => {
    highlighterPromise = undefined;
    throw error;
  });
  return highlighterPromise;
}

function fenceHtml(code: string, language: string, highlighter: HighlighterCore): string {
  const mapped = fenceLanguages[language.toLowerCase()] ?? "text";
  try {
    return highlighter.codeToHtml(code, {
      lang: mapped,
      theme: "github-light",
    });
  } catch {
    // An unmapped grammar degrades to an escaped plain block; the page still
    // renders.
    return `<pre class="doc-code-plain"><code>${markdown.utils.escapeHtml(code)}</code></pre>`;
  }
}

const markdown = new MarkdownIt({ html: false, linkify: false, typographer: false });

// `> [!NOTE]`-style GitHub alerts become classed blockquotes; the alert title
// is provided by CSS so untrusted text never enters generated markup.
function applyAlertClasses(tokens: Token[]) {
  for (let index = 0; index < tokens.length - 2; index += 1) {
    const open = tokens[index];
    const paragraph = tokens[index + 1];
    const inline = tokens[index + 2];
    if (open.type !== "blockquote_open" || paragraph?.type !== "paragraph_open" || inline?.type !== "inline") continue;
    const match = /^\[!(note|caution|warning|tip|important)\]\s*/i.exec(inline.content);
    if (!match) continue;
    const kind = match[1].toLowerCase();
    if (!alertKinds.has(kind)) continue;
    inline.content = inline.content.slice(match[0].length);
    const first = inline.children?.[0];
    if (first?.type === "text") first.content = first.content.slice(match[0].length);
    open.attrJoin("class", "doc-alert");
    open.attrJoin("class", `doc-alert-${kind}`);
  }
}

// Heading ids come from the library anchors so navigation, scrolling sync, and
// the practice line's anchor entries share one identity.
function assignHeadingIds(tokens: Token[], anchors: DocumentAnchorReference[]) {
  let cursor = 0;
  for (let index = 0; index < tokens.length; index += 1) {
    const token = tokens[index];
    if (token.type !== "heading_open") continue;
    const level = Number.parseInt(token.tag.slice(1), 10);
    const inline = tokens[index + 1];
    const title = inline?.content?.trim() ?? "";
    for (let scan = cursor; scan < anchors.length; scan += 1) {
      if (anchors[scan].level === level && anchors[scan].title === title) {
        token.attrSet("id", anchors[scan].id);
        cursor = scan + 1;
        break;
      }
    }
  }
}

export async function renderDocumentMarkdown(source: string, anchors: DocumentAnchorReference[]): Promise<string> {
  const highlighter = await getHighlighter();
  markdown.options.highlight = (code, language) => fenceHtml(code, language, highlighter);
  const tokens = markdown.parse(source, {});
  applyAlertClasses(tokens);
  assignHeadingIds(tokens, anchors);
  return markdown.renderer.render(tokens, markdown.options, {});
}
