<script setup lang="ts">
import { ref, watch, onMounted } from "vue";
import { useRoute, useRouter } from "vue-router";
import Sidebar from "./components/Sidebar.vue";
import { Icon } from "@iconify/vue";
import { initPushNotifications } from "./lib/push";

import { useAgents } from "./composables/useAgents";
import { useSessions } from "./composables/useSessions";
import { useChatStream } from "./composables/useChatStream";
import type { ChatMessage } from "./types";

const route = useRoute();
const router = useRouter();

const welcomePrompt = ref("");
const showDiffView = ref(false);
const chatInputText = ref("");
const currentGitRoot = ref("");
const isSidebarOpen = ref(typeof window !== "undefined" && window.innerWidth >= 768);
const isWorkspaceDetailsOpen = ref(true);

// 1. Agents Composable
const { agents, selectedAgentId, selectedDir, loadAgents } = useAgents();

// 2. Chat state shared between composables
const messages = ref<ChatMessage[]>([]);
const isStreamingRef = ref(false);

const {
  sessions,
  activeSessionId,
  activeSession,
  activeAgent,
  loadSessions,
  loadSessionData,
  handleSelectSession,
  handleNewChat,
  handleDeleteSession,
} = useSessions(
  route,
  router,
  agents,
  selectedAgentId,
  isStreamingRef,
  messages,
  welcomePrompt,
  showDiffView,
  chatInputText,
);

// 3. Chat Stream Composable
const { loading, isStreaming, handleSendMessage } = useChatStream(
  activeSessionId,
  sessions,
  agents,
  activeAgent,
  selectedAgentId,
  selectedDir,
  chatInputText,
  router,
  messages,
);

watch(
  isStreaming,
  (val) => {
    isStreamingRef.value = val;
  },
  { immediate: true },
);

onMounted(async () => {
  initPushNotifications().catch((err) => console.error("Push notification init error:", err));
  await loadAgents();
  await loadSessions();

  if (route.params.id && typeof route.params.id === "string") {
    await loadSessionData(route.params.id);
  }
});

const handleStartWelcomeChat = () => {
  if (welcomePrompt.value.trim()) {
    handleSendMessage(welcomePrompt.value);
  }
};

const toggleSidebar = () => {
  isSidebarOpen.value = !isSidebarOpen.value;
};

const closeSidebarOnMobile = () => {
  if (typeof window !== "undefined" && window.innerWidth < 768) {
    isSidebarOpen.value = false;
  }
};
</script>

<template>
  <div class="flex flex-col md:flex-row w-full h-[100dvh] bg-base-100 overflow-hidden relative">
    <!-- Mobile Top Navigation Header -->
    <header
      class="md:hidden flex items-center justify-between px-3 py-2.5 bg-base-300 border-b border-base-100 shrink-0 z-30"
    >
      <button
        @click="toggleSidebar"
        class="btn btn-ghost btn-xs btn-square text-base-content/80"
        title="Toggle Menu"
      >
        <Icon icon="mynaui:sidebar" class="h-5 w-5" />
      </button>
      <button
        @click="isWorkspaceDetailsOpen = !isWorkspaceDetailsOpen"
        class="flex items-center gap-1.5 text-sm font-semibold truncate max-w-[220px] px-2 py-1 rounded-md hover:bg-base-200/60 active:bg-base-200 transition-colors cursor-pointer select-none"
        title="Toggle Workspace Info"
      >
        <Icon :icon="activeAgent?.icon || 'fluent-color:bot-24'" class="h-4 w-4 shrink-0" />
        <span class="text-base-content font-bold truncate">
          {{ activeAgent?.name || "Coding Agent" }}
        </span>
        <Icon
          :icon="isWorkspaceDetailsOpen ? 'ep:arrow-up' : 'ep:arrow-down'"
          class="h-3.5 w-3.5 text-base-content/70 shrink-0"
        />
      </button>
      <button
        @click="handleNewChat(closeSidebarOnMobile)"
        class="btn btn-ghost btn-xs btn-square text-base-content/80"
        title="New Chat"
      >
        <Icon icon="mynaui:edit-one" class="h-5 w-5" />
      </button>
    </header>

    <!-- Mobile Overlay Backdrop -->
    <div
      v-if="isSidebarOpen"
      @click="toggleSidebar"
      class="md:hidden fixed inset-0 bg-black/50 z-40 transition-opacity"
    ></div>

    <!-- Sidebar -->
    <Sidebar
      :isOpen="isSidebarOpen"
      :sessions="sessions"
      :agents="agents"
      :activeSessionId="activeSessionId"
      @select-session="(id) => handleSelectSession(id, closeSidebarOnMobile)"
      @new-chat="handleNewChat(closeSidebarOnMobile)"
      @delete-session="handleDeleteSession"
      @toggle-sidebar="toggleSidebar"
    />

    <!-- Main Content Area -->
    <main class="flex-1 flex flex-col h-full bg-base-100 overflow-hidden min-w-0">
      <router-view v-slot="{ Component }">
        <component
          :is="Component"
          :agents="agents"
          :loading="loading"
          :messages="messages"
          :activeAgent="activeAgent"
          :runDir="activeSession?.runDir || selectedDir"
          :sessionId="activeSessionId || ''"
          :showDiffView="showDiffView"
          :gitRoot="currentGitRoot"
          v-model:selectedAgentId="selectedAgentId"
          v-model:selectedDir="selectedDir"
          v-model:prompt="welcomePrompt"
          v-model:isDetailsOpen="isWorkspaceDetailsOpen"
          v-model:chatInputText="chatInputText"
          @submit="handleStartWelcomeChat"
          @send="handleSendMessage"
          @open-diff="
            (gitRoot) => {
              currentGitRoot = gitRoot;
              showDiffView = true;
            }
          "
          @close-diff="showDiffView = false"
        />
      </router-view>
    </main>
  </div>
</template>
