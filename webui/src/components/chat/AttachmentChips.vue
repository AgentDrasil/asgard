<script setup lang="ts">
import { Icon } from "@iconify/vue";
import type { Attachment } from "../../types";
import { formatFileSize } from "../../lib/format";
import { getFileIcon } from "../../utils/fileUtils";

defineProps<{
  attachments: (Attachment | File | { name: string; size: number })[];
  isUploading?: boolean;
}>();

defineEmits<{
  (e: "remove", index: number): void;
}>();
</script>

<template>
  <div v-if="attachments.length > 0 || isUploading" class="flex flex-wrap items-center gap-2 px-1">
    <div
      v-for="(att, idx) in attachments"
      :key="att.name + idx"
      class="badge badge-lg gap-1.5 py-3.5 px-3 bg-base-200 border border-base-300 shadow-xs max-w-full text-xs font-mono select-none"
    >
      <Icon :icon="getFileIcon(undefined, att.name)" class="h-4 w-4 shrink-0" />
      <span class="truncate max-w-[160px] sm:max-w-[220px]" :title="att.name">
        {{ att.name }}
      </span>
      <span class="text-base-content/50 text-[11px]"> ({{ formatFileSize(att.size) }}) </span>
      <button
        type="button"
        @click="$emit('remove', idx)"
        class="btn btn-ghost btn-xs btn-circle ml-1 hover:bg-base-300"
        :title="$t('chat.removeAttachment')"
      >
        <Icon icon="material-symbols:close" class="h-3.5 w-3.5" />
      </button>
    </div>

    <!-- Uploading Indicator Badge -->
    <div
      v-if="isUploading"
      class="badge badge-lg gap-2 py-3.5 px-3 bg-base-200/80 border border-base-300 shadow-xs text-xs font-mono text-base-content/70 select-none"
    >
      <span class="loading loading-spinner loading-xs text-primary"></span>
      <span>{{ $t("chat.uploadingAttachment") }}</span>
    </div>
  </div>
</template>
