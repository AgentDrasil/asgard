<script setup lang="ts">
import { ref, computed, watch, nextTick, onMounted, onUnmounted } from "vue";
import { Icon } from "@iconify/vue";
import { getFileContent, getRawFileContentUrl } from "../../lib/api";
import { useShiki } from "../../composables/useShiki";
import { useShortcuts } from "../../composables/useShortcuts";
import { useInPageFind } from "../../composables/useInPageFind";
import { commentKey } from "../../utils/commentUtils";
import {
  mapExtToLang,
  escapeHtml,
  extractHighlightedLines,
  resolveViewerCategory,
  isCsvFile,
} from "../../utils/fileUtils";
import MarkdownContent from "../MarkdownContent.vue";
import FindBar from "../FindBar.vue";
import MediaViewer from "../common/MediaViewer.vue";
import CsvViewer from "../common/CsvViewer.vue";
import A2UIRenderer from "../a2ui/A2UIRenderer.vue";
import { isA2UIManifest, parseA2UIManifest } from "../../utils/a2uiUtils";
import type { CommentEntry, WorkspaceFileContent } from "../../types";

const props = defineProps<{
  sessionId: string;
  filePath: string | null;
  scope?: "workspace" | "tmp" | string;
  comments: Map<string, CommentEntry>;
}>();

const emit = defineEmits<{
  (e: "add-comment", comment: CommentEntry): void;
  (e: "delete-comment", key: string): void;
  (e: "clear-comments"): void;
  (e: "open-search"): void;
  (e: "file-loaded", data: WorkspaceFileContent | null): void;
}>();

const { findShortcut, matchShortcut } = useShortcuts();
const { highlightToHtml } = useShiki();

const fileData = ref<WorkspaceFileContent | null>(null);
const isLoading = ref(false);
const errorMessage = ref("");
const markdownMode = ref<"preview" | "source">("preview");
const csvMode = ref<"table" | "source">("table");
const a2uiMode = ref<"dashboard" | "source">("dashboard");

// Out-of-order response guard
let reqSequence = 0;

// Line comment widget state
const activeLine = ref<number | null>(null);
const widgetInput = ref("");
const textareaRef = ref<HTMLTextAreaElement | null>(null);

const isA2UI = computed(() => {
  if (!fileData.value) return false;
  return isA2UIManifest(fileData.value.name, fileData.value.path, fileData.value.content);
});

const parsedA2UIManifest = computed(() => {
  if (!isA2UI.value || !fileData.value?.content) return null;
  return parseA2UIManifest(fileData.value.content);
});

const isMarkdownFile = computed(() => {
  if (!fileData.value) return false;
  const ext = (fileData.value.ext || "").toLowerCase();
  return ext === "md" || ext === "markdown";
});

const isCsv = computed(() => {
  if (!fileData.value) return false;
  return isCsvFile(fileData.value.ext, fileData.value.path);
});

const viewerCategory = computed<
  "image" | "video" | "audio" | "pdf" | "csv" | "binary" | "markdown" | "code"
>(() => {
  return resolveViewerCategory(fileData.value);
});

const isMedia = computed(() => {
  const cat = viewerCategory.value;
  return cat === "image" || cat === "video" || cat === "audio" || cat === "pdf";
});

const isBinaryOnly = computed(() => {
  return viewerCategory.value === "binary" || (!isMedia.value && !!fileData.value?.isBinary);
});

const rawUrl = computed(() => {
  if (!props.sessionId || !fileData.value?.path) return "";
  return getRawFileContentUrl(props.sessionId, fileData.value.path);
});

const lines = computed<string[]>(() => {
  if (!fileData.value || fileData.value.isBinary || isMedia.value) return [];
  return fileData.value.content.split("\n");
});

const highlightedLines = computed<string[]>(() => {
  if (!fileData.value || fileData.value.isBinary || isMedia.value) return [];
  const content = fileData.value.content;
  const rawLines = lines.value;
  const lang = mapExtToLang(fileData.value.ext);

  const html = highlightToHtml(content, lang);
  if (!html) {
    return rawLines.map((l) => escapeHtml(l));
  }

  const extracted = extractHighlightedLines(html, rawLines.length);
  if (extracted.length === rawLines.length) {
    return extracted;
  }

  return rawLines.map((l) => escapeHtml(l));
});

async function loadContent() {
  if (!props.sessionId || !props.filePath) {
    fileData.value = null;
    isLoading.value = false;
    errorMessage.value = "";
    activeLine.value = null;
    emit("file-loaded", null);
    return;
  }

  const currentReq = ++reqSequence;
  isLoading.value = true;
  errorMessage.value = "";
  activeLine.value = null;
  widgetInput.value = "";

  try {
    const data = await getFileContent(props.sessionId, props.filePath, props.scope);
    if (currentReq !== reqSequence) return;
    if (!data) {
      fileData.value = null;
      errorMessage.value = "Failed to load file content";
      emit("file-loaded", null);
    } else {
      fileData.value = data;
      // If markdown, default to preview
      if (data.ext === "md" || data.ext === "markdown") {
        markdownMode.value = "preview";
      }
      if (isCsvFile(data.ext, data.path)) {
        csvMode.value = "table";
      }
      if (isA2UIManifest(data.name, data.path, data.content)) {
        a2uiMode.value = "dashboard";
      }
      emit("file-loaded", data);
    }
  } catch (err: any) {
    if (currentReq !== reqSequence) return;
    fileData.value = null;
    errorMessage.value = err?.message || "Failed to load file content";
    emit("file-loaded", null);
  } finally {
    if (currentReq === reqSequence) {
      isLoading.value = false;
    }
  }
}

watch(
  () => [props.sessionId, props.filePath, props.scope],
  () => {
    loadContent();
  },
  { immediate: true },
);

defineExpose({
  loadContent,
});

function hasComment(lineNum: number): boolean {
  if (!props.filePath) return false;
  return props.comments.has(commentKey(props.filePath, lineNum));
}

function getExistingComment(lineNum: number): CommentEntry | undefined {
  if (!props.filePath) return undefined;
  return props.comments.get(commentKey(props.filePath, lineNum));
}

function toggleCommentWidget(lineNum: number) {
  if (activeLine.value === lineNum) {
    activeLine.value = null;
    widgetInput.value = "";
  } else {
    activeLine.value = lineNum;
    const existing = getExistingComment(lineNum);
    widgetInput.value = existing?.comment ?? "";
    nextTick(() => {
      if (typeof textareaRef.value?.focus === "function") {
        textareaRef.value.focus();
      }
    });
  }
}

function closeWidget() {
  activeLine.value = null;
  widgetInput.value = "";
}

function submitComment() {
  if (!activeLine.value || !props.filePath) return;
  const lineNum = activeLine.value;
  const lineContent = lines.value[lineNum - 1] ?? "";
  const commentText = widgetInput.value.trim();

  if (!commentText) {
    emit("delete-comment", commentKey(props.filePath, lineNum));
  } else {
    emit("add-comment", {
      filePath: props.filePath,
      lineNumber: lineNum,
      lineContent,
      comment: commentText,
    });
  }
  closeWidget();
}

function handleDeleteComment(lineNum: number) {
  if (!props.filePath) return;
  emit("delete-comment", commentKey(props.filePath, lineNum));
  closeWidget();
}

const codeContainerRef = ref<HTMLElement | null>(null);
const findState = useInPageFind(codeContainerRef);

const handleGlobalKeydown = (e: KeyboardEvent) => {
  if (
    isMedia.value ||
    isBinaryOnly.value ||
    (isCsv.value && csvMode.value === "table") ||
    (isA2UI.value && a2uiMode.value === "dashboard")
  ) {
    return;
  }

  if (matchShortcut(e, "find")) {
    e.preventDefault();
    e.stopPropagation();
    findState.open();
  }
};

onMounted(() => {
  window.addEventListener("keydown", handleGlobalKeydown);
});

onUnmounted(() => {
  window.removeEventListener("keydown", handleGlobalKeydown);
});

// Watch fileData, markdownMode, csvMode, and a2uiMode to re-run or clear find highlights
watch([() => fileData.value, markdownMode, csvMode, a2uiMode], () => {
  if (
    findState.isOpen.value &&
    findState.query.value.trim() &&
    !isMedia.value &&
    !isBinaryOnly.value &&
    (!isCsv.value || csvMode.value === "source") &&
    (!isA2UI.value || a2uiMode.value === "source")
  ) {
    nextTick(() => {
      findState.performSearch();
    });
  } else {
    findState.clearHighlights();
  }
});
</script>

<template>
  <div class="flex-1 flex flex-col h-full overflow-hidden bg-base-100 min-w-0 relative">
    <!-- Subheader / Toolbar for Code Viewer -->
    <div
      v-if="fileData && !isMedia && !isBinaryOnly"
      class="px-3 py-1.5 bg-base-200/60 border-b border-base-300 flex items-center justify-end text-xs font-mono shrink-0 gap-2"
    >
      <div class="flex items-center gap-1.5 shrink-0">
        <!-- A2UI Dashboard Toggle -->
        <div
          v-if="isA2UI && parsedA2UIManifest"
          class="join bg-base-300/60 p-0.5 rounded-lg shrink-0"
        >
          <button
            @click="a2uiMode = 'dashboard'"
            :class="[
              'join-item btn btn-xs border-none font-medium gap-1 text-[11px]',
              a2uiMode === 'dashboard'
                ? 'btn-primary shadow-xs'
                : 'btn-ghost text-base-content/70 hover:text-base-content',
            ]"
            title="A2UI Dashboard Preview"
          >
            <Icon icon="material-symbols:dashboard-customize-outline" class="h-3 w-3" />
            <span>Dashboard</span>
          </button>
          <button
            @click="a2uiMode = 'source'"
            :class="[
              'join-item btn btn-xs border-none font-medium gap-1 text-[11px]',
              a2uiMode === 'source'
                ? 'btn-primary shadow-xs'
                : 'btn-ghost text-base-content/70 hover:text-base-content',
            ]"
            title="Raw JSON Source"
          >
            <Icon icon="octicon:code-24" class="h-3 w-3" />
            <span>Source</span>
          </button>
        </div>

        <!-- Markdown Preview Toggle (if markdown file) -->
        <div v-if="isMarkdownFile" class="join bg-base-300/60 p-0.5 rounded-lg shrink-0">
          <button
            @click="markdownMode = 'preview'"
            :class="[
              'join-item btn btn-xs border-none font-medium gap-1 text-[11px]',
              markdownMode === 'preview'
                ? 'btn-primary shadow-xs'
                : 'btn-ghost text-base-content/70 hover:text-base-content',
            ]"
          >
            <Icon icon="octicon:markdown-24" class="h-3 w-3" />
            <span>Preview</span>
          </button>
          <button
            @click="markdownMode = 'source'"
            :class="[
              'join-item btn btn-xs border-none font-medium gap-1 text-[11px]',
              markdownMode === 'source'
                ? 'btn-primary shadow-xs'
                : 'btn-ghost text-base-content/70 hover:text-base-content',
            ]"
          >
            <Icon icon="octicon:code-24" class="h-3 w-3" />
            <span>Source</span>
          </button>
        </div>

        <!-- CSV Preview Toggle (if CSV file) -->
        <div v-if="isCsv" class="join bg-base-300/60 p-0.5 rounded-lg shrink-0">
          <button
            @click="csvMode = 'table'"
            :class="[
              'join-item btn btn-xs border-none font-medium gap-1 text-[11px]',
              csvMode === 'table'
                ? 'btn-primary shadow-xs'
                : 'btn-ghost text-base-content/70 hover:text-base-content',
            ]"
          >
            <Icon icon="octicon:table-24" class="h-3 w-3" />
            <span>Table</span>
          </button>
          <button
            @click="csvMode = 'source'"
            :class="[
              'join-item btn btn-xs border-none font-medium gap-1 text-[11px]',
              csvMode === 'source'
                ? 'btn-primary shadow-xs'
                : 'btn-ghost text-base-content/70 hover:text-base-content',
            ]"
          >
            <Icon icon="octicon:code-24" class="h-3 w-3" />
            <span>Source</span>
          </button>
        </div>

        <!-- Find in File Button (hidden when table or dashboard view is active) -->
        <button
          v-if="(!isCsv || csvMode === 'source') && (!isA2UI || a2uiMode === 'source')"
          @click="findState.toggle()"
          class="btn btn-xs border-none font-medium gap-1 text-[11px]"
          :class="
            findState.isOpen.value
              ? 'btn-primary shadow-xs'
              : 'btn-ghost text-base-content/70 hover:text-base-content bg-base-300/60'
          "
          :title="`Find in file (${findShortcut})`"
        >
          <Icon icon="material-symbols:search" class="h-3.5 w-3.5" />
          <span class="hidden sm:inline">Find</span>
        </button>
      </div>
    </div>

    <!-- Floating In-Page Find Bar -->
    <FindBar
      v-if="
        !isMedia &&
        !isBinaryOnly &&
        (!isCsv || csvMode === 'source') &&
        (!isA2UI || a2uiMode === 'source')
      "
      v-model="findState.query.value"
      :isOpen="findState.isOpen.value"
      :currentIndex="findState.currentIndex.value"
      :totalMatches="findState.totalMatches.value"
      @next="findState.findNext"
      @prev="findState.findPrev"
      @close="findState.close"
    />

    <!-- Main Content Area -->
    <div ref="codeContainerRef" class="flex-1 overflow-auto min-w-0 relative">
      <!-- Loading State -->
      <div
        v-if="isLoading"
        class="flex items-center justify-center h-full text-base-content/50 gap-3"
      >
        <span class="loading loading-ring loading-md text-primary"></span>
        <span class="text-sm">Loading file...</span>
      </div>

      <!-- Error State -->
      <div v-else-if="errorMessage" class="flex items-center justify-center h-full p-8">
        <div class="alert alert-error max-w-md text-xs">
          <Icon icon="mynaui:danger" class="h-5 w-5 shrink-0" />
          <span>{{ errorMessage }}</span>
        </div>
      </div>

      <!-- Empty State (No File Selected) -->
      <div
        v-else-if="!filePath"
        class="flex flex-col items-center justify-center h-full gap-3 text-base-content/40 p-6 text-center"
      >
        <Icon icon="octicon:file-code-24" class="h-12 w-12 text-base-content/20" />
        <p class="text-sm font-medium text-base-content/80">No file selected</p>
        <p class="text-xs max-w-xs text-base-content/60">
          Select a file from the explorer sidebar or search across the workspace.
        </p>
        <button
          @click="emit('open-search')"
          class="btn btn-sm btn-outline gap-1.5 text-xs font-semibold mt-2 hover:btn-primary"
        >
          <Icon icon="material-symbols:search" class="h-3.5 w-3.5" />
          <span>Search Files</span>
        </button>
      </div>

      <!-- Media Viewer Mode (Image / Video / Audio / PDF) -->
      <MediaViewer
        v-else-if="
          (viewerCategory === 'image' ||
            viewerCategory === 'video' ||
            viewerCategory === 'audio' ||
            viewerCategory === 'pdf') &&
          fileData
        "
        :src="rawUrl"
        :fileName="fileData.name"
        :fileExt="fileData.ext"
        :fileSize="fileData.size"
        :mediaCategory="viewerCategory"
      />

      <!-- Binary File Fallback State -->
      <MediaViewer
        v-else-if="isBinaryOnly && fileData"
        src=""
        :fileName="fileData.name"
        :fileExt="fileData.ext"
        :fileSize="fileData.size"
        mediaCategory="binary"
      />

      <!-- A2UI Dashboard Preview Mode -->
      <div
        v-else-if="isA2UI && parsedA2UIManifest && a2uiMode === 'dashboard' && fileData"
        class="p-4 sm:p-6 max-w-7xl mx-auto overflow-y-auto w-full h-full"
      >
        <A2UIRenderer
          :manifest="parsedA2UIManifest"
          :sessionId="sessionId"
          :manifestPath="filePath"
        />
      </div>

      <!-- CSV Table Preview Mode -->
      <CsvViewer
        v-else-if="isCsv && csvMode === 'table' && fileData"
        :content="fileData.content"
        :fileName="fileData.name"
      />

      <!-- Markdown Preview Mode -->
      <div
        v-else-if="isMarkdownFile && markdownMode === 'preview' && fileData"
        class="p-6 max-w-4xl mx-auto overflow-y-auto"
      >
        <MarkdownContent :content="fileData.content" />
      </div>

      <!-- Syntax Highlighted Code Viewer with Line Numbers & Comments -->
      <div v-else-if="fileData" class="min-w-fit w-full">
        <table class="w-full border-collapse font-mono text-xs select-text">
          <tbody>
            <template v-for="(line, idx) in lines" :key="idx">
              <!-- Code Line Row -->
              <tr
                :class="[
                  'group transition-colors',
                  hasComment(idx + 1) ? 'bg-warning/10' : 'hover:bg-base-200/50',
                ]"
              >
                <!-- Line Number Gutter -->
                <td
                  class="w-12 px-2 py-0.5 text-right select-none text-base-content/40 hover:text-primary cursor-pointer border-r border-base-300 align-top group-hover:bg-base-200/80 shrink-0"
                  @click="toggleCommentWidget(idx + 1)"
                  :title="`Click to comment on line ${idx + 1}`"
                >
                  <div class="flex items-center justify-end gap-1">
                    <Icon
                      v-if="hasComment(idx + 1)"
                      icon="material-symbols:chat-bubble-outline"
                      class="h-3 w-3 text-warning shrink-0"
                    />
                    <span class="font-mono text-[11px]">{{ idx + 1 }}</span>
                  </div>
                </td>

                <!-- Code Text -->
                <td
                  class="px-3 py-0.5 whitespace-pre font-mono text-xs align-top text-base-content overflow-x-visible"
                >
                  <!-- eslint-disable-next-line vue/no-v-html -->
                  <span v-html="highlightedLines[idx] || '&nbsp;'"></span>
                </td>
              </tr>

              <!-- Inline Line Comment Widget -->
              <tr v-if="activeLine === idx + 1">
                <td colspan="2" class="p-0 border-y border-primary/40 bg-base-200/80">
                  <div
                    class="border border-primary/30 bg-base-200 rounded-lg mx-3 my-2 shadow-xl overflow-hidden"
                  >
                    <!-- Widget Header -->
                    <div
                      class="flex items-center justify-between px-3 py-1.5 bg-base-300/70 border-b border-base-300"
                    >
                      <div
                        class="flex items-center gap-1.5 text-xs font-semibold text-base-content/80"
                      >
                        <Icon
                          icon="material-symbols:chat-bubble-outline"
                          class="h-3.5 w-3.5 text-primary"
                        />
                        <span>Comment · {{ fileData.name }} · line {{ activeLine }}</span>
                      </div>
                      <button
                        @click="closeWidget"
                        class="btn btn-ghost btn-xs btn-square text-base-content/50 hover:text-base-content"
                      >
                        <Icon icon="mynaui:x" class="h-4 w-4" />
                      </button>
                    </div>

                    <!-- Line Preview -->
                    <div class="px-3 py-1.5 border-b border-base-300/60 bg-base-300/20">
                      <pre
                        class="text-xs font-mono text-base-content/70 whitespace-pre-wrap break-words"
                        >{{ line }}</pre>
                    </div>

                    <!-- Textarea Form -->
                    <div class="p-2.5 space-y-2">
                      <textarea
                        ref="textareaRef"
                        v-model="widgetInput"
                        placeholder="Add a comment… it will appear in the chat input"
                        rows="3"
                        class="textarea textarea-bordered bg-base-100 text-base-content w-full text-xs font-sans resize-none focus:outline-none focus:border-primary"
                        @keydown.ctrl.enter.prevent="submitComment"
                        autofocus
                      ></textarea>
                      <div class="flex items-center justify-between gap-2">
                        <button
                          v-if="hasComment(activeLine)"
                          @click="handleDeleteComment(activeLine)"
                          class="btn btn-ghost btn-xs text-error hover:bg-error/10 gap-1"
                        >
                          <Icon icon="mynaui:trash-one" class="h-3.5 w-3.5" />
                          Delete
                        </button>
                        <div class="flex gap-2 ml-auto">
                          <button @click="closeWidget" class="btn btn-ghost btn-xs">Cancel</button>
                          <button
                            @click="submitComment"
                            :disabled="!widgetInput.trim()"
                            class="btn btn-primary btn-xs gap-1 shadow-xs"
                          >
                            <Icon icon="material-symbols:add" class="h-3.5 w-3.5" />
                            Add to Chat
                          </button>
                        </div>
                      </div>
                    </div>
                  </div>
                </td>
              </tr>
            </template>
          </tbody>
        </table>
      </div>
    </div>

    <!-- Comment Summary Bar at Bottom -->
    <div
      v-if="comments.size > 0"
      class="shrink-0 px-3 py-2 bg-warning/10 border-t border-warning/20 flex items-center gap-2 flex-wrap text-xs"
    >
      <Icon icon="material-symbols:chat-bubble-outline" class="h-4 w-4 text-warning shrink-0" />
      <span class="text-warning font-medium shrink-0">
        {{ comments.size }} comment{{ comments.size > 1 ? "s" : "" }} in chat input
      </span>
      <div class="flex items-center gap-1 flex-wrap flex-1 min-w-0">
        <span
          v-for="[key, entry] in Array.from(comments.entries())"
          :key="key"
          class="badge badge-xs badge-warning gap-1 cursor-pointer hover:badge-error transition-colors"
          @click="emit('delete-comment', key)"
          :title="`${entry.filePath}:${entry.lineNumber} — click to remove`"
        >
          {{ entry.filePath.split("/").pop() }}:{{ entry.lineNumber }}
          <Icon icon="mynaui:x" class="h-2.5 w-2.5" />
        </span>
      </div>
      <button
        @click="emit('clear-comments')"
        class="btn btn-ghost btn-xs text-warning/70 hover:text-error ml-auto shrink-0"
        title="Clear all comments"
      >
        Clear all
      </button>
    </div>
  </div>
</template>
