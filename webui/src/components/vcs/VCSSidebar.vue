<script setup lang="ts">
import { ref } from "vue";
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
</script>

<template>
  <div class="flex flex-col h-full overflow-hidden bg-base-200 border-l border-base-300 min-w-0">
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
            class="badge badge-xs badge-neutral gap-1 font-mono text-[10px] shrink-0"
          >
            <span v-if="ahead > 0" class="text-success">↑{{ ahead }}</span>
            <span v-if="behind > 0" class="text-error">↓{{ behind }}</span>
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

    <!-- Middle Section: Changed Files list (Top half) -->
    <div class="flex-1 flex flex-col min-h-0 border-b border-base-300">
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

    <!-- Bottom Section: Commit History Tree (Bottom half) -->
    <div class="h-1/2 min-h-[220px] flex flex-col min-h-0 overflow-hidden">
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
