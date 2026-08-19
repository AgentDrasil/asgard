<script setup lang="ts">
import { ref, watch, onMounted, onUnmounted } from "vue";
import { useRoute, useRouter } from "vue-router";
import Sidebar from "./components/Sidebar.vue";
import FileSearchModal from "./components/file/FileSearchModal.vue";
import { initPushNotifications } from "./lib/push";
import type { ActiveView } from "./types";

import { useAgents } from "./composables/useAgents";
import { useSessions } from "./composables/useSessions";
import { useSessionStore } from "./composables/useSessionStore";

const route = useRoute();
const router = useRouter();

const welcomePrompt = ref("");
const activeView = ref<ActiveView>("chat");
const isFileSearchOpen = ref(false);
const selectedFilePath = ref<string | null>(null);
const isFileTreeOpen = ref(true);
const chatInputText = ref("");
const currentGitRoot = ref("");
const isSidebarOpen = ref(typeof window !== "undefined" && window.innerWidth >= 768);
const isWorkspaceDetailsOpen = ref(true);
const isTerminalOpen = ref(false);
const isArtifactDrawerOpen = ref(false);
const isVCSSidebarOpen = ref(true);
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
    if (isTerminalOpen.value) {
      isTerminalOpen.value = false;
    } else {
      toggleTerminal("session");
    }
    return;
  }

  // Ctrl+P / Cmd+P (Open file search modal before input guard)
  if (
    ctrlKey &&
    !e.altKey &&
    !e.shiftKey &&
    (e.code === "KeyP" || e.key === "p" || e.key === "P")
  ) {
    e.preventDefault();
    e.stopPropagation();
    isFileSearchOpen.value = true;
    return;
  }

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
    if (activeView.value === "vcs") {
      isVCSSidebarOpen.value = !isVCSSidebarOpen.value;
    } else if (activeView.value === "file") {
      isFileTreeOpen.value = !isFileTreeOpen.value;
    } else {
      isArtifactDrawerOpen.value = !isArtifactDrawerOpen.value;
    }
  } else if (ctrlKey && e.altKey && !e.shiftKey && e.code === "KeyD") {
    e.preventDefault();
    e.stopPropagation();
    if (activeView.value === "vcs") {
      activeView.value = "chat";
    } else {
      const gitRootCandidate = activeSession.value?.runDir || selectedDir.value;
      if (gitRootCandidate) {
        currentGitRoot.value = gitRootCandidate;
      }
      activeView.value = "vcs";
    }
  } else if (ctrlKey && e.altKey && !e.shiftKey && e.code === "KeyF") {
    e.preventDefault();
    e.stopPropagation();
    if (activeView.value === "file") {
      activeView.value = "chat";
    } else {
      activeView.value = "file";
    }
  }
};

// 1. Agents Composable
const { agents, selectedAgentId, selectedDir, selectedModel, loadAgents } = useAgents();

// 2. Session Store (Single Source of Truth)
const store = useSessionStore({ agents, router });
const {
  sessions,
  activeSessionId,
  activeSession,
  activeAgent,
  messages,
  artifacts,
  workingAgentLabel,
  isInputBusy,
  openSession,
  closeSession,
  loadSessions,
  sendMessage,
  updateMessageReply,
} = store;

// 3. Sessions Navigation & Operations
const { handleSelectSession, handleNewChat, handleDeleteSession } = useSessions(
  route,
  router,
  agents,
  selectedAgentId,
  selectedDir,
  activeSessionId,
  loadSessions,
);

// Route watcher to open or close session
watch(
  () => route.params.id,
  async (newId) => {
    activeView.value = "chat";
    selectedFilePath.value = null;
    chatInputText.value = "";
    if (newId && typeof newId === "string") {
      await openSession(newId);
    } else {
      closeSession();
      welcomePrompt.value = "";
    }
  },
  { immediate: true },
);

onMounted(async () => {
  window.addEventListener("keydown", handleGlobalKeydown, true);
  initPushNotifications().catch((err) => console.error("Push notification init error:", err));
  await loadAgents();
  await loadSessions();
});

onUnmounted(() => {
  window.removeEventListener("keydown", handleGlobalKeydown, true);
});

const handleStartWelcomeChat = () => {
  if (welcomePrompt.value.trim()) {
    sendMessage(welcomePrompt.value, {
      selectedAgentId: selectedAgentId.value,
      selectedDir: selectedDir.value,
      selectedModel: selectedModel.value,
    });
  }
};

const handleSendMessage = (text: string) => {
  sendMessage(text, {
    selectedAgentId: selectedAgentId.value,
    selectedDir: selectedDir.value,
    selectedModel: selectedModel.value,
  });
};

const handleAskReplied = (msgId?: string, text?: string) => {
  if (msgId && text) {
    updateMessageReply(msgId, text);
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
          :loading="isInputBusy"
          :workingAgentLabel="workingAgentLabel"
          :messages="messages"
          :artifacts="artifacts"
          :activeAgent="activeAgent"
          :runDir="activeSession?.runDir || selectedDir"
          :sessionId="activeSessionId || ''"
          :gitRoot="currentGitRoot"
          :terminalType="terminalType"
          v-model:activeView="activeView"
          v-model:selectedFilePath="selectedFilePath"
          v-model:isFileTreeOpen="isFileTreeOpen"
          v-model:selectedAgentId="selectedAgentId"
          v-model:selectedDir="selectedDir"
          v-model:selectedModel="selectedModel"
          v-model:prompt="welcomePrompt"
          v-model:isDetailsOpen="isWorkspaceDetailsOpen"
          v-model:chatInputText="chatInputText"
          v-model:isTerminalOpen="isTerminalOpen"
          v-model:isArtifactDrawerOpen="isArtifactDrawerOpen"
          v-model:isVCSSidebarOpen="isVCSSidebarOpen"
          @submit="handleStartWelcomeChat"
          @send="handleSendMessage"
          @open-diff="
            (gitRoot) => {
              currentGitRoot = gitRoot;
              activeView = 'vcs';
            }
          "
          @close-diff="activeView = 'chat'"
          @open-search="isFileSearchOpen = true"
          @toggle-terminal="toggleTerminal('session')"
          @toggle-sidebar="toggleSidebar"
          @ask-replied="handleAskReplied"
        />
      </router-view>
    </main>

    <!-- File Search Modal (Ctrl+P / Cmd+P) -->
    <FileSearchModal
      :isOpen="isFileSearchOpen"
      :sessionId="activeSessionId || ''"
      :runDir="activeSession?.runDir || selectedDir"
      @close="isFileSearchOpen = false"
      @select-file="
        (path) => {
          selectedFilePath = path;
          activeView = 'file';
          isFileSearchOpen = false;
        }
      "
    />
  </div>
</template>
