<script setup lang="ts">
import { ref, onMounted, onUnmounted } from "vue";
import { useRoute, useRouter } from "vue-router";
import { useI18n } from "vue-i18n";
import type { ChatSession, AgentInfo } from "../types";
import { Icon } from "@iconify/vue";
import SessionList from "./sidebar/SessionList.vue";
import LanguageSelector from "./sidebar/LanguageSelector.vue";
import { useShortcuts } from "../composables/useShortcuts";

const { t } = useI18n();
const route = useRoute();
const router = useRouter();
const { toggleSidebarShortcut, toggleTerminalShortcut } = useShortcuts();

const props = withDefaults(
  defineProps<{
    sessions: ChatSession[];
    agents?: AgentInfo[];
    activeSessionId: string | null;
    isOpen?: boolean;
  }>(),
  {
    isOpen: true,
    agents: () => [],
  },
);

const emit = defineEmits<{
  (e: "select-session", id: string): void;
  (e: "new-chat", agentId?: string, runDir?: string): void;
  (e: "delete-session", id: string): void;
  (e: "archive-session", id: string): void;
  (e: "toggle-sidebar"): void;
  (e: "toggle-terminal"): void;
  (e: "open-quota"): void;
  (e: "open-session-search"): void;
}>();

const navigateToDashboard = () => {
  if (typeof window !== "undefined" && window.innerWidth < 768 && props.isOpen) {
    emit("toggle-sidebar");
  }
  router.push("/dashboard");
};

const viewMode = ref<"list" | "agent">("list");

const toggleViewMode = (mode: "list" | "agent") => {
  viewMode.value = mode;
  if (typeof localStorage !== "undefined" && localStorage) {
    localStorage.setItem("asgard_sidebar_view_mode", mode);
  }
};

// Resizable sidebar width logic
const DEFAULT_WIDTH = 256;
const MIN_WIDTH = 200;
const MAX_WIDTH = 500;

const sidebarWidth = ref(DEFAULT_WIDTH);
const isResizing = ref(false);

const startResize = (e: MouseEvent) => {
  e.preventDefault();
  isResizing.value = true;
  document.addEventListener("mousemove", handleMouseMove);
  document.addEventListener("mouseup", stopResize);
  document.body.style.userSelect = "none";
  document.body.style.cursor = "col-resize";
};

const handleMouseMove = (e: MouseEvent) => {
  if (!isResizing.value) return;
  const newWidth = Math.min(Math.max(e.clientX, MIN_WIDTH), MAX_WIDTH);
  sidebarWidth.value = newWidth;
};

const stopResize = () => {
  if (isResizing.value) {
    isResizing.value = false;
    if (typeof localStorage !== "undefined" && localStorage) {
      localStorage.setItem("asgard_sidebar_width", sidebarWidth.value.toString());
    }
    document.removeEventListener("mousemove", handleMouseMove);
    document.removeEventListener("mouseup", stopResize);
    document.body.style.userSelect = "";
    document.body.style.cursor = "";
  }
};

const navigateToSettings = () => {
  router.push("/settings");
};

onMounted(() => {
  if (typeof localStorage !== "undefined" && localStorage) {
    const savedViewMode = localStorage.getItem("asgard_sidebar_view_mode");
    if (savedViewMode === "list" || savedViewMode === "agent") {
      viewMode.value = savedViewMode;
    }

    const savedWidth = localStorage.getItem("asgard_sidebar_width");
    if (savedWidth) {
      const parsed = parseInt(savedWidth, 10);
      if (!isNaN(parsed) && parsed >= MIN_WIDTH && parsed <= MAX_WIDTH) {
        sidebarWidth.value = parsed;
      }
    }
  }
});

onUnmounted(() => {
  document.removeEventListener("mousemove", handleMouseMove);
  document.removeEventListener("mouseup", stopResize);
});
</script>

<template>
  <aside
    :style="isOpen ? { width: `${sidebarWidth}px` } : undefined"
    :class="[
      isOpen
        ? 'max-md:w-72 max-md:translate-x-0'
        : 'w-72 md:w-16 md:items-center max-md:-translate-x-full',
      'bg-base-300 border-r border-base-100 flex flex-col h-full shrink-0 relative',
      'max-md:fixed max-md:top-0 max-md:bottom-0 max-md:left-0 max-md:z-50 max-md:shadow-2xl md:relative',
      isResizing ? 'select-none transition-none' : 'transition-all duration-300',
    ]"
  >
    <!-- Resize Handle (Visible only when sidebar is open on desktop) -->
    <div
      v-if="isOpen"
      @mousedown="startResize"
      class="hidden md:block absolute right-0 top-0 bottom-0 w-1.5 cursor-col-resize hover:bg-primary/40 active:bg-primary z-30 transition-colors"
      :title="t('sidebar.dragToResize')"
    />

    <!-- Header / Toggle Sidebar Button -->
    <div
      :class="['p-4 flex items-center gap-2 w-full', isOpen ? 'justify-between' : 'justify-center']"
    >
      <h1
        v-if="isOpen"
        class="text-lg font-bold bg-gradient-to-r from-indigo-600 to-cyan-600 dark:from-indigo-400 dark:to-cyan-400 bg-clip-text text-transparent truncate cursor-pointer"
        @click="router.push('/newchat')"
      >
        Asgard
      </h1>
      <button
        @click="emit('toggle-sidebar')"
        class="btn btn-ghost btn-xs btn-square text-base-content/70 hover:text-base-content"
        :title="
          isOpen
            ? t('sidebar.collapseSidebar', { shortcut: toggleSidebarShortcut })
            : t('sidebar.expandSidebar', { shortcut: toggleSidebarShortcut })
        "
      >
        <Icon icon="mynaui:sidebar" class="h-5 w-5 fill-current" />
      </button>
    </div>

    <!-- New Chat Button (above view mode switch) -->
    <div :class="['px-2 pt-1 pb-1 w-full flex flex-col items-center']">
      <button
        @click="emit('new-chat')"
        :class="[
          'flex items-center gap-3 py-2.5 rounded-lg cursor-pointer transition-all duration-200 text-sm font-medium text-base-content/85 hover:bg-base-200',
          isOpen ? 'w-full px-3' : 'w-10 h-10 justify-center p-0',
        ]"
        :title="t('sidebar.newChat')"
      >
        <Icon icon="mynaui:edit-one" class="h-5 w-5 fill-current" />
        <span v-if="isOpen">{{ t("sidebar.newChat") }}</span>
      </button>

      <button
        @click="navigateToDashboard"
        :class="[
          'flex items-center gap-3 py-2.5 rounded-lg cursor-pointer transition-all duration-200 text-sm font-medium',
          route.path === '/dashboard'
            ? 'bg-primary/10 text-primary font-semibold'
            : 'text-base-content/85 hover:bg-base-200',
          isOpen ? 'w-full px-3' : 'w-10 h-10 justify-center p-0',
        ]"
        :title="isOpen ? undefined : t('sidebar.dashboard')"
      >
        <Icon icon="mynaui:kanban" class="h-5 w-5 fill-current shrink-0" />
        <span v-if="isOpen">{{ t("sidebar.dashboard") }}</span>
      </button>

      <button
        @click="emit('open-session-search')"
        :class="[
          'flex items-center gap-3 py-2.5 rounded-lg cursor-pointer transition-all duration-200 text-sm font-medium text-base-content/85 hover:bg-base-200',
          isOpen ? 'w-full px-3' : 'w-10 h-10 justify-center p-0',
        ]"
        :title="t('sidebar.searchSessions')"
      >
        <Icon icon="material-symbols:search" class="h-5 w-5 fill-current" />
        <span v-if="isOpen">{{ t("sidebar.searchSessions") }}</span>
      </button>
    </div>

    <!-- View Mode Switcher (List Mode vs Group by Agent Mode) -->
    <div v-if="isOpen" class="px-3 pb-2 w-full">
      <div class="join w-full bg-base-200/60 p-0.5 rounded-lg">
        <button
          @click="toggleViewMode('list')"
          :class="[
            'join-item btn btn-xs flex-1 border-none font-medium gap-1.5',
            viewMode === 'list'
              ? 'btn-primary shadow-xs'
              : 'btn-ghost text-base-content/70 hover:text-base-content',
          ]"
          :title="t('sidebar.listView')"
        >
          <Icon icon="mynaui:list-solid" class="h-4 w-4 fill-current" />
          <span>{{ t("sidebar.list") }}</span>
        </button>
        <button
          @click="toggleViewMode('agent')"
          :class="[
            'join-item btn btn-xs flex-1 border-none font-medium gap-1.5',
            viewMode === 'agent'
              ? 'btn-primary shadow-xs'
              : 'btn-ghost text-base-content/70 hover:text-base-content',
          ]"
          :title="t('sidebar.byAgentView')"
        >
          <Icon icon="mynaui:grid" class="h-4 w-4 fill-current" />
          <span>{{ t("sidebar.byAgent") }}</span>
        </button>
      </div>
    </div>

    <!-- Sessions List -->
    <div class="flex-1 overflow-y-auto p-2 space-y-1 w-full flex flex-col items-center">
      <template v-if="isOpen">
        <SessionList
          :sessions="sessions"
          :agents="agents"
          :active-session-id="activeSessionId"
          :view-mode="viewMode"
          @select-session="emit('select-session', $event)"
          @delete-session="emit('delete-session', $event)"
          @archive-session="emit('archive-session', $event)"
          @new-chat="(agentId, dir) => emit('new-chat', agentId, dir)"
        />
      </template>
    </div>

    <!-- Bottom Actions, Language Selector & Settings Entry -->
    <div class="p-2 border-t border-base-100 w-full flex flex-col items-center space-y-1">
      <button
        @click="emit('toggle-terminal')"
        :class="[
          'flex items-center gap-3 py-2 rounded-lg cursor-pointer transition-all duration-200 text-sm font-medium text-base-content/85 hover:bg-base-200',
          isOpen ? 'w-full px-3' : 'w-10 h-10 justify-center p-0',
        ]"
        :title="t('sidebar.terminalWithShortcut', { shortcut: toggleTerminalShortcut })"
      >
        <Icon icon="mynaui:terminal" class="h-5 w-5 fill-current shrink-0" />
        <span v-if="isOpen">{{ t("sidebar.terminal") }}</span>
      </button>

      <button
        @click="emit('open-quota')"
        :class="[
          'flex items-center gap-3 py-2 rounded-lg cursor-pointer transition-all duration-200 text-sm font-medium text-base-content/85 hover:bg-base-200',
          isOpen ? 'w-full px-3' : 'w-10 h-10 justify-center p-0',
        ]"
        :title="t('sidebar.usageAndQuota')"
      >
        <Icon icon="mynaui:chart-bar-one" class="h-5 w-5 fill-current shrink-0" />
        <span v-if="isOpen">{{ t("sidebar.usageAndQuota") }}</span>
      </button>

      <button
        @click="navigateToSettings"
        :class="[
          'flex items-center gap-3 py-2 rounded-lg cursor-pointer transition-all duration-200 text-sm font-medium',
          route.path.startsWith('/settings')
            ? 'bg-primary/10 text-primary font-semibold'
            : 'text-base-content/85 hover:bg-base-200',
          isOpen ? 'w-full px-3' : 'w-10 h-10 justify-center p-0',
        ]"
        :title="t('sidebar.settings')"
      >
        <Icon icon="mynaui:cog" class="h-5 w-5 fill-current shrink-0" />
        <span v-if="isOpen">{{ t("sidebar.settings") }}</span>
      </button>

      <!-- Language Selector -->
      <div v-if="isOpen" class="w-full pt-1">
        <LanguageSelector />
      </div>
    </div>
  </aside>
</template>
