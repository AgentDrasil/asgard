<script setup lang="ts">
import { ref, watch, computed } from "vue";
import { Icon } from "@iconify/vue";
import { marked } from "marked";
import DOMPurify from "dompurify";
import { useShiki } from "../composables/useShiki";

const { highlightBlock, highlightHtmlCodeBlocks } = useShiki();

marked.setOptions({
  gfm: true,
  breaks: true,
});

const MARKDOWN_BLOCK_CLASSES = [
  "rounded-lg",
  "p-4",
  "overflow-x-auto",
  "my-2",
  "border",
  "border-base-300",
  "text-xs",
  "font-mono",
] as const;

const MARKDOWN_PROSE_CLASSES = [
  "font-sans",
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
].join(" ");

const props = defineProps<{
  sessionId: string;
  activeFilePath: string | null;
  modifiedFiles: string[];
}>();

const emit = defineEmits<{
  (e: "close"): void;
  (e: "select-file", path: string): void;
}>();

interface FileData {
  path: string;
  name: string;
  ext: string;
  size: number;
  content: string;
  updatedAt: string;
}

const fileData = ref<FileData | null>(null);
const loading = ref(false);
const errorMsg = ref<string | null>(null);

const isMarkdown = computed(() => {
  if (!fileData.value) return false;
  const ext = fileData.value.ext.toLowerCase();
  return ext === "md" || ext === "markdown";
});

const highlightedArtifactContent = computed(() => {
  if (!fileData.value || !fileData.value.content) return "";
  const ext = fileData.value.ext.toLowerCase();
  const lang = isMarkdown.value ? "markdown" : ext || "text";
  const highlighted = highlightBlock(fileData.value.content, lang, [
    "rounded-lg",
    "p-4",
    "whitespace-pre-wrap",
    "break-words",
    "border",
    "border-base-300",
    "text-xs",
    "font-mono",
    "leading-relaxed",
  ]);
  if (highlighted) return highlighted;
  // Fallback while Shiki loads or when it fails: keep the same wrapping so the
  // layout doesn't jump once highlighting kicks in.
  return `<pre class="font-mono text-xs text-base-content bg-base-200/80 p-4 rounded-lg border border-base-300 leading-relaxed whitespace-pre-wrap break-words"><code>${DOMPurify.sanitize(fileData.value.content)}</code></pre>`;
});

const renderedMarkdownContent = computed(() => {
  if (!fileData.value || !fileData.value.content) return "";
  try {
    const rawHtml = marked.parse(fileData.value.content) as string;
    const sanitized = DOMPurify.sanitize(rawHtml);
    return highlightHtmlCodeBlocks(sanitized, [...MARKDOWN_BLOCK_CLASSES]);
  } catch {
    return DOMPurify.sanitize(fileData.value.content);
  }
});

function formatPath(path: string): string {
  if (!path) return "";
  return path.replace(/^\/home\/[^/]+/, "~");
}

function getFileIcon(path: string) {
  const ext = path.split(".").pop()?.toLowerCase() || "";
  switch (ext) {
    case "md":
    case "markdown":
      return "octicon:markdown-24";
    case "go":
      return "vscode-icons:file-type-go";
    case "ts":
    case "tsx":
      return "vscode-icons:file-type-typescript";
    case "js":
    case "jsx":
      return "vscode-icons:file-type-js";
    case "json":
      return "vscode-icons:file-type-json";
    case "css":
    case "html":
      return "vscode-icons:file-type-html";
    default:
      return "octicon:file-code-24";
  }
}

async function fetchFile(path: string) {
  if (!path || !props.sessionId) return;
  loading.value = true;
  errorMsg.value = null;

  try {
    const res = await fetch(
      `/api/v1/workspace/file?session_id=${encodeURIComponent(props.sessionId)}&path=${encodeURIComponent(path)}`,
    );
    if (!res.ok) {
      const errJson = await res.json().catch(() => ({}));
      throw new Error(errJson.error || `HTTP ${res.status}`);
    }
    const data: FileData = await res.json();
    fileData.value = data;
  } catch (err: any) {
    errorMsg.value = err.message || "Failed to load file";
    fileData.value = null;
  } finally {
    loading.value = false;
  }
}

watch(
  [() => props.activeFilePath, () => props.sessionId],
  ([newPath]) => {
    if (newPath) {
      fetchFile(newPath);
    } else {
      fileData.value = null;
    }
  },
  { immediate: true },
);

function onFileSelectChange(event: Event) {
  const target = event.target as HTMLSelectElement;
  if (target && target.value) {
    emit("select-file", target.value);
  }
}
</script>

<template>
  <div
    class="h-full flex flex-col bg-base-100 border-l border-base-300 text-base-content select-text overflow-hidden shadow-2xl"
  >
    <!-- Header & Dropdown File Selection Bar -->
    <div
      class="flex items-center justify-between px-3 py-2.5 sm:px-6 sm:py-3.5 bg-base-200 border-b border-base-300 shrink-0 shadow-sm min-h-[52px]"
    >
      <div class="flex items-center gap-2 overflow-hidden flex-1 min-w-0 mr-2">
        <!-- Mobile Back Button -->
        <button
          @click="emit('close')"
          class="md:hidden btn btn-sm btn-ghost btn-square text-base-content/80 hover:text-base-content shrink-0"
          title="Back to Chat"
        >
          <Icon icon="material-symbols:arrow-back-ios-rounded" class="h-4 w-4 ml-1" />
        </button>

        <span
          class="hidden sm:flex text-[11px] font-bold uppercase tracking-wider text-emerald-500 bg-emerald-500/10 border border-emerald-500/30 px-2 h-8 rounded items-center gap-1.5 shrink-0"
        >
          Artifacts
        </span>

        <!-- Dropdown Selector for Files -->
        <div class="relative flex-1 min-w-0">
          <div class="relative flex items-center w-full">
            <Icon
              :icon="getFileIcon(activeFilePath || '')"
              class="absolute left-2.5 h-4 w-4 shrink-0 text-emerald-500 pointer-events-none z-10"
            />
            <select
              :value="activeFilePath || ''"
              @change="onFileSelectChange"
              class="select select-sm select-bordered w-full pl-8 pr-8 font-mono text-xs text-base-content bg-base-100 focus:outline-none focus:border-emerald-500 truncate"
              title="Select file to preview"
            >
              <option v-for="file in modifiedFiles" :key="file" :value="file">
                {{ formatPath(file) }}
              </option>
            </select>
          </div>
        </div>
      </div>

      <div class="flex items-center gap-1.5 shrink-0">
        <button
          @click="fetchFile(activeFilePath || '')"
          class="p-1.5 text-xs rounded bg-base-300 hover:bg-base-300/80 text-base-content transition-colors border border-base-300"
          title="Refresh File Content"
        >
          <Icon icon="octicon:sync-24" class="h-3.5 w-3.5" />
        </button>
      </div>
    </div>

    <!-- Content Preview Area -->
    <div class="flex-1 overflow-y-auto p-3 sm:p-4 relative bg-base-100">
      <div
        v-if="loading"
        class="flex items-center justify-center h-full text-base-content/60 text-sm gap-2"
      >
        <div
          class="w-4 h-4 border-2 border-emerald-500 border-t-transparent rounded-full animate-spin"
        ></div>
        <span>Loading file content...</span>
      </div>

      <div
        v-else-if="errorMsg"
        class="flex flex-col items-center justify-center h-full text-error text-sm gap-2 p-6 text-center"
      >
        <span>⚠️ {{ errorMsg }}</span>
        <button
          @click="fetchFile(activeFilePath || '')"
          class="mt-2 px-3 py-1 bg-base-300 text-base-content text-xs rounded hover:bg-base-300/80"
        >
          Retry
        </button>
      </div>

      <div v-else-if="fileData" class="h-full">
        <div
          v-if="isMarkdown"
          v-html="renderedMarkdownContent"
          :class="MARKDOWN_PROSE_CLASSES"
          class="w-full h-full min-w-0"
        ></div>
        <div v-else v-html="highlightedArtifactContent" class="w-full h-full min-w-0"></div>
      </div>

      <div v-else class="flex items-center justify-center h-full text-base-content/50 text-sm">
        Select a file to preview
      </div>
    </div>
  </div>
</template>
