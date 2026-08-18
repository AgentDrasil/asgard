<script setup lang="ts">
import type { ChatMessage } from "../../types";

defineProps<{
  message: ChatMessage;
}>();
</script>

<template>
  <div class="chat chat-end min-w-0">
    <div
      class="chat-header text-[10px] uppercase font-bold text-base-content/40 mb-1 select-none flex items-center gap-1"
    >
      You
    </div>
    <div
      class="chat-bubble chat-bubble-primary text-primary-content border border-primary/20 text-sm leading-relaxed max-w-3xl shadow-sm font-sans whitespace-pre-wrap break-words [word-break:break-word] min-w-0"
    >
      {{ message.content }}
    </div>
  </div>
</template>
