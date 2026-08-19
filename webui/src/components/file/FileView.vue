<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted, watch } from "vue";
import { Icon } from "@iconify/vue";
import FileTreeSidebar from "./FileTreeSidebar.vue";
import FileCodeViewer from "./FileCodeViewer.vue";
import { useShortcuts } from "../../composables/useShortcuts";
import { rebuildChatInputFromComments, commentKey } from "../../utils/commentUtils";
import { humanfriendly } from "../../lib/format";
import type { CommentEntry, WorkspaceFileContent } from "../../types";

const props = defineProps<{
  sessionId: string;
  runDir: string;
  gitRoot: string;
  initialFilePath?: string | null;
  isTerminalOpen?: boolean;
}>();

const isFileTreeOpen = defineModel<boolean>("isFileTreeOpen", { default: true });
const chatInputText = defineModel<string>("chatInputText", { default: "" });
const selectedFilePath = defineModel<string | null>("selectedFilePath", { default: null });

const emit = defineEmits<{
  (e: "close"): void;
  (e: "open-vcs"): void;
  (e: "toggle-terminal"): void;
  (e: "open-search"): void;
}>();

const {
  toggleTerminalShortcut,
  toggleArtifactsShortcut,
  toggleDiffShortcut,
  toggleFileViewShortcut,
} = useShortcuts();

// ── Responsive Layout & Mobile Tabs ──────────────────────────────────────────
const isDesktop = ref(typeof window !== "undefined" && window.innerWidth >= 768);
const mobileActiveTab = ref<"files" | "code">(
  props.initialFilePath || selectedFilePath.value ? "code" : "files",
);

const updateWindowWidth = () => {
  isDesktop.value = window.innerWidth >= 768;
};

onMounted(() => {
  window.addEventListener("resize", updateWindowWidth);
  if (props.initialFilePath && !selectedFilePath.value) {
    selectedFilePath.value = props.initialFilePath;
  }
});
onUnmounted(() => {
  window.removeEventListener("resize", updateWindowWidth);
});

// ── In-Memory Comments & Selected File Metadata ──────────────────────────────
const comments = ref<Map<string, CommentEntry>>(new Map());
const currentFileData = ref<WorkspaceFileContent | null>(null);
const codeViewerRef = ref<InstanceType<typeof FileCodeViewer> | null>(null);
const treeSidebarRef = ref<InstanceType<typeof FileTreeSidebar> | null>(null);

const commentedFileList = computed(() => {
  return [...new Set(Array.from(comments.value.values()).map((c) => c.filePath))];
});

function handleAddComment(entry: CommentEntry) {
  const key = commentKey(entry.filePath, entry.lineNumber);
  comments.value.set(key, entry);
  chatInputText.value = rebuildChatInputFromComments(comments.value);
}

function handleDeleteComment(key: string) {
  comments.value.delete(key);
  chatInputText.value = rebuildChatInputFromComments(comments.value);
}

function handleClearComments() {
  comments.value.clear();
  chatInputText.value = "";
}

function handleSelectFile(path: string) {
  selectedFilePath.value = path;
  if (!isDesktop.value) {
    mobileActiveTab.value = "code";
  }
}

function handleRefreshFile() {
  codeViewerRef.value?.loadContent();
}

watch(
  () => props.initialFilePath,
  (newPath) => {
    if (newPath) {
      selectedFilePath.value = newPath;
      if (!isDesktop.value) {
        mobileActiveTab.value = "code";
      }
    }
  },
);

const breadcrumbParts = computed(() => {
  if (!selectedFilePath.value) return [];
  return selectedFilePath.value.split("/").filter(Boolean);
});
</script>

<template>
  <div class="flex-1 flex flex-col h-full overflow-hidden bg-base-100 min-w-0">
    <!-- Header -->
    <header
      class="px-3 py-2 sm:px-4 sm:py-2.5 bg-base-200 border-b border-base-300 flex items-center justify-between gap-2 shrink-0 shadow-sm"
    >
      <!-- Left: File Path / Breadcrumbs & File Metadata / Refresh -->
      <div class="flex items-center gap-2 min-w-0 font-mono text-xs">
        <Icon icon="octicon:file-directory-24" class="h-4 w-4 text-primary shrink-0" />
        <template v-if="selectedFilePath">
          <div class="flex items-center gap-1 truncate text-base-content/80">
            <span
              v-for="(part, idx) in breadcrumbParts"
              :key="idx"
              class="flex items-center gap-1 truncate"
            >
              <span v-if="idx > 0" class="text-base-content/40">/</span>
              <span
                :class="
                  idx === breadcrumbParts.length - 1
                    ? 'font-bold text-base-content'
                    : 'text-base-content/70'
                "
              >
                {{ part }}
              </span>
            </span>
          </div>

          <!-- File Size and Timestamp -->
          <div
            v-if="currentFileData"
            class="hidden lg:flex items-center gap-1.5 text-[11px] text-base-content/50 shrink-0 font-mono"
          >
            <span>({{ humanfriendly(currentFileData.size) }}B)</span>
            <span v-if="currentFileData.updatedAt"
              >· {{ new Date(currentFileData.updatedAt).toLocaleString() }}</span
            >
          </div>

          <!-- Refresh File Button -->
          <button
            @click="handleRefreshFile"
            class="btn btn-ghost btn-xs btn-circle text-base-content/60 hover:text-base-content shrink-0"
            title="Refresh current file"
          >
            <Icon icon="mynaui:refresh" class="h-3.5 w-3.5" />
          </button>
        </template>
        <template v-else>
          <span class="font-bold text-base-content truncate">Workspace Files</span>
        </template>
      </div>

      <!-- Center: Mobile Tab Segmented Switcher -->
      <div class="flex items-center gap-2">
        <div class="md:hidden join bg-base-300/70 p-0.5 rounded-lg">
          <button
            @click="mobileActiveTab = 'files'"
            :class="[
              'join-item btn btn-xs border-none font-medium gap-1 text-[11px]',
              mobileActiveTab === 'files'
                ? 'btn-primary shadow-xs'
                : 'btn-ghost text-base-content/70',
            ]"
          >
            <Icon icon="octicon:file-directory-24" class="h-3 w-3" />
            <span>Files</span>
          </button>
          <button
            @click="mobileActiveTab = 'code'"
            :class="[
              'join-item btn btn-xs border-none font-medium gap-1 text-[11px]',
              mobileActiveTab === 'code'
                ? 'btn-primary shadow-xs'
                : 'btn-ghost text-base-content/70',
            ]"
          >
            <Icon icon="octicon:file-code-24" class="h-3 w-3" />
            <span>Code</span>
          </button>
        </div>
      </div>

      <!-- Right: View Switcher Join Group & Layout Controls Join Group -->
      <div class="flex items-center gap-1.5 sm:gap-2 shrink-0">
        <!-- View Switcher Join Group (Chat / VCS / Files) -->
        <div class="join bg-base-300/60 p-0.5 rounded-lg shrink-0">
          <button
            @click="emit('close')"
            class="join-item btn btn-xs border-none font-medium gap-1 sm:gap-1.5 btn-ghost text-base-content/70 hover:text-base-content"
            :title="`Switch to Chat View (${toggleFileViewShortcut})`"
          >
            <Icon icon="material-symbols:chat-outline" class="h-3.5 w-3.5" />
            <span class="hidden sm:inline">Chat</span>
          </button>
          <button
            v-if="gitRoot"
            @click="emit('open-vcs')"
            class="join-item btn btn-xs border-none font-medium gap-1 sm:gap-1.5 btn-ghost text-base-content/70 hover:text-base-content"
            :title="`Switch to VCS View (${toggleDiffShortcut})`"
          >
            <Icon icon="octicon:git-branch-24" class="h-3.5 w-3.5" />
            <span class="hidden sm:inline">VCS</span>
          </button>
          <button
            class="join-item btn btn-xs border-none font-medium gap-1 sm:gap-1.5 btn-primary shadow-xs"
            title="Files View"
          >
            <Icon icon="octicon:file-code-24" class="h-3.5 w-3.5" />
            <span class="hidden sm:inline">Files</span>
          </button>
        </div>

        <!-- Layout Controls Join Group (Bottom Panel / Right Sidebar) -->
        <div class="join bg-base-300/60 p-0.5 rounded-lg shrink-0">
          <!-- Toggle Terminal Bottom Panel Button -->
          <button
            @click="emit('toggle-terminal')"
            class="join-item btn btn-xs border-none gap-1"
            :class="
              isTerminalOpen
                ? 'btn-primary shadow-xs'
                : 'btn-ghost text-base-content/70 hover:text-base-content'
            "
            :title="`Toggle Terminal Panel (${toggleTerminalShortcut})`"
          >
            <Icon icon="codicon:layout-panel" class="h-3.5 w-3.5" />
            <span class="hidden xl:inline">Terminal</span>
          </button>

          <!-- Toggle File Tree Right Sidebar Button -->
          <button
            @click="isFileTreeOpen = !isFileTreeOpen"
            class="hidden md:inline-flex join-item btn btn-xs border-none gap-1"
            :class="
              isFileTreeOpen
                ? 'btn-primary shadow-xs'
                : 'btn-ghost text-base-content/70 hover:text-base-content'
            "
            :title="`Toggle File Explorer Sidebar (${toggleArtifactsShortcut})`"
          >
            <Icon icon="codicon:layout-sidebar-right" class="h-3.5 w-3.5" />
            <span class="hidden xl:inline">Sidebar</span>
          </button>
        </div>
      </div>
    </header>

    <!-- Main File View Body: Split into Code Viewer (Middle) & FileTreeSidebar (Right) -->
    <div class="flex-1 flex overflow-hidden min-h-0 relative">
      <!-- Middle Area: File Code Viewer -->
      <div
        class="flex-1 flex flex-col h-full overflow-hidden min-w-0 relative"
        :class="!isDesktop && mobileActiveTab === 'files' ? 'hidden' : 'flex'"
      >
        <FileCodeViewer
          ref="codeViewerRef"
          :sessionId="sessionId"
          :filePath="selectedFilePath"
          :comments="comments"
          @file-loaded="(data) => (currentFileData = data)"
          @add-comment="handleAddComment"
          @delete-comment="handleDeleteComment"
          @clear-comments="handleClearComments"
          @open-search="emit('open-search')"
        />
      </div>

      <!-- Right Area: File Tree Sidebar -->
      <div
        v-if="isFileTreeOpen"
        class="h-full flex relative shrink-0 z-20 md:z-auto min-w-0"
        :class="[!isDesktop && mobileActiveTab === 'code' ? 'hidden' : 'flex w-full md:w-auto']"
      >
        <FileTreeSidebar
          ref="treeSidebarRef"
          :sessionId="sessionId"
          :runDir="runDir"
          :selectedPath="selectedFilePath"
          :commentedFiles="commentedFileList"
          @select-file="handleSelectFile"
          @open-search="emit('open-search')"
        />
      </div>
    </div>
  </div>
</template>
