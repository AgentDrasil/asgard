<script setup lang="ts">
import { ref, onMounted } from "vue";
import { Icon } from "@iconify/vue";
import VCSFileList from "./VCSFileList.vue";
import VCSCommitTree from "./VCSCommitTree.vue";
import { gitPush, gitPull } from "../../lib/api";
import type { GitDiffFile, GitCommit } from "../../types";

const props = defineProps<{
  runDir: string;
  files: GitDiffFile[];
  selectedIndex: number;
  commentedFiles?: string[];
  commits: GitCommit[];
  currentBranch: string;
  trackingBranch?: string;
  ahead: number;
  behind: number;
  unstashedCount: number;
  selectedCommit: string | null;
  loading?: boolean;
}>();

const emit = defineEmits<{
  (e: "select-file", index: number): void;
  (e: "select-commit", hash: string | null): void;
  (e: "refresh"): void;
}>();

const isPushing = ref(false);
const isPulling = ref(false);
const actionMessage = ref<{ type: "success" | "error"; text: string } | null>(null);

let actionTimer: number | null = null;
function setActionFeedback(type: "success" | "error", text: string) {
  actionMessage.value = { type, text };
  if (actionTimer) window.clearTimeout(actionTimer);
  actionTimer = window.setTimeout(() => {
    actionMessage.value = null;
  }, 4000);
}

async function handlePush() {
  if (isPushing.value || !props.runDir) return;
  isPushing.value = true;
  actionMessage.value = null;
  try {
    const res = await gitPush(props.runDir);
    if (res.success) {
      setActionFeedback("success", res.output || "Pushed successfully");
      emit("refresh");
    } else {
      setActionFeedback("error", res.error || "Push failed");
    }
  } catch (err: any) {
    setActionFeedback("error", err?.message || "Push failed");
  } finally {
    isPushing.value = false;
  }
}

async function handlePull() {
  if (isPulling.value || !props.runDir) return;
  isPulling.value = true;
  actionMessage.value = null;
  try {
    const res = await gitPull(props.runDir);
    if (res.success) {
      setActionFeedback("success", res.output || "Pulled successfully");
      emit("refresh");
    } else {
      setActionFeedback("error", res.error || "Pull failed");
    }
  } catch (err: any) {
    setActionFeedback("error", err?.message || "Pull failed");
  } finally {
    isPulling.value = false;
  }
}

// ── Vertical Resizer (Files vs Commits height split) ────────────────────────
const DEFAULT_COMMITS_HEIGHT = 240;
const MIN_COMMITS_HEIGHT = 100;
const MIN_FILES_HEIGHT = 100;

const commitsHeight = ref(DEFAULT_COMMITS_HEIGHT);
const isResizingVertical = ref(false);
const sidebarContainerRef = ref<HTMLDivElement | null>(null);

const startVerticalResize = (e: MouseEvent) => {
  e.preventDefault();
  isResizingVertical.value = true;
  const startY = e.clientY;
  const startHeight = commitsHeight.value;

  const handleMouseMove = (moveEvent: MouseEvent) => {
    if (!isResizingVertical.value) return;
    const deltaY = moveEvent.clientY - startY;
    const containerHeight = sidebarContainerRef.value?.clientHeight || 600;
    const maxCommitsHeight = Math.max(MIN_COMMITS_HEIGHT, containerHeight - MIN_FILES_HEIGHT - 100);
    const newHeight = Math.min(
      Math.max(startHeight - deltaY, MIN_COMMITS_HEIGHT),
      maxCommitsHeight,
    );
    commitsHeight.value = newHeight;
  };

  const stopResize = () => {
    if (isResizingVertical.value) {
      isResizingVertical.value = false;
      localStorage.setItem("asgard_vcs_commits_height", commitsHeight.value.toString());
      document.removeEventListener("mousemove", handleMouseMove);
      document.removeEventListener("mouseup", stopResize);
      document.body.style.userSelect = "";
      document.body.style.cursor = "";
    }
  };

  document.addEventListener("mousemove", handleMouseMove);
  document.addEventListener("mouseup", stopResize);
  document.body.style.userSelect = "none";
  document.body.style.cursor = "row-resize";
};

onMounted(() => {
  const savedHeight = localStorage.getItem("asgard_vcs_commits_height");
  if (savedHeight) {
    const parsed = parseInt(savedHeight, 10);
    if (!isNaN(parsed) && parsed >= MIN_COMMITS_HEIGHT) {
      commitsHeight.value = parsed;
    }
  }
});
</script>

<template>
  <div
    ref="sidebarContainerRef"
    class="flex flex-col h-full overflow-hidden bg-base-200 border-l border-base-300 min-w-0"
  >
    <!-- Top Action Bar (Buttons: Push / Pull / Refresh / Branch info) -->
    <div class="p-2.5 bg-base-200 border-b border-base-300 shrink-0 space-y-2">
      <!-- Top Row: Branch & Ahead/Behind Status -->
      <div class="flex items-center justify-between gap-1 text-xs">
        <div class="flex items-center gap-1.5 min-w-0 font-mono">
          <Icon icon="octicon:git-branch-24" class="h-3.5 w-3.5 text-primary shrink-0" />
          <span class="font-bold text-base-content truncate" :title="currentBranch || 'git'">
            {{ currentBranch || "main" }}
          </span>
          <span
            v-if="ahead > 0 || behind > 0"
            class="badge badge-sm bg-base-300 text-base-content border border-base-content/15 px-1.5 py-0.5 gap-1 font-mono text-[11px] font-bold shrink-0 shadow-xs"
          >
            <span v-if="ahead > 0" class="text-success flex items-center gap-0.5 font-bold">
              <span>↑</span><span>{{ ahead }}</span>
            </span>
            <span v-if="behind > 0" class="text-error flex items-center gap-0.5 font-bold">
              <span>↓</span><span>{{ behind }}</span>
            </span>
          </span>
        </div>

        <button
          @click="emit('refresh')"
          :disabled="loading || isPushing || isPulling"
          class="btn btn-ghost btn-xs btn-circle text-base-content/70 hover:text-base-content"
          title="Refresh Git status"
        >
          <Icon icon="mynaui:refresh" :class="['h-3.5 w-3.5', { 'animate-spin': loading }]" />
        </button>
      </div>

      <!-- Button Row: Pull & Push -->
      <div class="grid grid-cols-2 gap-1.5">
        <!-- Pull Button -->
        <button
          @click="handlePull"
          :disabled="isPulling || isPushing || loading"
          class="btn btn-sm btn-outline gap-1.5 text-xs font-semibold hover:btn-primary"
          title="Git Pull from remote"
        >
          <span v-if="isPulling" class="loading loading-spinner loading-xs"></span>
          <Icon v-else icon="octicon:repo-pull-24" class="h-3.5 w-3.5" />
          <span>Pull</span>
        </button>

        <!-- Push Button -->
        <button
          @click="handlePush"
          :disabled="isPushing || isPulling || loading"
          class="btn btn-sm btn-primary gap-1.5 text-xs font-semibold shadow-xs"
          title="Git Push to remote"
        >
          <span v-if="isPushing" class="loading loading-spinner loading-xs"></span>
          <Icon v-else icon="octicon:repo-push-24" class="h-3.5 w-3.5" />
          <span>Push</span>
        </button>
      </div>

      <!-- Action Toast / Feedback message -->
      <div
        v-if="actionMessage"
        :class="[
          'text-xs px-2.5 py-1.5 rounded flex items-center gap-1.5 shadow-xs transition-all',
          actionMessage.type === 'success'
            ? 'bg-success/15 text-success border border-success/30'
            : 'bg-error/15 text-error border border-error/30',
        ]"
      >
        <Icon
          :icon="
            actionMessage.type === 'success'
              ? 'material-symbols:check-circle-outline-rounded'
              : 'mynaui:danger'
          "
          class="h-4 w-4 shrink-0"
        />
        <span class="truncate flex-1 font-mono text-[11px]">{{ actionMessage.text }}</span>
        <button
          @click="actionMessage = null"
          class="btn btn-ghost btn-xs btn-circle h-4 w-4 min-h-0 text-current hover:bg-base-100/30"
        >
          <Icon icon="mynaui:x" class="h-3 w-3" />
        </button>
      </div>
    </div>

    <!-- Middle Section: Changed Files list (Top resizable part) -->
    <div class="flex-1 flex flex-col min-h-[100px] overflow-hidden">
      <div
        class="px-3 py-1.5 bg-base-300/40 border-b border-base-300/60 flex items-center justify-between shrink-0"
      >
        <div class="flex items-center gap-1.5 text-xs font-bold text-base-content/80">
          <Icon icon="octicon:file-diff-24" class="h-3.5 w-3.5 text-primary" />
          <span>{{ selectedCommit ? "Commit Files" : "Changed Files" }}</span>
          <span class="badge badge-xs badge-neutral text-[10px] font-mono">{{ files.length }}</span>
        </div>
      </div>
      <div class="flex-1 min-h-0 overflow-hidden">
        <VCSFileList
          :files="files"
          :selectedIndex="selectedIndex"
          :commentedFiles="commentedFiles"
          @select-file="(idx) => emit('select-file', idx)"
        />
      </div>
    </div>

    <!-- Horizontal Resizer Handle between Files and Commits -->
    <div
      @mousedown="startVerticalResize"
      class="h-1.5 w-full cursor-row-resize bg-base-300 hover:bg-primary/50 active:bg-primary transition-colors z-20 shrink-0 border-y border-base-content/10 flex items-center justify-center group select-none"
      title="Drag to resize Files and Commits panels"
    >
      <div class="w-8 h-0.5 bg-base-content/20 group-hover:bg-primary-content/80 rounded"></div>
    </div>

    <!-- Bottom Section: Commit History Tree (Bottom resizable part) -->
    <div
      :style="{ height: `${commitsHeight}px` }"
      :class="[
        'shrink-0 flex flex-col min-h-[100px] overflow-hidden',
        isResizingVertical ? 'transition-none' : 'transition-[height] duration-150',
      ]"
    >
      <VCSCommitTree
        :commits="commits"
        :unstashedCount="unstashedCount"
        :selectedCommit="selectedCommit"
        :loading="loading"
        @select-commit="(hash) => emit('select-commit', hash)"
      />
    </div>
  </div>
</template>
