<script setup lang="ts">
import { ref, onMounted } from "vue";
import type { ChatSession } from "../types";
import { Icon } from "@iconify/vue";
import { apiFetch } from "../lib/api";

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
const isReloading = ref(false);

const reloadApp = async () => {
  if (isReloading.value) return;
  isReloading.value = true;
  try {
    await apiFetch("/api/manage/reload", { method: "POST" });
  } catch (err) {
    console.error("Failed to reload via /api/manage/reload:", err);
  } finally {
    isReloading.value = false;
  }
};

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
      :class="['p-4 flex items-center gap-2 w-full', isOpen ? 'justify-between' : 'justify-center']"
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
        <Icon icon="mynaui:sidebar" class="h-5 w-5 fill-current" />
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
        <Icon icon="mynaui:edit-one" class="h-5 w-5 fill-current" />
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
            <Icon icon="mynaui:trash-one" class="h-4 w-4 fill-current" />
          </button>
        </div>
      </template>
    </div>

    <!-- Action Menu (Horizontal with icon only) -->
    <div
      class="px-3 py-1 flex items-center justify-around gap-1 w-full border-t border-base-100/50"
    >
      <button
        @click="reloadApp"
        class="btn btn-ghost btn-xs btn-circle text-base-content/70 hover:text-base-content"
        title="Reload (/api/manage/reload)"
        :disabled="isReloading"
      >
        <Icon
          icon="mynaui:refresh"
          :class="['h-5 w-5 fill-current', { 'animate-spin': isReloading }]"
        />
      </button>

      <button
        class="btn btn-ghost btn-xs btn-circle text-base-content/70 hover:text-base-content opacity-50 cursor-not-allowed"
        title="Terminal (TODO)"
        disabled
      >
        <Icon icon="mynaui:terminal" class="h-5 w-5 fill-current" />
      </button>
    </div>

    <!-- Footer / Theme Toggle -->
    <div
      :class="[
        'p-3 bg-base-300 flex items-center text-xs w-full border-t border-base-100',
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
        <Icon icon="mynaui:sun" class="swap-off fill-current w-5 h-5" />
        <!-- Moon icon (shown when light, click for dark) -->
        <Icon icon="mynaui:moon" class="swap-on fill-current w-5 h-5" />
      </label>
    </div>
  </aside>
</template>
