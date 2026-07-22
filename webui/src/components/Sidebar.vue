<script setup lang="ts">
import { ref, onMounted } from "vue";
import type { ChatSession } from "../types";

const props = withDefaults(
  defineProps<{
    sessions: ChatSession[];
    activeSessionId: string | null;
    isOpen?: boolean;
  }>(),
  {
    isOpen: true,
  },
);

const emit = defineEmits<{
  (e: "select-session", id: string): void;
  (e: "new-chat"): void;
  (e: "delete-session", id: string): void;
  (e: "toggle-sidebar"): void;
}>();

const currentTheme = ref("dark");

onMounted(() => {
  const saved = localStorage.getItem("theme");
  if (saved) {
    currentTheme.value = saved;
  } else {
    const docTheme = document.documentElement.getAttribute("data-theme");
    if (docTheme) {
      currentTheme.value = docTheme;
    }
  }
  document.documentElement.setAttribute("data-theme", currentTheme.value);
});

const toggleTheme = () => {
  currentTheme.value = currentTheme.value === "dark" ? "light" : "dark";
  document.documentElement.setAttribute("data-theme", currentTheme.value);
  localStorage.setItem("theme", currentTheme.value);
};
</script>

<template>
  <aside
    :class="[
      isOpen ? 'w-64' : 'w-16 items-center',
      'bg-base-300 border-r border-base-100 flex flex-col h-full shrink-0 transition-all duration-200',
    ]"
  >
    <!-- Header / Toggle Sidebar Button -->
    <div
      :class="[
        'p-4 border-b border-base-100 flex items-center gap-2 w-full',
        isOpen ? 'justify-between' : 'justify-center',
      ]"
    >
      <h1
        v-if="isOpen"
        class="text-lg font-bold bg-gradient-to-r from-indigo-600 to-cyan-600 dark:from-indigo-400 dark:to-cyan-400 bg-clip-text text-transparent truncate"
      >
        Asgard WebUI
      </h1>
      <button
        @click="emit('toggle-sidebar')"
        class="btn btn-ghost btn-xs btn-square text-base-content/70 hover:text-base-content"
        :title="isOpen ? 'Collapse Sidebar' : 'Expand Sidebar'"
      >
        <svg
          class="w-5 h-5"
          viewBox="0 0 24 24"
          fill="none"
          stroke="currentColor"
          stroke-width="2"
          stroke-linecap="round"
          stroke-linejoin="round"
        >
          <rect x="3" y="3" width="18" height="18" rx="4" />
          <line x1="9" y1="3" x2="9" y2="21" />
        </svg>
      </button>
    </div>

    <!-- Sessions List -->
    <div class="flex-1 overflow-y-auto p-2 space-y-1 w-full flex flex-col items-center">
      <!-- New Chat Button in List -->
      <button
        @click="emit('new-chat')"
        :class="[
          'flex items-center gap-3 py-2.5 rounded-lg cursor-pointer transition-all duration-200 text-sm font-medium text-base-content/85 hover:bg-base-200',
          isOpen ? 'w-full px-3' : 'w-10 h-10 justify-center p-0',
        ]"
        title="New chat"
      >
        <svg
          xmlns="http://www.w3.org/2000/svg"
          class="h-5 w-5 shrink-0 text-base-content/70"
          fill="none"
          viewBox="0 0 24 24"
          stroke="currentColor"
          stroke-width="2"
        >
          <path
            stroke-linecap="round"
            stroke-linejoin="round"
            d="M11 5H6a2 2 0 00-2 2v11a2 2 0 002 2h11a2 2 0 002-2v-5m-1.414-9.414a2 2 0 112.828 2.828L11.828 15H9v-2.828l8.586-8.586z"
          />
        </svg>
        <span v-if="isOpen">New chat</span>
      </button>

      <template v-if="isOpen">
        <div v-if="sessions.length === 0" class="text-xs text-base-content/50 text-center py-6">
          No active sessions
        </div>
        <div
          v-for="session in sessions"
          :key="session.chatID"
          @click="emit('select-session', session.chatID)"
          :class="[
            'group flex items-center justify-between px-3 py-2.5 rounded-lg cursor-pointer transition-all duration-200 text-sm font-medium w-full',
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
      </template>
    </div>

    <!-- Footer / Theme Toggle -->
    <div
      :class="[
        'p-3 border-t border-base-100 bg-base-300 flex items-center text-xs w-full',
        isOpen ? 'justify-between px-4' : 'justify-center',
      ]"
    >
      <span v-if="isOpen" class="text-base-content/70 font-medium select-none capitalize">
        {{ currentTheme }} mode
      </span>
      <label
        class="swap swap-rotate btn btn-ghost btn-xs btn-circle text-base-content/80 hover:text-base-content"
        title="Toggle Light/Dark Theme"
      >
        <input
          type="checkbox"
          class="theme-controller"
          :checked="currentTheme === 'light'"
          @change="toggleTheme"
        />
        <!-- Sun icon (shown when dark, click for light) -->
        <svg
          class="swap-off fill-current w-4 h-4"
          xmlns="http://www.w3.org/2000/svg"
          viewBox="0 0 24 24"
        >
          <path
            d="M5.64,17l-.71.71a1,1,0,0,0,0,1.41,1,1,0,0,0,1.41,0l.71-.71A1,1,0,0,0,5.64,17ZM5,12a1,1,0,0,0-1-1H3a1,1,0,0,0,0,2H4A1,1,0,0,0,5,12Zm7-7a1,1,0,0,0,1-1V3a1,1,0,0,0-2,0V4A1,1,0,0,0,12,5ZM5.64,7.05a1,1,0,0,0,.71.71,1,1,0,0,0,1.41,0,1,1,0,0,0,0-1.41l-.71-.71A1,1,0,0,0,5.64,7.05ZM18.36,17A1,1,0,0,0,17,18.36l.71.71a1,1,0,0,0,1.41,0,1,1,0,0,0,0-1.41ZM21,11H20a1,1,0,0,0,0,2h1a1,1,0,0,0,0-2Zm-9,7a1,1,0,0,0-1,1v1a1,1,0,0,0,2,0V19A1,1,0,0,0,12,18ZM18.36,7.05,19.07,6.34a1,1,0,0,0-1.41-1.41l-.71.71a1,1,0,0,0,0,1.41A1,1,0,0,0,18.36,7.05ZM12,6a6,6,0,1,0,6,6A6.07,6.07,0,0,0,12,6Z"
          />
        </svg>
        <!-- Moon icon (shown when light, click for dark) -->
        <svg
          class="swap-on fill-current w-4 h-4"
          xmlns="http://www.w3.org/2000/svg"
          viewBox="0 0 24 24"
        >
          <path
            d="M21.64,13a1,1,0,0,0-1.05-.14,8.05,8.05,0,0,1-3.37.73A8.15,8.15,0,0,1,9.08,5.49a8.59,8.59,0,0,1,.25-2A1,1,0,0,0,8,2.36,10.14,10.14,0,1,0,22,14.05A1,1,0,0,0,21.64,13Z"
          />
        </svg>
      </label>
    </div>
  </aside>
</template>
