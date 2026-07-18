<script setup lang="ts">
import type { ChatSession } from "../types";

defineProps<{
  sessions: ChatSession[];
  activeSessionId: string | null;
}>();

const emit = defineEmits<{
  (e: "select-session", id: string): void;
  (e: "new-chat"): void;
  (e: "delete-session", id: string): void;
}>();
</script>

<template>
  <aside class="w-64 bg-base-300 border-r border-base-100 flex flex-col h-full shrink-0">
    <!-- Header / New Chat Button -->
    <div class="p-4 border-b border-base-100 flex justify-between items-center gap-2">
      <h1
        class="text-lg font-bold bg-gradient-to-r from-indigo-400 to-cyan-400 bg-clip-text text-transparent"
      >
        Asgard WebUI
      </h1>
      <button @click="emit('new-chat')" class="btn btn-sm btn-outline btn-primary">New Chat</button>
    </div>

    <!-- Sessions List -->
    <div class="flex-1 overflow-y-auto p-2 space-y-1">
      <div v-if="sessions.length === 0" class="text-xs text-base-content/50 text-center py-8">
        No active sessions
      </div>
      <div
        v-for="session in sessions"
        :key="session.chatID"
        @click="emit('select-session', session.chatID)"
        :class="[
          'group flex items-center justify-between px-3 py-2.5 rounded-lg cursor-pointer transition-all duration-200 text-sm font-medium',
          activeSessionId === session.chatID
            ? 'bg-primary text-primary-content shadow-md shadow-primary/10'
            : 'hover:bg-base-200 text-base-content/85',
        ]"
      >
        <span class="truncate pr-2 select-none">{{ session.title || "Untitled Chat" }}</span>

        <button
          @click.stop="emit('delete-session', session.chatID)"
          :class="[
            'btn btn-ghost btn-xs opacity-0 group-hover:opacity-100 transition-opacity p-1 min-h-0 h-6 w-6',
            activeSessionId === session.chatID
              ? 'text-primary-content hover:bg-white/20'
              : 'text-error hover:bg-error/10',
          ]"
          title="Delete session"
        >
          <svg
            xmlns="http://www.w3.org/2000/svg"
            class="h-4 w-4"
            fill="none"
            viewBox="0 0 24 24"
            stroke="currentColor"
          >
            <path
              stroke-linecap="round"
              stroke-linejoin="round"
              stroke-width="2"
              d="M19 7l-.867 12.142A2 2 0 0116.138 21H7.862a2 2 0 01-1.995-1.858L5 7m5 4v6m4-6v6m1-10V4a1 1 0 00-1-1h-4a1 1 0 00-1 1v3M4 7h16"
            />
          </svg>
        </button>
      </div>
    </div>

    <!-- Footer -->
    <div class="p-3 border-t border-base-100 bg-base-300 text-xs text-base-content/40 text-center">
      v2.0 (Vue + daisyUI)
    </div>
  </aside>
</template>
