<script setup lang="ts">
import { ref, watch, onMounted, onUnmounted, computed } from "vue";
import { Icon } from "@iconify/vue";
import FileTreeNode from "./FileTreeNode.vue";
import { getFileTree } from "../../lib/api";
import { useShortcuts } from "../../composables/useShortcuts";
import { formatPath } from "../../utils/agentUtils";
import type { FileTreeEntry } from "../../types";

const props = defineProps<{
  sessionId: string;
  runDir: string;
  selectedPath: string | null;
  commentedFiles: string[];
  loading?: boolean;
}>();

const emit = defineEmits<{
  (e: "select-file", path: string): void;
  (e: "open-search"): void;
  (e: "refresh"): void;
}>();

const { fileSearchShortcut } = useShortcuts();

const rootNodes = ref<FileTreeEntry[]>([]);
const isTreeLoading = ref(false);
const treeError = ref("");
const treeVersion = ref(0);
let treeReqId = 0;

const workspaceName = computed(() => {
  if (!props.runDir) return "Workspace";
  const parts = props.runDir.replace(/\\/g, "/").split("/").filter(Boolean);
  return parts[parts.length - 1] || formatPath(props.runDir) || props.runDir;
});

async function loadTree() {
  if (!props.sessionId) return;
  const currentReq = ++treeReqId;
  isTreeLoading.value = true;
  treeError.value = "";
  try {
    const entries = await getFileTree(props.sessionId, "");
    if (currentReq !== treeReqId) return;
    rootNodes.value = entries;
    treeVersion.value++;
  } catch (err: any) {
    if (currentReq !== treeReqId) return;
    treeError.value = err?.message || "Failed to load files";
  } finally {
    if (currentReq === treeReqId) {
      isTreeLoading.value = false;
    }
  }
}

function handleRefresh() {
  loadTree();
  emit("refresh");
}

watch(
  () => [props.sessionId, props.runDir],
  () => {
    loadTree();
  },
  { immediate: true },
);

defineExpose({
  loadTree,
});

// ── Sidebar width management (Desktop resizing) ────────────────────────────
const DEFAULT_SIDEBAR_WIDTH = 280;
const MIN_SIDEBAR_WIDTH = 200;
const MAX_SIDEBAR_WIDTH = 600;

const sidebarWidth = ref(DEFAULT_SIDEBAR_WIDTH);
const isResizing = ref(false);
const isDesktop = ref(typeof window !== "undefined" && window.innerWidth >= 768);

const updateWindowWidth = () => {
  isDesktop.value = window.innerWidth >= 768;
};

const startResize = (e: MouseEvent) => {
  e.preventDefault();
  isResizing.value = true;
  document.addEventListener("mousemove", handleMouseMove);
  document.addEventListener("mouseup", stopResize);
  document.body.style.userSelect = "none";
  document.body.style.cursor = "col-resize";
};

const handleMouseMove = (e: MouseEvent) => {
  if (!isResizing.value) return;
  const newWidth = Math.min(
    Math.max(window.innerWidth - e.clientX, MIN_SIDEBAR_WIDTH),
    MAX_SIDEBAR_WIDTH,
  );
  sidebarWidth.value = newWidth;
};

const stopResize = () => {
  if (isResizing.value) {
    isResizing.value = false;
    localStorage.setItem("asgard_filetree_sidebar_width", sidebarWidth.value.toString());
    document.removeEventListener("mousemove", handleMouseMove);
    document.removeEventListener("mouseup", stopResize);
    document.body.style.userSelect = "";
    document.body.style.cursor = "";
  }
};

onMounted(() => {
  window.addEventListener("resize", updateWindowWidth);
  const saved = localStorage.getItem("asgard_filetree_sidebar_width");
  if (saved) {
    const parsed = parseInt(saved, 10);
    if (!isNaN(parsed) && parsed >= MIN_SIDEBAR_WIDTH && parsed <= MAX_SIDEBAR_WIDTH) {
      sidebarWidth.value = parsed;
    }
  }
});

onUnmounted(() => {
  window.removeEventListener("resize", updateWindowWidth);
  document.removeEventListener("mousemove", handleMouseMove);
  document.removeEventListener("mouseup", stopResize);
  document.body.style.userSelect = "";
  document.body.style.cursor = "";
});
</script>

<template>
  <aside
    class="h-full flex relative shrink-0 shadow-xl bg-base-200 border-l border-base-300 min-w-0"
    :class="[
      isDesktop ? (isResizing ? 'transition-none' : 'transition-[width] duration-200') : 'w-full',
    ]"
    :style="{
      width: isDesktop ? `${sidebarWidth}px` : '100%',
    }"
  >
    <!-- Resizer Handle on Left Edge (Desktop only) -->
    <div
      v-if="isDesktop"
      @mousedown="startResize"
      class="absolute top-0 left-0 w-1.5 h-full cursor-col-resize hover:bg-primary/50 transition-colors z-30"
      title="Drag to resize File Tree sidebar"
    ></div>

    <!-- Sidebar Main Column -->
    <div class="flex-1 flex flex-col h-full overflow-hidden min-w-0">
      <!-- Header: Workspace Name + Actions (Search, Refresh) -->
      <div
        class="p-2.5 bg-base-200 border-b border-base-300 flex items-center justify-between gap-2 shrink-0"
      >
        <div class="flex items-center gap-1.5 min-w-0 font-mono text-xs">
          <Icon icon="octicon:file-directory-fill-24" class="h-4 w-4 text-warning shrink-0" />
          <span class="font-bold text-base-content truncate" :title="runDir">
            {{ workspaceName }}
          </span>
        </div>

        <div class="flex items-center gap-1 shrink-0">
          <!-- Refresh button -->
          <button
            @click="handleRefresh"
            :disabled="isTreeLoading || loading"
            class="btn btn-ghost btn-xs btn-circle text-base-content/70 hover:text-base-content"
            title="Refresh File Tree"
          >
            <Icon
              icon="mynaui:refresh"
              :class="['h-3.5 w-3.5', { 'animate-spin': isTreeLoading || loading }]"
            />
          </button>
        </div>
      </div>

      <!-- Quick Search / File Filter Shortcut Banner -->
      <div class="px-2.5 py-1.5 bg-base-300/30 border-b border-base-300/60 shrink-0">
        <button
          @click="emit('open-search')"
          class="w-full flex items-center justify-between px-2 py-1 rounded bg-base-100/70 border border-base-300/80 text-base-content/60 hover:text-base-content hover:border-primary/40 text-xs transition-colors cursor-pointer"
        >
          <span class="flex items-center gap-1.5 truncate">
            <Icon icon="material-symbols:search" class="h-3.5 w-3.5 text-base-content/40" />
            <span class="text-[11px] truncate">Find file...</span>
          </span>
          <kbd class="kbd kbd-xs bg-base-200 text-[10px] text-base-content/50 font-mono">{{
            fileSearchShortcut
          }}</kbd>
        </button>
      </div>

      <!-- Tree Content Area -->
      <div class="flex-1 overflow-y-auto overflow-x-hidden p-1.5 space-y-0.5 min-h-0">
        <!-- Loading State -->
        <div
          v-if="isTreeLoading && rootNodes.length === 0"
          class="flex items-center justify-center py-8 text-base-content/50 gap-2 text-xs"
        >
          <span class="loading loading-spinner loading-xs text-primary"></span>
          <span>Loading files...</span>
        </div>

        <!-- Error State -->
        <div v-else-if="treeError" class="p-3">
          <div class="alert alert-error text-xs p-2.5">
            <Icon icon="mynaui:danger" class="h-4 w-4 shrink-0" />
            <span>{{ treeError }}</span>
          </div>
        </div>

        <!-- Empty Workspace State -->
        <div
          v-else-if="rootNodes.length === 0"
          class="py-8 px-3 text-center text-xs text-base-content/40 flex flex-col items-center justify-center gap-1.5"
        >
          <Icon icon="octicon:file-directory-24" class="h-6 w-6 text-base-content/20" />
          <span>Workspace is empty</span>
        </div>

        <!-- Root Node List -->
        <template v-else>
          <FileTreeNode
            v-for="node in rootNodes"
            :key="node.path"
            :node="node"
            :sessionId="sessionId"
            :selectedPath="selectedPath"
            :commentedFiles="commentedFiles"
            :depth="0"
            :treeVersion="treeVersion"
            @select-file="(path) => emit('select-file', path)"
          />
        </template>
      </div>
    </div>
  </aside>
</template>
