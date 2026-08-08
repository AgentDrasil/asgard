<script setup lang="ts">
import { ref, watch, onMounted, onUnmounted } from "vue";
import { useRoute, useRouter } from "vue-router";
import Sidebar from "./components/Sidebar.vue";
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
const isTerminalOpen = ref(false);
const isArtifactDrawerOpen = ref(false);
const terminalType = ref<"session" | "sidebar">("session");

const toggleTerminal = (type: "session" | "sidebar" = "session") => {
  if (isTerminalOpen.value && terminalType.value === type) {
    isTerminalOpen.value = false;
  } else {
    terminalType.value = type;
    isTerminalOpen.value = true;
  }
};

const handleGlobalKeydown = (e: KeyboardEvent) => {
  const isMac = typeof navigator !== "undefined" && /mac/i.test(navigator.platform);
  const ctrlKey = isMac ? e.metaKey : e.ctrlKey;

  if (ctrlKey && !e.shiftKey && (e.code === "Backquote" || e.key === "`")) {
    e.preventDefault();
    e.stopPropagation();
    // Toggle off whatever terminal is currently open (session or sidebar);
    // open the session terminal when none is open.
    if (isTerminalOpen.value) {
      isTerminalOpen.value = false;
    } else {
      toggleTerminal("session");
    }
    return;
  }

  // Guard: ignore editing shortcuts when focused on input fields or contenteditable elements
  const target = e.target as HTMLElement | null;
  if (target && (/^(INPUT|TEXTAREA|SELECT)$/.test(target.tagName) || target.isContentEditable)) {
    return;
  }

  if (ctrlKey && !e.altKey && !e.shiftKey && e.code === "KeyB") {
    e.preventDefault();
    e.stopPropagation();
    toggleSidebar();
  } else if (ctrlKey && e.altKey && !e.shiftKey && e.code === "KeyB") {
    e.preventDefault();
    e.stopPropagation();
    isArtifactDrawerOpen.value = !isArtifactDrawerOpen.value;
  } else if (ctrlKey && e.altKey && !e.shiftKey && e.code === "KeyD") {
    e.preventDefault();
    e.stopPropagation();
    if (!showDiffView.value) {
      const gitRootCandidate = activeSession.value?.runDir || selectedDir.value;
      if (gitRootCandidate) {
        currentGitRoot.value = gitRootCandidate;
      }
    }
    showDiffView.value = !showDiffView.value;
  }
};

// 1. Agents Composable
const { agents, selectedAgentId, selectedDir, selectedModel, loadAgents } = useAgents();

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
  selectedDir,
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
  selectedModel,
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
  window.addEventListener("keydown", handleGlobalKeydown, true);
  initPushNotifications().catch((err) => console.error("Push notification init error:", err));
  await loadAgents();
  await loadSessions();

  if (route.params.id && typeof route.params.id === "string") {
    await loadSessionData(route.params.id);
  }
});

onUnmounted(() => {
  window.removeEventListener("keydown", handleGlobalKeydown, true);
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
      @new-chat="(agentId, runDir) => handleNewChat(closeSidebarOnMobile, agentId, runDir)"
      @delete-session="handleDeleteSession"
      @toggle-sidebar="toggleSidebar"
      @toggle-terminal="toggleTerminal('sidebar')"
    />
    <!-- Main Content Area -->
    <main class="flex-1 flex flex-col h-full bg-base-100 overflow-hidden min-w-0">
      <router-view v-slot="{ Component }">
        <component
          :is="Component"
          :agents="agents"
          :loading="loading || isStreaming || (activeSession?.isRunning ?? false)"
          :messages="messages"
          :artifacts="activeSession?.artifacts || []"
          :activeAgent="activeAgent"
          :runDir="activeSession?.runDir || selectedDir"
          :sessionId="activeSessionId || ''"
          :showDiffView="showDiffView"
          :gitRoot="currentGitRoot"
          :terminalType="terminalType"
          v-model:selectedAgentId="selectedAgentId"
          v-model:selectedDir="selectedDir"
          v-model:selectedModel="selectedModel"
          v-model:prompt="welcomePrompt"
          v-model:isDetailsOpen="isWorkspaceDetailsOpen"
          v-model:chatInputText="chatInputText"
          v-model:isTerminalOpen="isTerminalOpen"
          v-model:isArtifactDrawerOpen="isArtifactDrawerOpen"
          @submit="handleStartWelcomeChat"
          @send="handleSendMessage"
          @open-diff="
            (gitRoot) => {
              currentGitRoot = gitRoot;
              showDiffView = true;
            }
          "
          @close-diff="showDiffView = false"
          @toggle-terminal="toggleTerminal('session')"
          @toggle-sidebar="toggleSidebar"
        />
      </router-view>
    </main>
  </div>
</template>
