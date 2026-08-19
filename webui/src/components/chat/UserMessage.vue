<script setup lang="ts">
import type { ChatMessage } from "../../types";
import { formatTimestamp } from "../../lib/format";

defineProps<{
  message: ChatMessage;
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
      <span class="uppercase font-bold">You</span>
    </div>
    <div
      class="chat-bubble chat-bubble-primary text-primary-content text-sm leading-relaxed max-w-3xl shadow-sm font-sans whitespace-pre-wrap break-words [word-break:break-word] min-w-0"
    >
      {{ message.content }}
    </div>
  </div>
</template>
