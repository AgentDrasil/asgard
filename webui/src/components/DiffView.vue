<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted, watch } from "vue";
import { DiffView, DiffModeEnum, SplitSide } from "@git-diff-view/vue";
import { DiffFile } from "@git-diff-view/vue";
import "@git-diff-view/vue/styles/diff-view.css";
import { Icon } from "@iconify/vue";
import { getGitDiff } from "../lib/api";
import type { GitDiffFile } from "../types";
import { useShortcuts } from "../composables/useShortcuts";

const { toggleDiffShortcut } = useShortcuts();

const props = defineProps<{
  runDir: string;
  gitRoot: string;
  chatInputText: string;
}>();

const emit = defineEmits<{
  (e: "close"): void;
  (e: "update:chatInputText", val: string): void;
}>();

// ── Diff data ────────────────────────────────────────────────────────────────
const files = ref<GitDiffFile[]>([]);
const loading = ref(false);
const errorMsg = ref("");
const selectedIndex = ref(0);

const selectedFile = computed(() => files.value[selectedIndex.value] ?? null);

const diffFileObj = computed((): DiffFile | null => {
  const f = selectedFile.value;
  if (!f) return null;
  const df = DiffFile.createInstance({
    oldFile: { fileName: f.oldPath, content: f.oldContent },
    newFile: { fileName: f.newPath, content: f.newContent },
    hunks: f.hunks,
  });
  df.initTheme(theme.value);
  df.init();
  df.buildSplitDiffLines();
  df.buildUnifiedDiffLines();
  return df;
});

async function loadDiff() {
  loading.value = true;
  errorMsg.value = "";
  activeWidget.value = null;
  try {
    const result = await getGitDiff(props.runDir);
    files.value = result;
    selectedIndex.value = 0;
  } catch (e: any) {
    errorMsg.value = e?.message ?? "Failed to load diff";
  } finally {
    loading.value = false;
  }
}

onMounted(loadDiff);
watch(() => props.runDir, loadDiff);

// ── View mode ────────────────────────────────────────────────────────────────
// Default: Split on desktop, Unified on mobile
const isMobile = typeof window !== "undefined" && window.innerWidth < 768;
const viewMode = ref<DiffModeEnum>(isMobile ? DiffModeEnum.Unified : DiffModeEnum.Split);

// ── Theme ────────────────────────────────────────────────────────────────────
const theme = ref<"dark" | "light">((localStorage.getItem("theme") as "dark" | "light") ?? "dark");

const syncTheme = () => {
  const docTheme = document.documentElement.getAttribute("data-theme");
  theme.value = docTheme === "light" ? "light" : "dark";
};

const observer = new MutationObserver(syncTheme);
onMounted(() => {
  syncTheme();
  observer.observe(document.documentElement, { attributes: true, attributeFilter: ["data-theme"] });
});
onUnmounted(() => observer.disconnect());

// ── File selector (dropdown) ──────────────────────────────────────────
const showFileDropdown = ref(false);
const fileDropdownRef = ref<HTMLDivElement | null>(null);

const handleOutsideClick = (e: MouseEvent) => {
  if (fileDropdownRef.value && !fileDropdownRef.value.contains(e.target as Node)) {
    showFileDropdown.value = false;
  }
};
onMounted(() => document.addEventListener("click", handleOutsideClick, true));
onUnmounted(() => document.removeEventListener("click", handleOutsideClick, true));

// ── Line comments (in-memory) ────────────────────────────────────────────────
interface CommentEntry {
  filePath: string;
  side: SplitSide;
  lineNumber: number;
  lineContent: string;
  comment: string;
}

const comments = ref<Map<string, CommentEntry>>(new Map());

interface ActiveWidget {
  side: SplitSide;
  lineNumber: number;
}
const activeWidget = ref<ActiveWidget | null>(null);
const widgetInput = ref("");

function commentKey(filePath: string, side: SplitSide, lineNumber: number) {
  return `${filePath}:${side}:${lineNumber}`;
}

// Called by DiffView via @on-add-widget-click
function handleAddWidgetClick(lineNumber: number, side: SplitSide) {
  const key = commentKey(selectedFile.value?.newPath ?? "", side, lineNumber);
  const existing = comments.value.get(key);
  widgetInput.value = existing?.comment ?? "";
  if (activeWidget.value?.side === side && activeWidget.value?.lineNumber === lineNumber) {
    // Toggle off if clicking the same line again
    activeWidget.value = null;
    widgetInput.value = "";
  } else {
    activeWidget.value = { side, lineNumber };
  }
}

function closeWidget() {
  activeWidget.value = null;
  widgetInput.value = "";
}

function getLineContent(side: SplitSide, lineNumber: number): string {
  const f = selectedFile.value;
  if (!f) return "";
  const source = side === SplitSide.old ? f.oldContent : f.newContent;
  const lines = source.split("\n");
  return lines[lineNumber - 1] ?? "";
}

function sideName(side: SplitSide): string {
  return side === SplitSide.old ? "old" : "new";
}

function submitComment() {
  if (!activeWidget.value || !selectedFile.value) return;
  const { side, lineNumber } = activeWidget.value;
  const filePath = selectedFile.value.newPath;
  const key = commentKey(filePath, side, lineNumber);
  const lineContent = getLineContent(side, lineNumber);

  if (!widgetInput.value.trim()) {
    comments.value.delete(key);
  } else {
    comments.value.set(key, {
      filePath,
      side,
      lineNumber,
      lineContent,
      comment: widgetInput.value.trim(),
    });
  }

  rebuildChatInput();
  closeWidget();
}

function deleteComment(key: string) {
  comments.value.delete(key);
  rebuildChatInput();
}

function formatCommentBlock(entry: CommentEntry): string {
  return `${entry.filePath} line ${entry.lineNumber}\n${entry.lineContent}\n---\n\nuser comment:\n\n${entry.comment}\n---`;
}

function rebuildChatInput() {
  if (comments.value.size === 0) {
    emit("update:chatInputText", "");
    return;
  }
  const blocks = Array.from(comments.value.values()).map(formatCommentBlock);
  emit("update:chatInputText", blocks.join("\n\n"));
}

function hasComment(side: SplitSide, lineNumber: number): boolean {
  const filePath = selectedFile.value?.newPath ?? "";
  return comments.value.has(commentKey(filePath, side, lineNumber));
}

// File has any comments
function fileHasComments(filePath: string): boolean {
  return Array.from(comments.value.values()).some((c) => c.filePath === filePath);
}
</script>

<template>
  <div class="flex-1 flex flex-col h-full overflow-hidden bg-base-100 min-w-0">
    <!-- Header -->
    <header
      class="px-3 py-2 sm:px-4 sm:py-2.5 bg-base-200 border-b border-base-300 flex items-center gap-2 shrink-0 shadow-sm"
    >
      <!-- Title & Mobile Back Button -->
      <div class="flex items-center gap-2 min-w-0 flex-1">
        <!-- Mobile Back Button -->
        <button
          @click="emit('close')"
          class="sm:hidden btn btn-sm btn-ghost btn-square text-base-content/80 hover:text-base-content shrink-0"
          title="Back to Chat"
        >
          <Icon icon="material-symbols:arrow-back-ios-rounded" class="h-4 w-4 ml-1" />
        </button>
        <Icon icon="material-symbols:difference-outline" class="h-5 w-5 text-primary shrink-0" />
        <span class="text-sm font-bold text-base-content truncate">Git Diff</span>
      </div>

      <!-- Mode toggle (desktop only) -->
      <div class="hidden sm:flex items-center">
        <div class="join bg-base-300/60 p-0.5 rounded-lg">
          <button
            @click="viewMode = DiffModeEnum.Split"
            :class="[
              'join-item btn btn-xs border-none font-medium gap-1',
              viewMode === DiffModeEnum.Split
                ? 'btn-primary shadow-xs'
                : 'btn-ghost text-base-content/70 hover:text-base-content',
            ]"
            title="Side by Side"
          >
            <Icon icon="material-symbols:view-column-2-outline" class="h-3.5 w-3.5" />
            Split
          </button>
          <button
            @click="viewMode = DiffModeEnum.Unified"
            :class="[
              'join-item btn btn-xs border-none font-medium gap-1',
              viewMode === DiffModeEnum.Unified
                ? 'btn-primary shadow-xs'
                : 'btn-ghost text-base-content/70 hover:text-base-content',
            ]"
            title="Unified"
          >
            <Icon icon="material-symbols:view-stream-outline" class="h-3.5 w-3.5" />
            Unified
          </button>
        </div>
      </div>

      <!-- Refresh -->
      <button
        @click="loadDiff"
        :disabled="loading"
        class="btn btn-ghost btn-xs btn-square text-base-content/70 hover:text-base-content"
        title="Refresh diff"
      >
        <Icon icon="mynaui:refresh" :class="['h-4 w-4', { 'animate-spin': loading }]" />
      </button>

      <!-- Desktop Close -->
      <button
        @click="emit('close')"
        class="hidden sm:flex btn btn-ghost btn-xs btn-square text-base-content/70 hover:text-error"
        :title="`Close diff view and return to chat (${toggleDiffShortcut})`"
      >
        <Icon icon="mynaui:x" class="h-5 w-5" />
      </button>
    </header>

    <!-- File selector -->
    <div
      v-if="files.length > 0"
      class="px-2 py-1.5 bg-base-200/60 border-b border-base-300 shrink-0"
    >
      <!-- Dropdown selector -->
      <div class="relative" ref="fileDropdownRef">
        <button
          @click.stop="showFileDropdown = !showFileDropdown"
          class="btn btn-sm w-full justify-between font-mono text-xs text-left border border-base-300 bg-base-100 hover:bg-base-200"
        >
          <span class="flex items-center gap-2 truncate min-w-0">
            <span
              v-if="selectedFile && fileHasComments(selectedFile.newPath)"
              class="badge badge-xs badge-warning shrink-0"
              >💬</span
            >
            <span class="truncate">{{ selectedFile?.newPath ?? "Select file…" }}</span>
          </span>
          <Icon
            :icon="showFileDropdown ? 'ep:arrow-up' : 'ep:arrow-down'"
            class="h-4 w-4 shrink-0 ml-1"
          />
        </button>

        <!-- Dropdown list -->
        <div
          v-if="showFileDropdown"
          class="absolute top-full left-0 right-0 z-50 mt-1 bg-base-200 border border-base-300 rounded-lg shadow-xl overflow-hidden max-h-60 overflow-y-auto"
        >
          <button
            v-for="(f, idx) in files"
            :key="f.newPath"
            @click="
              selectedIndex = idx;
              activeWidget = null;
              showFileDropdown = false;
            "
            :class="[
              'w-full text-left px-3 py-2.5 font-mono text-xs flex items-center gap-2 hover:bg-base-300 transition-colors',
              selectedIndex === idx ? 'text-primary bg-primary/10' : 'text-base-content/80',
            ]"
          >
            <span v-if="fileHasComments(f.newPath)" class="badge badge-xs badge-warning shrink-0"
              >💬</span
            >
            <span class="truncate">{{ f.newPath }}</span>
            <Icon
              v-if="selectedIndex === idx"
              icon="material-symbols:check-rounded"
              class="h-3.5 w-3.5 ml-auto shrink-0 text-primary"
            />
          </button>
        </div>
      </div>
    </div>

    <!-- Main content -->
    <div class="flex-1 overflow-auto min-w-0 relative">
      <!-- Loading -->
      <div
        v-if="loading"
        class="flex items-center justify-center h-full text-base-content/50 gap-3"
      >
        <span class="loading loading-ring loading-md text-primary"></span>
        <span class="text-sm">Loading diff...</span>
      </div>

      <!-- Error -->
      <div v-else-if="errorMsg" class="flex items-center justify-center h-full p-8">
        <div class="alert alert-error max-w-md">
          <Icon icon="mynaui:danger" class="h-5 w-5 shrink-0" />
          <span class="text-sm">{{ errorMsg }}</span>
        </div>
      </div>

      <!-- No changes -->
      <div
        v-else-if="files.length === 0"
        class="flex flex-col items-center justify-center h-full gap-3 text-base-content/40"
      >
        <Icon icon="material-symbols:check-circle-outline-rounded" class="h-12 w-12 text-success" />
        <p class="text-sm font-medium">No changes detected</p>
        <p class="text-xs">Working tree is clean relative to HEAD</p>
      </div>

      <!-- Diff viewer -->
      <div v-else-if="diffFileObj" class="h-full diff-scroll-container">
        <DiffView
          :diff-file="diffFileObj"
          :diff-view-mode="viewMode"
          :diff-view-theme="theme"
          :diff-view-highlight="true"
          :diff-view-add-widget="true"
          :diff-view-wrap="true"
          @on-add-widget-click="handleAddWidgetClick"
          class="h-full"
        >
          <!-- Comment widget slot — rendered at the active line -->
          <template #widget="{ lineNumber, side, onClose }">
            <div
              v-if="activeWidget?.side === side && activeWidget?.lineNumber === lineNumber"
              class="border border-primary/30 bg-base-200 rounded-lg mx-4 my-2 shadow-xl overflow-hidden"
            >
              <!-- Widget header -->
              <div
                class="flex items-center justify-between px-3 py-2 bg-base-300/60 border-b border-base-300"
              >
                <div class="flex items-center gap-2 text-xs font-semibold text-base-content/70">
                  <Icon icon="material-symbols:chat-bubble-outline" class="h-4 w-4 text-primary" />
                  <span
                    >Comment · {{ selectedFile?.newPath }} · line {{ lineNumber }} ({{
                      sideName(side)
                    }})</span
                  >
                </div>
                <button
                  @click="
                    onClose();
                    closeWidget();
                  "
                  class="btn btn-ghost btn-xs btn-square text-base-content/50 hover:text-base-content"
                >
                  <Icon icon="mynaui:x" class="h-4 w-4" />
                </button>
              </div>

              <!-- Line preview -->
              <div class="px-3 py-2 border-b border-base-300/60 bg-base-300/20">
                <pre
                  class="text-xs font-mono text-base-content/60 whitespace-pre-wrap break-words"
                  >{{ getLineContent(side, lineNumber) }}</pre>
              </div>

              <!-- Textarea -->
              <div class="p-3 space-y-2">
                <textarea
                  v-model="widgetInput"
                  placeholder="Add a comment… it will appear in the chat input below"
                  rows="3"
                  class="textarea textarea-bordered bg-base-100 text-base-content w-full text-xs font-sans resize-none focus:outline-none focus:border-primary"
                  @keydown.ctrl.enter.prevent="submitComment"
                  autofocus
                ></textarea>
                <div class="flex items-center justify-between gap-2">
                  <button
                    v-if="hasComment(side, lineNumber)"
                    @click="
                      deleteComment(commentKey(selectedFile?.newPath ?? '', side, lineNumber));
                      onClose();
                    "
                    class="btn btn-ghost btn-xs text-error hover:bg-error/10 gap-1"
                  >
                    <Icon icon="mynaui:trash-one" class="h-3.5 w-3.5" />
                    Delete
                  </button>
                  <div class="flex gap-2 ml-auto">
                    <button
                      @click="
                        onClose();
                        closeWidget();
                      "
                      class="btn btn-ghost btn-xs"
                    >
                      Cancel
                    </button>
                    <button
                      @click="submitComment"
                      :disabled="!widgetInput.trim()"
                      class="btn btn-primary btn-xs gap-1"
                    >
                      <Icon icon="material-symbols:add" class="h-3.5 w-3.5" />
                      Add to Chat
                    </button>
                  </div>
                </div>
              </div>
            </div>
          </template>
        </DiffView>
      </div>
    </div>

    <!-- Comment summary bar -->
    <div
      v-if="comments.size > 0"
      class="shrink-0 px-3 py-2 bg-warning/10 border-t border-warning/20 flex items-center gap-2 flex-wrap"
    >
      <Icon icon="material-symbols:chat-bubble-outline" class="h-4 w-4 text-warning shrink-0" />
      <span class="text-xs text-warning font-medium">
        {{ comments.size }} comment{{ comments.size > 1 ? "s" : "" }} added to chat input
      </span>
      <div class="flex items-center gap-1 flex-wrap flex-1 min-w-0">
        <span
          v-for="[key, entry] in Array.from(comments.entries())"
          :key="key"
          class="badge badge-xs badge-warning gap-1 cursor-pointer hover:badge-error transition-colors"
          @click="deleteComment(key)"
          :title="`${entry.filePath}:${entry.lineNumber} — click to remove`"
        >
          {{ entry.filePath.split("/").pop() }}:{{ entry.lineNumber }}
          <Icon icon="mynaui:x" class="h-2.5 w-2.5" />
        </span>
      </div>
      <button
        @click="
          comments.clear();
          emit('update:chatInputText', '');
        "
        class="btn btn-ghost btn-xs text-warning/70 hover:text-error ml-auto shrink-0"
        title="Clear all comments"
      >
        Clear all
      </button>
    </div>
  </div>
</template>

<style scoped>
/* Let the diff scroll within its container */
.diff-scroll-container {
  overflow: auto;
}

:deep(.d2h-wrapper),
:deep(.diff-view-container) {
  font-family: ui-monospace, SFMono-Regular, "SF Mono", Menlo, Consolas, monospace;
  font-size: 12px;
}
</style>
