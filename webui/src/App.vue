<script setup lang="ts">
import { ref, computed, watch, onMounted, onUnmounted } from "vue";
import { useRoute, useRouter } from "vue-router";
import { Icon } from "@iconify/vue";
import Sidebar from "./components/Sidebar.vue";
import FileSearchModal from "./components/file/FileSearchModal.vue";
import CommandPaletteModal from "./components/CommandPaletteModal.vue";
import QuotaModal from "./components/sidebar/QuotaModal.vue";
import ToastContainer from "./components/common/ToastContainer.vue";
import { initPushNotifications } from "./lib/push";
import { getSystemStatus, reloadAgents } from "./lib/api";
import { useToast } from "./composables/useToast";
import { useRestartFlow } from "./composables/useRestartFlow";
import type { ActiveView, Attachment, CommandItem } from "./types";
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
import { resolveGlobalAction } from "./utils/keybindingUtils";

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
  isRestarting,
  isRestartConfirmOpen,
  openRestartConfirm,
  closeRestartConfirm,
  triggerRestartWorkflow,
} = useRestartFlow();

const {
  currentOS,
  activeBindings,
  loadCustomKeybindings,
  toggleSidebarShortcut,
  toggleArtifactsShortcut,
  toggleDiffShortcut,
  toggleTerminalShortcut,
  toggleFileViewShortcut,
  searchFilesShortcut,
  newChatShortcut,
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
  const target = e.target as HTMLElement | null;
  const isInputFocused = Boolean(
    target && (/^(INPUT|TEXTAREA|SELECT)$/.test(target.tagName) || target.isContentEditable),
  );

  const action = resolveGlobalAction(
    e,
    activeBindings.value,
    isInputFocused,
    isTerminalOpen.value,
    currentOS.value,
  );

  if (!action) return;

  switch (action) {
    case "search_files": {
      const currentSessionId = activeSessionId.value || (route.params.id as string);
      if (!currentSessionId) {
        break;
      }
      isCommandPaletteOpen.value = false;
      isFileSearchOpen.value = true;
      break;
    }
    case "command_palette":
      isFileSearchOpen.value = false;
      isCommandPaletteOpen.value = true;
      break;
    case "open_terminal_session":
      toggleTerminal("session");
      break;
    case "close_terminal":
      isTerminalOpen.value = false;
      break;
    case "toggle_sidebar":
      toggleSidebar();
      break;
    case "toggle_artifacts":
      if (activeView.value === "vcs") {
        isVCSSidebarOpen.value = !isVCSSidebarOpen.value;
      } else if (activeView.value === "file") {
        isFileTreeOpen.value = !isFileTreeOpen.value;
      } else {
        isArtifactDrawerOpen.value = !isArtifactDrawerOpen.value;
      }
      break;
    case "toggle_diff":
      navigateToVcs();
      break;
    case "toggle_file_view":
      navigateToFiles();
      break;
    case "new_chat":
      handleNewChat(closeSidebarOnMobile);
      break;
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

const openFileSearch = () => {
  const sessionId = activeSessionId.value || (route.params.id as string);
  if (!sessionId) return;
  isFileSearchOpen.value = true;
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
  void loadCustomKeybindings();
  await Promise.all([loadAgents(), loadSessions()]);
});

onUnmounted(() => {
  window.removeEventListener("keydown", handleGlobalKeydown, true);
});

const handleStartWelcomeChat = (files?: File[]) => {
  if (welcomePrompt.value.trim()) {
    sendMessage(welcomePrompt.value, {
      selectedAgentId: selectedAgentId.value,
      selectedDir: selectedDir.value,
      selectedModel: selectedModel.value,
      pendingFiles: files,
    });
  }
};

const handleSendMessage = (text: string, attachments?: Attachment[]) => {
  sendMessage(text, {
    selectedAgentId: selectedAgentId.value,
    selectedDir: selectedDir.value,
    selectedModel: selectedModel.value,
    attachments,
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

const isReloadingAgents = ref(false);

const reloadApp = async () => {
  if (isReloadingAgents.value) return;
  isReloadingAgents.value = true;
  try {
    const result = await reloadAgents();
    if (result.success) {
      toast.success("Agent configuration reloaded successfully", {
        title: "Reload Success",
      });
      await Promise.all([loadAgents(), loadSessions()]);
    } else {
      toast.error(result.error || "Failed to reload agent configuration", {
        title: "Reload Error",
      });
    }
  } catch (err: any) {
    toast.error(err?.message || "Failed to reload agent configuration", {
      title: "Reload Error",
    });
  } finally {
    isReloadingAgents.value = false;
  }
};

const commandList = computed<CommandItem[]>(() => [
  {
    id: "open-settings",
    title: "Open Settings",
    icon: "mynaui:cog",
    action: () => router.push("/settings"),
  },
  {
    id: "open-keybindings",
    title: "Open Keyboard Shortcuts",
    icon: "material-symbols:keyboard-outline",
    action: () => router.push("/settings/keybindings"),
  },
  {
    id: "open-logs",
    title: "Open System Logs & Diagnostics",
    icon: "mynaui:terminal",
    action: () => router.push("/settings/logs"),
  },
  {
    id: "edit-config",
    title: "Open Config Editor",
    icon: "mynaui:cog-three",
    action: () => router.push("/settings/config"),
  },
  {
    id: "reload-agents",
    title: "Reload Agents",
    icon: "mynaui:refresh",
    action: () => reloadApp(),
  },
  {
    id: "restart-server",
    title: "Restart Server",
    icon: "mynaui:power",
    action: () => openRestartConfirm(),
  },
  {
    id: "search-files",
    title: "Search Files in Workspace",
    icon: "octicon:file-code-24",
    shortcut: searchFilesShortcut.value,
    action: () => {
      openFileSearch();
    },
  },
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
    shortcut: newChatShortcut.value,
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
      @toggle-terminal="() => toggleTerminal('sidebar')"
      @open-quota="isQuotaModalOpen = true"
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
          @open-search="openFileSearch"
          @toggle-terminal="toggleTerminal('session')"
          @toggle-sidebar="toggleSidebar"
          @ask-replied="handleAskReplied"
        />
      </router-view>
    </main>

    <!-- File Search Modal (triggered from Command Palette or action buttons) -->
    <FileSearchModal
      :isOpen="isFileSearchOpen"
      :sessionId="activeSessionId || ''"
      :runDir="activeSession?.runDir || selectedDir"
      @close="isFileSearchOpen = false"
      @select-file="
        (path, scope) => {
          selectedFilePath = path;
          isFileSearchOpen = false;
          const sessionId = activeSessionId || (route.params.id as string);
          if (sessionId) {
            router.push({
              path: buildFilesRoute(sessionId, path),
              query: scope ? { scope } : undefined,
            });
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

    <!-- Restart Confirmation Modal -->
    <Transition name="fade">
      <div
        v-if="isRestartConfirmOpen"
        class="fixed inset-0 bg-black/60 backdrop-blur-xs z-50 flex items-center justify-center p-4"
        tabindex="-1"
        @click.self="closeRestartConfirm"
        @keydown.esc="closeRestartConfirm"
      >
        <div
          class="bg-base-200 border border-base-100 rounded-2xl w-full max-w-md p-6 shadow-2xl space-y-4"
        >
          <div class="flex items-start gap-3">
            <div class="p-2.5 rounded-full bg-warning/10 text-warning shrink-0">
              <Icon icon="mynaui:danger" class="h-6 w-6" />
            </div>
            <div class="space-y-1">
              <h3 class="font-bold text-lg text-base-content">Confirm Server Restart?</h3>
              <p class="text-sm text-base-content/70 leading-relaxed">
                This will gracefully terminate the current Asgard backend process.
              </p>
            </div>
          </div>

          <div
            class="bg-base-300/60 rounded-xl p-3.5 border border-base-100/40 text-xs text-base-content/80 space-y-1.5"
          >
            <div class="font-semibold text-warning flex items-center gap-1.5">
              <Icon icon="mynaui:info-triangle" class="h-4 w-4 shrink-0" />
              <span>Prerequisites</span>
            </div>
            <p>
              Please ensure your Docker container is configured with an automatic restart policy
              (such as <code>--restart=always</code> or <code>--restart=unless-stopped</code>),
              otherwise the container will not restart after the process exits.
            </p>
            <p class="text-base-content/60 text-[11px]">
              The page will poll system status and automatically refresh once the server is back
              online.
            </p>
          </div>

          <div class="flex items-center justify-end gap-2 pt-2">
            <button
              @click="closeRestartConfirm"
              class="btn btn-ghost btn-sm"
              :disabled="isRestarting"
            >
              Cancel
            </button>
            <button
              @click="triggerRestartWorkflow"
              class="btn btn-error btn-sm gap-1.5"
              :disabled="isRestarting"
            >
              <Icon icon="mynaui:power" class="h-4 w-4" />
              <span>Confirm Restart</span>
            </button>
          </div>
        </div>
      </div>
    </Transition>

    <!-- Global Toast Container -->
    <ToastContainer />
  </div>
</template>
