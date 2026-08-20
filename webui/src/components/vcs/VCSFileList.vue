<script setup lang="ts">
import { ref, computed } from "vue";
import { Icon } from "@iconify/vue";
import type { GitDiffFile } from "../../types";

const props = defineProps<{
  files: GitDiffFile[];
  selectedIndex: number;
  commentedFiles?: string[];
}>();

const emit = defineEmits<{
  (e: "select-file", index: number): void;
}>();

const searchQuery = ref("");

const filteredFiles = computed(() => {
  if (!searchQuery.value.trim()) {
    return props.files.map((file, originalIndex) => ({ file, originalIndex }));
  }
  const q = searchQuery.value.toLowerCase().trim();
  return props.files
    .map((file, originalIndex) => ({ file, originalIndex }))
    .filter(({ file }) => file.newPath.toLowerCase().includes(q));
});

function getFileStatus(file: GitDiffFile): { label: string; badgeClass: string } {
  switch (file.status) {
    case "A":
      return { label: "A", badgeClass: "badge-success text-success-content" };
    case "D":
      return { label: "D", badgeClass: "badge-error text-error-content" };
    case "R":
      return { label: "R", badgeClass: "badge-info text-info-content" };
  }
  // Fallback for payloads without a status field
  if (file.oldPath === "/dev/null") {
    return { label: "A", badgeClass: "badge-success text-success-content" };
  }
  if (file.newPath === "/dev/null") {
    return { label: "D", badgeClass: "badge-error text-error-content" };
  }
  if (file.oldPath && file.newPath && file.oldPath !== file.newPath) {
    return { label: "R", badgeClass: "badge-info text-info-content" };
  }
  return { label: "M", badgeClass: "badge-warning text-warning-content" };
}

function getFileNameAndDir(path: string) {
  const parts = path.split("/");
  const fileName = parts.pop() || path;
  const dirName = parts.join("/");
  return { fileName, dirName };
}

function isCommented(path: string): boolean {
  return props.commentedFiles?.includes(path) || false;
}
</script>

<template>
  <div class="flex flex-col h-full overflow-hidden min-h-0 bg-base-100">
    <!-- Search / Filter header -->
    <div class="p-2 border-b border-base-300 bg-base-200/40 shrink-0">
      <div class="relative flex items-center">
        <Icon
          icon="iconamoon:search"
          class="absolute left-2.5 h-3.5 w-3.5 text-base-content/50 pointer-events-none"
        />
        <input
          v-model="searchQuery"
          type="text"
          placeholder="Filter files..."
          class="input input-xs w-full pl-8 pr-7 bg-base-100 border-base-300 focus:border-primary text-xs"
        />
        <button
          v-if="searchQuery"
          @click="searchQuery = ''"
          class="absolute right-1.5 btn btn-ghost btn-xs btn-circle h-4 w-4 min-h-0 text-base-content/50 hover:text-base-content"
        >
          <Icon icon="mynaui:x" class="h-3 w-3" />
        </button>
      </div>
    </div>

    <!-- Files List -->
    <div class="flex-1 overflow-y-auto overflow-x-hidden p-1.5 space-y-1">
      <div
        v-if="files.length === 0"
        class="py-6 px-3 text-center text-xs text-base-content/40 flex flex-col items-center justify-center gap-1.5"
      >
        <Icon
          icon="material-symbols:check-circle-outline-rounded"
          class="h-6 w-6 text-success/60"
        />
        <span>No changed files</span>
      </div>

      <div
        v-else-if="filteredFiles.length === 0"
        class="py-4 text-center text-xs text-base-content/40"
      >
        No matching files
      </div>

      <button
        v-for="{ file, originalIndex } in filteredFiles"
        :key="file.newPath || file.oldPath"
        @click="emit('select-file', originalIndex)"
        :class="[
          'w-full text-left px-2.5 py-1.5 rounded-md text-xs font-mono flex items-center gap-2 transition-colors cursor-pointer group',
          selectedIndex === originalIndex
            ? 'bg-primary/15 text-primary font-semibold shadow-xs'
            : 'text-base-content/80 hover:bg-base-200 hover:text-base-content',
        ]"
      >
        <!-- Status Badge -->
        <span
          :class="[
            'badge badge-xs font-bold text-[10px] shrink-0 w-4 h-4 p-0 flex items-center justify-center rounded',
            getFileStatus(file).badgeClass,
          ]"
          :title="getFileStatus(file).label"
        >
          {{ getFileStatus(file).label }}
        </span>

        <!-- Path with directory muted and filename clear -->
        <span class="flex-1 truncate min-w-0 flex items-baseline gap-1">
          <span class="font-medium truncate text-base-content">{{
            getFileNameAndDir(file.newPath).fileName
          }}</span>
          <span
            v-if="getFileNameAndDir(file.newPath).dirName"
            class="text-[10px] text-base-content/40 truncate"
          >
            {{ getFileNameAndDir(file.newPath).dirName }}
          </span>
        </span>

        <!-- Comment badge -->
        <span
          v-if="isCommented(file.newPath)"
          class="badge badge-xs badge-warning shrink-0"
          title="Contains comments"
        >
          💬
        </span>

        <!-- Active indicator -->
        <Icon
          v-if="selectedIndex === originalIndex"
          icon="material-symbols:chevron-right-rounded"
          class="h-4 w-4 text-primary shrink-0 opacity-80"
        />
      </button>
    </div>
  </div>
</template>
