<script setup lang="ts">
import { ref, computed, watch } from "vue";
import { Icon } from "@iconify/vue";
import { getFileTree } from "../../lib/api";
import { getFileIcon, isAncestorDir } from "../../utils/fileUtils";
import type { FileTreeEntry } from "../../types";

const props = withDefaults(
  defineProps<{
    node: FileTreeEntry;
    sessionId: string;
    selectedPath: string | null;
    commentedFiles: string[];
    depth?: number;
    treeVersion?: number;
  }>(),
  {
    depth: 0,
    treeVersion: 0,
  },
);

const emit = defineEmits<{
  (e: "select-file", path: string): void;
}>();

const isExpanded = ref(props.node.isExpanded ?? false);
const isLoading = ref(false);
const isLoaded = ref(
  props.node.isLoaded ?? (props.node.children && props.node.children.length > 0) ?? false,
);
const children = ref<FileTreeEntry[]>(props.node.children ?? []);
const loadError = ref("");
let childReqId = 0;

const isSelected = computed(() => !props.node.isDir && props.selectedPath === props.node.path);
const isCommented = computed(
  () => !props.node.isDir && props.commentedFiles.includes(props.node.path),
);

async function loadDir() {
  if (!props.node.isDir || !props.sessionId) return;
  isLoading.value = true;
  loadError.value = "";
  const currentReq = ++childReqId;
  try {
    const result = await getFileTree(props.sessionId, props.node.path);
    if (currentReq !== childReqId) return;
    children.value = result;
    isLoaded.value = true;
  } catch (err: any) {
    if (currentReq !== childReqId) return;
    console.error("Failed to load child directory:", err);
    loadError.value = err?.message || "Failed to load directory";
  } finally {
    if (currentReq === childReqId) {
      isLoading.value = false;
    }
  }
}

async function toggleExpand() {
  if (!props.node.isDir) {
    emit("select-file", props.node.path);
    return;
  }

  isExpanded.value = !isExpanded.value;
  if (isExpanded.value && !isLoaded.value && !isLoading.value) {
    await loadDir();
  }
}

watch(
  () => props.selectedPath,
  (targetPath) => {
    if (props.node.isDir && targetPath && isAncestorDir(props.node.path, targetPath)) {
      isExpanded.value = true;
      if (!isLoaded.value && !isLoading.value) {
        loadDir();
      }
    }
  },
  { immediate: true },
);

watch(
  () => [props.node, props.treeVersion],
  () => {
    if (props.node.children && props.node.children.length > 0) {
      children.value = props.node.children;
      isLoaded.value = true;
    } else if (isExpanded.value) {
      loadDir();
    } else {
      isLoaded.value = false;
      children.value = [];
    }
  },
);
</script>

<template>
  <div class="select-none text-xs font-mono w-full min-w-0">
    <!-- Row -->
    <button
      type="button"
      @click="toggleExpand"
      :style="{ paddingLeft: `${depth * 14 + 6}px` }"
      :class="[
        'w-full text-left py-1 pr-2 rounded-sm flex items-center gap-1.5 transition-colors cursor-pointer group min-w-0',
        isSelected
          ? 'bg-primary/15 text-primary font-semibold shadow-xs'
          : 'text-base-content/80 hover:bg-base-300/50 hover:text-base-content',
      ]"
      :title="node.path"
    >
      <!-- Directory Chevron & Folder Icon -->
      <template v-if="node.isDir">
        <span
          class="w-3.5 h-3.5 flex items-center justify-center shrink-0 text-base-content/50 group-hover:text-base-content"
        >
          <Icon v-if="isLoading" icon="mynaui:refresh" class="h-3 w-3 animate-spin text-primary" />
          <Icon v-else :icon="isExpanded ? 'ep:arrow-down' : 'ep:arrow-right'" class="h-3 w-3" />
        </span>
        <Icon
          :icon="isExpanded ? 'octicon:file-directory-open-fill-24' : 'octicon:file-directory-24'"
          class="h-4 w-4 text-warning/90 shrink-0"
        />
      </template>

      <!-- File Icon -->
      <template v-else>
        <!-- Indent placeholder where chevron would be -->
        <span class="w-3.5 h-3.5 shrink-0"></span>
        <Icon
          :icon="getFileIcon(node.ext, node.path)"
          class="h-4 w-4 shrink-0 text-base-content/70"
        />
      </template>

      <!-- Node Name -->
      <span
        class="truncate flex-1 min-w-0"
        :class="{ 'font-medium text-base-content': isSelected }"
      >
        {{ node.name }}
      </span>

      <!-- Comment badge for files -->
      <span
        v-if="isCommented"
        class="badge badge-xs badge-warning shrink-0 p-0.5"
        title="Contains comments"
      >
        <Icon icon="material-symbols:chat-bubble-outline" class="h-2.5 w-2.5" />
      </span>
    </button>

    <!-- Children container if expanded directory -->
    <div v-if="node.isDir && isExpanded" class="flex flex-col">
      <div
        v-if="loadError"
        :style="{ paddingLeft: `${(depth + 1) * 14 + 20}px` }"
        class="py-1 text-[11px] text-error flex items-center gap-1"
      >
        <Icon icon="mynaui:danger" class="h-3 w-3 shrink-0" />
        <span class="truncate">{{ loadError }}</span>
      </div>
      <div
        v-else-if="children.length === 0 && !isLoading && isLoaded"
        :style="{ paddingLeft: `${(depth + 1) * 14 + 20}px` }"
        class="py-1 text-[11px] text-base-content/40 italic"
      >
        (empty)
      </div>
      <FileTreeNode
        v-for="child in children"
        :key="child.path"
        :node="child"
        :sessionId="sessionId"
        :selectedPath="selectedPath"
        :commentedFiles="commentedFiles"
        :depth="depth + 1"
        :treeVersion="treeVersion"
        @select-file="(p) => emit('select-file', p)"
      />
    </div>
  </div>
</template>
