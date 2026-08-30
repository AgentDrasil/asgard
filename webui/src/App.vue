<script setup lang="ts">
import { ref, computed, watch, onMounted, onUnmounted } from "vue";
import { useRoute, useRouter } from "vue-router";
import Sidebar from "./components/Sidebar.vue";
import FileSearchModal from "./components/file/FileSearchModal.vue";
import CommandPaletteModal from "./components/CommandPaletteModal.vue";
import QuotaModal from "./components/sidebar/QuotaModal.vue";
import ToastContainer from "./components/common/ToastContainer.vue";
import { initPushNotifications } from "./lib/push";
import { getSystemStatus } from "./lib/api";
import { useToast } from "./composables/useToast";
import type { ActiveView, CommandItem } from "./types";
import {
  resolveViewFromRoute,
  buildChatRoute,
  buildFilesRoute,
  buildVcsRoute,
} from "./utils/routeUtils";

import { useAgents } from "./composables/useAgents";
import { useSessions } from "./composables/useSessions";
import { useSessionStore } from "./composables/useSessionStore";
import { useShortcuts } from "./composables/useShortcuts";

const route = useRoute();
const router = useRouter();

const welcomePrompt = ref("");
const activeView = ref<ActiveView>("chat");
const isFileSearchOpen = ref(false);
const isCommandPaletteOpen = ref(false);
const isQuotaModalOpen = ref(false);
const selectedFilePath = ref<string | null>(null);
const selectedCommit = ref<string | null>(null);
const isFileTreeOpen = ref(true);
const chatInputText = ref("");
const currentGitRoot = ref("");
const isSidebarOpen = ref(typeof window !== "undefined" && window.innerWidth >= 768);
const isWorkspaceDetailsOpen = ref(true);
const isTerminalOpen = ref(false);
const isArtifactDrawerOpen = ref(false);
const isVCSSidebarOpen = ref(true);
const terminalType = ref<"session" | "sidebar">("session");

const {
  toggleSidebarShortcut,
  toggleArtifactsShortcut,
  toggleDiffShortcut,
  toggleTerminalShortcut,
  toggleFileViewShortcut,
} = useShortcuts();

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

  const isBareF1 =
    (e.code === "F1" || e.key === "F1") && !e.ctrlKey && !e.metaKey && !e.altKey && !e.shiftKey;

  const isCommandPaletteKey =
    isBareF1 ||
    (ctrlKey && e.shiftKey && !e.altKey && (e.code === "KeyP" || e.key === "P" || e.key === "p"));

  if (isCommandPaletteKey) {
    e.preventDefault();
    e.stopPropagation();
    isFileSearchOpen.value = false;
    isCommandPaletteOpen.value = true;
    return;
  }

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
    isCommandPaletteOpen.value = false;
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
    navigateToVcs();
  } else if (ctrlKey && e.altKey && !e.shiftKey && e.code === "KeyF") {
    e.preventDefault();
    e.stopPropagation();
    navigateToFiles();
  }
};

const navigateToVcs = () => {
  const sessionId = activeSessionId.value || (route.params.id as string);
  if (activeView.value === "vcs") {
    if (sessionId) {
      router.push(buildChatRoute(sessionId));
    } else {
      activeView.value = "chat";
    }
  } else {
    const gitRootCandidate = activeSession.value?.runDir || selectedDir.value;
    if (gitRootCandidate) {
      currentGitRoot.value = gitRootCandidate;
    }
    if (sessionId) {
      router.push(buildVcsRoute(sessionId, selectedCommit.value, selectedFilePath.value));
    } else {
      activeView.value = "vcs";
    }
  }
};

const navigateToFiles = () => {
  const sessionId = activeSessionId.value || (route.params.id as string);
  if (activeView.value === "file") {
    if (sessionId) {
      router.push(buildChatRoute(sessionId));
    } else {
      activeView.value = "chat";
    }
  } else {
    if (sessionId) {
      router.push(buildFilesRoute(sessionId, selectedFilePath.value));
    } else {
      activeView.value = "file";
    }
  }
};

const navigateToChat = () => {
  const sessionId = activeSessionId.value || (route.params.id as string);
  if (sessionId) {
    router.push(buildChatRoute(sessionId));
  } else {
    activeView.value = "chat";
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

let prevSessionId: string | null = null;

// Route watcher to synchronize session, view, and parameters
watch(
  [() => route.path, () => route.params],
  async () => {
    const rawId = route.params.id;
    const newSessionId = typeof rawId === "string" && rawId.trim() !== "" ? rawId : null;

    if (newSessionId !== prevSessionId) {
      if (newSessionId) {
        chatInputText.value = "";
        welcomePrompt.value = "";
        await openSession(newSessionId);
      } else {
        closeSession();
        welcomePrompt.value = "";
        chatInputText.value = "";
      }
      prevSessionId = newSessionId;
    }

    const resolved = resolveViewFromRoute(
      route.path,
      route.params,
      route.name ? String(route.name) : null,
    );

    if (activeView.value !== resolved.activeView) {
      activeView.value = resolved.activeView;
    }
    if (selectedFilePath.value !== resolved.filePath) {
      selectedFilePath.value = resolved.filePath;
    }
    if (selectedCommit.value !== resolved.commitId) {
      selectedCommit.value = resolved.commitId;
    }
  },
  { immediate: true },
);

const toast = useToast();

const checkSystemStatus = async () => {
  try {
    const status = await getSystemStatus();
    if (!status) return;

    if (status.status === "degraded") {
      if (status.errors && status.errors.length > 0) {
        for (const err of status.errors) {
          toast.error(err, { title: "System Degraded" });
        }
      } else {
        toast.warning("System is currently running in degraded mode", {
          title: "System Degraded",
        });
      }
    }

    if (status.warnings && status.warnings.length > 0) {
      for (const warn of status.warnings) {
        toast.warning(warn, { title: "System Warning" });
      }
    }
  } catch (e) {
    console.error("checkSystemStatus error:", e);
  }
};

onMounted(async () => {
  window.addEventListener("keydown", handleGlobalKeydown, true);
  initPushNotifications().catch((err) => console.error("Push notification init error:", err));
  void checkSystemStatus();
  await Promise.all([loadAgents(), loadSessions()]);
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

const commandList = computed<CommandItem[]>(() => [
  {
    id: "toggle-left-panel",
    title: "Toggle Left Panel",
    icon: "mynaui:sidebar",
    shortcut: toggleSidebarShortcut.value,
    action: () => toggleSidebar(),
  },
  {
    id: "toggle-right-panel",
    title: "Toggle Right Panel",
    icon: "codicon:layout-sidebar-right",
    shortcut: toggleArtifactsShortcut.value,
    action: () => {
      if (activeView.value === "vcs") {
        isVCSSidebarOpen.value = !isVCSSidebarOpen.value;
      } else if (activeView.value === "file") {
        isFileTreeOpen.value = !isFileTreeOpen.value;
      } else {
        isArtifactDrawerOpen.value = !isArtifactDrawerOpen.value;
      }
    },
  },
  {
    id: "toggle-terminal-session",
    title: "Toggle Terminal (Current Session)",
    icon: "codicon:layout-panel",
    shortcut: toggleTerminalShortcut.value,
    action: () => toggleTerminal("session"),
  },
  {
    id: "toggle-terminal-global",
    title: "Toggle Terminal (Global)",
    icon: "mynaui:terminal",
    action: () => toggleTerminal("sidebar"),
  },
  {
    id: "switch-chat-view",
    title: "Switch to Chat View",
    icon: "material-symbols:chat-outline",
    action: () => navigateToChat(),
  },
  {
    id: "switch-vcs-view",
    title: "Switch to VCS View",
    icon: "octicon:git-branch-24",
    shortcut: toggleDiffShortcut.value,
    action: () => navigateToVcs(),
  },
  {
    id: "switch-files-view",
    title: "Switch to Files View",
    icon: "octicon:file-code-24",
    shortcut: toggleFileViewShortcut.value,
    action: () => navigateToFiles(),
  },
  {
    id: "new-chat",
    title: "New Chat",
    icon: "mynaui:edit-one",
    action: () => handleNewChat(closeSidebarOnMobile),
  },
  {
    id: "new-chat-same-current",
    title: "New Chat (Same with Current)",
    icon: "mynaui:copy",
    action: () =>
      handleNewChat(
        closeSidebarOnMobile,
        activeSession.value?.currentAgent || selectedAgentId.value,
        activeSession.value?.runDir || selectedDir.value,
      ),
  },
  {
    id: "show-quota",
    title: "Show Quota (Usage)",
    icon: "mynaui:chart-bar-one",
    action: () => {
      isQuotaModalOpen.value = true;
    },
  },
]);
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
          v-model:selectedCommit="selectedCommit"
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
            (gitRoot: string) => {
              currentGitRoot = gitRoot;
            }
          "
          @close-diff="navigateToChat"
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
          isFileSearchOpen = false;
          const sessionId = activeSessionId || (route.params.id as string);
          if (sessionId) {
            router.push(buildFilesRoute(sessionId, path));
          } else {
            activeView = 'file';
          }
        }
      "
    />

    <!-- Command Palette Modal (F1 / Ctrl+Shift+P / Cmd+Shift+P) -->
    <CommandPaletteModal
      :isOpen="isCommandPaletteOpen"
      :commands="commandList"
      @close="isCommandPaletteOpen = false"
    />

    <!-- Quota Modal -->
    <QuotaModal v-model="isQuotaModalOpen" />

    <!-- Global Toast Container -->
    <ToastContainer />
  </div>
</template>
