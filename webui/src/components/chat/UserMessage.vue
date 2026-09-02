<script setup lang="ts">
import { Icon } from "@iconify/vue";
import type { ChatMessage } from "../../types";
import { formatTimestamp, formatFileSize } from "../../lib/format";
import { getFileIcon } from "../../utils/fileUtils";
import { getAttachmentUrl } from "../../lib/api";

defineProps<{
  message: ChatMessage;
  sessionId?: string | null;
}>();
</script>

<template>
  <div class="chat chat-end min-w-0">
    <div
      class="chat-header text-[10px] text-base-content/40 mb-1 select-none flex items-center gap-1.5 justify-end"
    >
      <span v-if="message.timestamp" class="font-normal font-mono">
        {{ formatTimestamp(message.timestamp) }}
      </span>
      <span class="uppercase font-bold">{{ $t("chat.you") }}</span>
    </div>
    <div
      class="chat-bubble chat-bubble-primary text-primary-content text-sm leading-relaxed max-w-3xl shadow-sm font-sans whitespace-pre-wrap break-words [word-break:break-word] min-w-0"
    >
      <div v-if="message.content">{{ message.content }}</div>

      <!-- Attached Files Section -->
      <div
        v-if="message.attachments && message.attachments.length > 0"
        class="flex flex-col gap-1.5"
        :class="{ 'mt-2.5 pt-2 border-t border-primary-content/20': message.content }"
      >
        <div
          v-for="att in message.attachments"
          :key="att.name"
          class="flex items-center gap-2 bg-black/15 hover:bg-black/25 rounded-lg px-2.5 py-1.5 transition-colors group text-xs font-mono select-none"
        >
          <Icon :icon="getFileIcon(undefined, att.name)" class="h-4 w-4 shrink-0" />
          <span class="truncate flex-1 font-medium" :title="att.name">
            {{ att.name }}
          </span>
          <span class="text-[11px] opacity-75 shrink-0">
            {{ formatFileSize(att.size) }}
          </span>
          <a
            v-if="sessionId"
            :href="getAttachmentUrl(sessionId, att.name)"
            target="_blank"
            download
            class="btn btn-ghost btn-xs btn-circle text-primary-content opacity-75 hover:opacity-100 hover:bg-black/20 shrink-0"
            :title="$t('chat.downloadAttachment', { name: att.name })"
          >
            <Icon icon="material-symbols:download" class="h-3.5 w-3.5" />
          </a>
        </div>
      </div>
    </div>
  </div>
</template>
