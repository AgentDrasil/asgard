<script setup lang="ts">
import { computed } from "vue";
import { marked } from "marked";
import MermaidViewer from "./MermaidViewer.vue";
import { useShiki } from "../composables/useShiki";
import { splitMarkdownTokens } from "../utils/markdownSegments";
import { sanitizeMarkdownHtml } from "../utils/markdownSanitize";

const props = withDefaults(
  defineProps<{
    content: string;
    customClass?: string;
  }>(),
  {
    customClass: "",
  },
);

const { highlightHtmlCodeBlocks } = useShiki();

const BLOCK_CLASSES = [
  "rounded-lg",
  "p-4",
  "overflow-x-auto",
  "my-2",
  "border",
  "border-base-300",
  "text-xs",
  "font-mono",
] as const;

const DEFAULT_PROSE_CLASSES = [
  "font-sans",
  "prose",
  "prose-sm",
  "max-w-none",
  "text-base-content",
  "leading-relaxed",
  "min-w-0",
  "break-words",
  "[word-break:break-word]",
  "[&_p]:mb-3",
  "[&_h1]:text-2xl",
  "[&_h1]:font-bold",
  "[&_h1]:mb-4",
  "[&_h1]:mt-6",
  "[&_h1]:pb-2",
  "[&_h1]:border-b",
  "[&_h1]:border-base-300",
  "[&_h2]:text-xl",
  "[&_h2]:font-bold",
  "[&_h2]:mb-3",
  "[&_h2]:mt-5",
  "[&_h2]:pb-1",
  "[&_h2]:border-b",
  "[&_h2]:border-base-300/60",
  "[&_h3]:text-lg",
  "[&_h3]:font-semibold",
  "[&_h3]:mb-2",
  "[&_h3]:mt-4",
  "[&_h4]:text-base",
  "[&_h4]:font-semibold",
  "[&_h4]:mb-2",
  "[&_h4]:mt-3",
  "[&_h5]:text-sm",
  "[&_h5]:font-semibold",
  "[&_h5]:mb-1",
  "[&_h5]:mt-2",
  "[&_h6]:text-xs",
  "[&_h6]:font-semibold",
  "[&_h6]:mb-1",
  "[&_h6]:mt-2",
  "[&_hr]:my-4",
  "[&_hr]:border-t",
  "[&_hr]:border-base-content/20",
  "[&_blockquote]:border-l-4",
  "[&_blockquote]:border-base-300",
  "[&_blockquote]:pl-4",
  "[&_blockquote]:my-3",
  "[&_blockquote]:italic",
  "[&_blockquote]:text-base-content/80",
  "[&_ul]:list-disc",
  "[&_ul]:ml-5",
  "[&_ul]:mb-3",
  "[&_ol]:list-decimal",
  "[&_ol]:ml-5",
  "[&_ol]:mb-3",
  "[&_li]:mb-1",
  "[&_a]:text-primary",
  "[&_a]:underline",
  "hover:[&_a]:text-primary-focus",
  "[&_table]:block",
  "[&_table]:overflow-x-auto",
  "[&_table]:max-w-full",
  "[&_table]:border-collapse",
  "[&_table]:my-3",
  "[&_th]:border",
  "[&_th]:border-base-300",
  "[&_th]:bg-base-200/80",
  "[&_th]:px-3",
  "[&_th]:py-2",
  "[&_th]:text-left",
  "[&_th]:font-semibold",
  "[&_td]:border",
  "[&_td]:border-base-300",
  "[&_td]:px-3",
  "[&_td]:py-2",
  "[&_pre]:bg-base-200/80",
  "[&_pre]:p-4",
  "[&_pre]:rounded-lg",
  "[&_pre]:border",
  "[&_pre]:border-base-300",
  "[&_pre]:overflow-x-auto",
  "[&_pre]:max-w-full",
  "[&:not(pre)>code]:bg-base-200/80",
  "[&:not(pre)>code]:px-1.5",
  "[&:not(pre)>code]:py-0.5",
  "[&:not(pre)>code]:rounded",
  "[&_code]:text-warning",
  "[&_code]:break-words",
  "[&_.katex-display]:my-3",
  "[&_.katex-display-wrapper]:overflow-x-auto",
].join(" ");

type ContentSegment = { type: "markdown"; html: string } | { type: "mermaid"; code: string };

const segments = computed<ContentSegment[]>(() => {
  if (!props.content) return [];

  const rawSegments = splitMarkdownTokens(props.content);
  return rawSegments.map((segment) => {
    if (segment.type === "mermaid") {
      return {
        type: "mermaid",
        code: segment.code,
      };
    }

    const tokensToParse = segment.tokens;
    if (segment.links) {
      (tokensToParse as unknown as { links: typeof segment.links }).links = segment.links;
    }
    const rawHtml = marked.parser(tokensToParse);
    const sanitized = sanitizeMarkdownHtml(rawHtml);
    const highlighted = highlightHtmlCodeBlocks(sanitized, [...BLOCK_CLASSES]);
    return {
      type: "markdown",
      html: highlighted,
    };
  });
});
</script>

<template>
  <div class="markdown-content-wrapper min-w-0 w-full">
    <template v-for="(segment, idx) in segments" :key="idx">
      <MermaidViewer v-if="segment.type === 'mermaid'" :code="segment.code" />
      <div v-else :class="[DEFAULT_PROSE_CLASSES, customClass]" v-html="segment.html" />
    </template>
  </div>
</template>
