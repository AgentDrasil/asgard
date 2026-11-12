<script setup lang="ts">
import { ref, computed, watch, onMounted, onUnmounted } from "vue";
import { useRoute, useRouter } from "vue-router";
import { useI18n } from "vue-i18n";
import { Icon } from "@iconify/vue";
import Sidebar from "./components/Sidebar.vue";
import TerminalPanel from "./components/TerminalPanel.vue";
import FileSearchModal from "./components/file/FileSearchModal.vue";
import CommandPaletteModal from "./components/CommandPaletteModal.vue";
import SessionSearchModal from "./components/SessionSearchModal.vue";
import QuotaModal from "./components/sidebar/QuotaModal.vue";
import ToastContainer from "./components/common/ToastContainer.vue";
import { initPushNotifications } from "./lib/push";
import { getDirInfo, getSystemStatus, isAuthEnabled, logout } from "./lib/api";
import { useToast } from "./composables/useToast";
import { useUserProfile } from "./composables/useUserProfile";
import type { ActiveView, Attachment, CommandItem } from "./types";
import {
  resolveViewFromRoute,
  buildChatRoute,
  buildFilesRoute,
  buildVcsRoute,
} from "./utils/routeUtils";

import { useAgents } from "./composables/useAgents";
import { useRunners } from "./composables/useRunners";
import { useSessions } from "./composables/useSessions";
import { useSessionStore, notifySendNowResult } from "./composables/useSessionStore";
import { useShortcuts } from "./composables/useShortcuts";
import { resolveGlobalAction } from "./utils/keybindingUtils";

const { t } = useI18n();
const route = useRoute();
const router = useRouter();

const welcomePrompt = ref("");
const allowCrossSession = ref(false);
const worktree = ref(false);
const activeView = ref<ActiveView>("chat");
const isFileSearchOpen = ref(false);
const isCommandPaletteOpen = ref(false);
const isSessionSearchOpen = ref(false);
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

  // A modal <dialog> owns the keyboard (Esc is handled natively via cancel);
  // only the shortcuts that open/switch modals stay active over it.
  if (
    action !== "search_files" &&
    action !== "command_palette" &&
    document.querySelector("dialog[open]")
  ) {
    return;
  }

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

const openSessionSearch = () => {
  isSessionSearchOpen.value = true;
};

// 1. Agents Composable
const {
  runners,
  runnerMode,
  loading: runnersLoading,
  selectedRunnerId,
  runnerForSession,
  refreshRunners,
} = useRunners();
const hasNoRunnerAccess = computed(() => runnerMode.value && runners.value.length === 0);
const authEnabled = isAuthEnabled();
const handleLogout = () => logout();
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
  queuedMessages,
  isRunning,
  waitingSubagents,
  workingAgentLabel,
  isInputBusy,
  openSession,
  closeSession,
  loadSessions,
  archiveSessionById,
  unlockSessionById,
  sendMessage,
  stopExecution,
  updateMessageReply,
  editQueuedMessage,
  deleteQueuedMessage,
  sendingQueuedIds,
  sendQueuedMessageNow,
} = store;

// 3. Sessions Navigation & Operations
const {
  handleSelectSession,
  handleNewChat: rawHandleNewChat,
  handleDeleteSession,
  handleArchiveSession,
  handleUnlockSession,
  isUnlockingSession,
} = useSessions(
  route,
  router,
  agents,
  selectedAgentId,
  selectedDir,
  activeSessionId,
  loadSessions,
  archiveSessionById,
  unlockSessionById,
);

const handleNewChat = (closeSidebar?: () => void, agentId?: string, runDir?: string) => {
  if (hasNoRunnerAccess.value) {
    if (isAdmin.value) {
      router.push("/settings/runners");
    }
    return;
  }
  rawHandleNewChat(closeSidebar, agentId, runDir);
};

// Keep the VCS git root in sync with the effective workspace directory so the
// VCS entry point is available on direct loads (e.g. refresh while in file view).
const effectiveRunDir = computed(() => activeSession.value?.runDir || selectedDir.value);
watch(
  [effectiveRunDir, () => runnerForSession(activeSession.value)] as const,
  async ([dir, runner]) => {
    if (!dir) {
      currentGitRoot.value = "";
      return;
    }
    const info = await getDirInfo(dir, runner || undefined);
    if (
      effectiveRunDir.value === dir &&
      (runnerForSession(activeSession.value) || "") === (runner || "")
    ) {
      currentGitRoot.value = info.gitRoot || "";
    }
  },
  { immediate: true },
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
        if (activeSession.value?.currentAgent) {
          selectedAgentId.value = activeSession.value.currentAgent;
        }
        if (activeSession.value?.runDir) {
          selectedDir.value = activeSession.value.runDir;
        }
      } else {
        // Do NOT reset the agent/workspace selection here. `handleNewChat`
        // already sets the correct agent and run dir (explicit from the sidebar
        // or the agent default), and this watcher runs after it when navigating
        // from a chat to /newchat. Resetting here clobbered those values on the
        // first click, requiring a second click to apply them.
        closeSession();
        welcomePrompt.value = "";
        allowCrossSession.value = false;
        worktree.value = false;
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
const { profile, load: loadProfile } = useUserProfile();
const isAdmin = computed(() => !isAuthEnabled() || Boolean(profile.value?.isAdmin));

const checkSystemStatus = async () => {
  try {
    if (isAuthEnabled()) {
      await loadProfile();
      if (!isAdmin.value) return;
    }
    const status = await getSystemStatus();
    if (!status) return;

    if (status.status === "degraded") {
      if (status.errors && status.errors.length > 0) {
        for (const err of status.errors) {
          toast.error(err, {
            title: t("app.backendSystemDegraded"),
            translate: true,
            history: false,
          });
        }
      } else {
        toast.warning(t("app.systemRunningDegraded"), {
          title: t("app.backendSystemDegraded"),
          history: false,
        });
      }
    }

    if (status.warnings && status.warnings.length > 0) {
      for (const warn of status.warnings) {
        toast.warning(warn, {
          title: t("app.backendSystemWarning"),
          translate: true,
          history: false,
        });
      }
    }
  } catch (e) {
    console.error("checkSystemStatus error:", e);
  }
};

watch(
  [hasNoRunnerAccess, isAdmin, () => route.path],
  ([noAccess, admin, path]) => {
    if (noAccess && admin && !path.startsWith("/settings")) {
      void router.replace("/settings/runners");
    }
  },
  { immediate: true },
);

watch(hasNoRunnerAccess, (noAccess, prevNoAccess) => {
  if (!noAccess && prevNoAccess) {
    void checkSystemStatus();
    void loadAgents(runnerForSession(activeSession.value) || undefined);
    void loadSessions();
  }
});

onMounted(async () => {
  window.addEventListener("keydown", handleGlobalKeydown, true);
  initPushNotifications().catch((err) => console.error("Push notification init error:", err));
  void loadCustomKeybindings();
  if (isAuthEnabled()) {
    await loadProfile();
  }
  // Runners first: the agents list is scoped to the selected runner.
  await refreshRunners();
  if (hasNoRunnerAccess.value) {
    if (isAdmin.value && !route.path.startsWith("/settings")) {
      await router.replace("/settings/runners");
    }
  } else {
    void checkSystemStatus();
    await Promise.all([
      loadAgents(runnerForSession(activeSession.value) || undefined),
      loadSessions(),
    ]);
  }
});

// Subagent chats of the open chat, and the parent when it is a subagent itself.
const activeSubagents = computed(() =>
  activeSessionId.value
    ? sessions.value.filter((s) => s.parentChatId === activeSessionId.value)
    : [],
);
const activeParentSession = computed(() => {
  const parentId = activeSession.value?.parentChatId;
  return parentId ? sessions.value.find((s) => s.chatID === parentId) || null : null;
});
const killingSubagentIds = ref<string[]>([]);
const handleKillSubagent = async (id: string) => {
  if (killingSubagentIds.value.includes(id)) return;
  killingSubagentIds.value = [...killingSubagentIds.value, id];
  try {
    await stopExecution(id);
  } finally {
    killingSubagentIds.value = killingSubagentIds.value.filter((x) => x !== id);
  }
};

// Only list sessions bound to the selected runner (unbound legacy ones stay visible).
const visibleSessions = computed(() =>
  runnerMode.value && selectedRunnerId.value
    ? sessions.value.filter((s) => !s.runnerId || s.runnerId === selectedRunnerId.value)
    : sessions.value,
);

// Switching runner in the sidebar also switches the active session to that
// runner's most recent session, or to a new chat when it has none.
watch(selectedRunnerId, (runner, prev) => {
  if (!runner || runner === prev || !prev) return;
  const current = activeSession.value;
  if (current?.runnerId === runner) return;
  const target = sessions.value.find((s) => s.runnerId === runner);
  if (target) handleSelectSession(target.chatID);
  else handleNewChat();
});

// Re-scope the agents list when the effective runner changes (dropdown for a
// new chat, bound runner for an existing chat).
watch(
  () => runnerForSession(activeSession.value),
  (runner, prev) => {
    if (runner !== prev) void loadAgents(runner || undefined);
  },
);

onUnmounted(() => {
  window.removeEventListener("keydown", handleGlobalKeydown, true);
});

const handleStartWelcomeChat = (files?: File[]) => {
  if (welcomePrompt.value.trim()) {
    sendMessage(welcomePrompt.value, {
      selectedAgentId: selectedAgentId.value,
      selectedDir: selectedDir.value,
      selectedModel: selectedModel.value,
      allowCrossSession: allowCrossSession.value,
      // Worktrees live on a runner; without one the choice cannot apply.
      worktree: worktree.value && !!selectedRunnerId.value,
      runnerId: selectedRunnerId.value || undefined,
      pendingFiles: files,
    });
    selectedModel.value = "";
  }
};

const handleSendMessage = (text: string, attachments?: Attachment[]) => {
  sendMessage(text, {
    selectedAgentId: selectedAgentId.value,
    selectedDir: selectedDir.value,
    attachments,
  });
};

const handleRetryLastUserMessage = () => {
  for (let i = messages.value.length - 1; i >= 0; i--) {
    const m = messages.value[i];
    if (m.role === "user") {
      sendMessage(m.content, {
        selectedAgentId: selectedAgentId.value,
        selectedDir: selectedDir.value,
        attachments: m.attachments,
      });
      break;
    }
  }
};

const handleStopExecution = () => {
  void stopExecution();
};

const handleAskReplied = (msgId?: string, text?: string) => {
  if (msgId && text) {
    updateMessageReply(msgId, text);
  }
};

const handleEditQueued = (id: string, text: string) => {
  void editQueuedMessage(id, text);
};

const handleDeleteQueued = (id: string) => {
  void deleteQueuedMessage(id);
};

const handleSendNowQueued = async (id: string) => {
  notifySendNowResult(await sendQueuedMessageNow(id));
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
    id: "open-dashboard",
    title: t("commands.openDashboard"),
    icon: "mynaui:kanban",
    action: () => router.push("/dashboard"),
  },
  {
    id: "open-settings",
    title: t("commands.openSettings"),
    icon: "mynaui:cog",
    action: () => router.push("/settings"),
  },
  {
    id: "open-keybindings",
    title: t("commands.openKeybindings"),
    icon: "material-symbols:keyboard-outline",
    action: () => router.push("/settings/keybindings"),
  },
  {
    id: "open-logs",
    title: t("commands.openLogs"),
    icon: "mynaui:terminal",
    action: () => router.push("/settings/logs"),
  },
  {
    id: "search-sessions",
    title: t("commands.searchSessions"),
    icon: "material-symbols:search",
    action: () => openSessionSearch(),
  },
  {
    id: "search-files",
    title: t("commands.searchFiles"),
    icon: "octicon:file-code-24",
    shortcut: searchFilesShortcut.value,
    action: () => {
      openFileSearch();
    },
  },
  {
    id: "toggle-left-panel",
    title: t("commands.toggleLeftPanel"),
    icon: "mynaui:sidebar",
    shortcut: toggleSidebarShortcut.value,
    action: () => toggleSidebar(),
  },
  {
    id: "toggle-right-panel",
    title: t("commands.toggleRightPanel"),
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
    title: t("commands.toggleTerminalSession"),
    icon: "codicon:layout-panel",
    shortcut: toggleTerminalShortcut.value,
    action: () => toggleTerminal("session"),
  },
  {
    id: "toggle-terminal-global",
    title: t("commands.toggleTerminalGlobal"),
    icon: "mynaui:terminal",
    action: () => toggleTerminal("sidebar"),
  },
  {
    id: "switch-chat-view",
    title: t("commands.switchChatView"),
    icon: "material-symbols:chat-outline",
    action: () => navigateToChat(),
  },
  {
    id: "switch-vcs-view",
    title: t("commands.switchVcsView"),
    icon: "octicon:git-branch-24",
    shortcut: toggleDiffShortcut.value,
    action: () => navigateToVcs(),
  },
  {
    id: "switch-files-view",
    title: t("commands.switchFilesView"),
    icon: "octicon:file-code-24",
    shortcut: toggleFileViewShortcut.value,
    action: () => navigateToFiles(),
  },
  {
    id: "new-chat",
    title: t("commands.newChat"),
    icon: "mynaui:edit-one",
    shortcut: newChatShortcut.value,
    action: () => handleNewChat(closeSidebarOnMobile),
  },
  {
    id: "new-chat-same-current",
    title: t("commands.newChatSameCurrent"),
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
    title: t("commands.showQuota"),
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
      :sessions="visibleSessions"
      :agents="agents"
      :activeSessionId="activeSessionId"
      @select-session="(id) => handleSelectSession(id, closeSidebarOnMobile)"
      @new-chat="(agentId, runDir) => handleNewChat(closeSidebarOnMobile, agentId, runDir)"
      @delete-session="handleDeleteSession"
      @archive-session="handleArchiveSession"
      @toggle-sidebar="toggleSidebar"
      @toggle-terminal="() => toggleTerminal('sidebar')"
      @open-quota="isQuotaModalOpen = true"
      @open-session-search="openSessionSearch"
    />

    <!-- Main Content Area -->
    <main class="flex-1 flex flex-col h-full bg-base-100 overflow-hidden min-w-0">
      <!-- No Runner Access view for non-admin users -->
      <div
        v-if="hasNoRunnerAccess && !isAdmin"
        class="flex-1 flex flex-col items-center justify-center p-6 text-center select-none"
        data-testid="no-runner-access"
      >
        <div
          class="w-16 h-16 rounded-full bg-warning/10 text-warning flex items-center justify-center mb-4 shadow-sm"
        >
          <Icon icon="mynaui:server" class="w-8 h-8" />
        </div>
        <h2 class="text-xl font-bold text-base-content mb-2">
          {{ t("runners.noRunnerAccessTitle") }}
        </h2>
        <p class="text-sm text-base-content/60 max-w-md mb-6 leading-relaxed">
          {{ t("runners.noRunnerAccessDesc") }}
        </p>
        <div class="flex items-center gap-3">
          <button
            class="btn btn-sm btn-outline gap-2"
            :disabled="runnersLoading"
            @click="refreshRunners"
          >
            <Icon icon="mynaui:refresh" :class="['w-4 h-4', { 'animate-spin': runnersLoading }]" />
            <span>{{ t("runners.refresh") }}</span>
          </button>
          <button
            v-if="authEnabled"
            class="btn btn-sm btn-ghost gap-2 text-error"
            @click="handleLogout"
          >
            <Icon icon="mynaui:logout" class="w-4 h-4" />
            <span>{{ t("sidebar.logout") }}</span>
          </button>
        </div>
      </div>

      <router-view v-else v-slot="{ Component }">
        <component
          :is="Component"
          :agents="agents"
          :loading="isInputBusy"
          :workingAgentLabel="workingAgentLabel"
          :messages="messages"
          :artifacts="artifacts"
          :activeAgent="activeAgent"
          :runDir="activeSession?.runDir || selectedDir"
          :runnerId="activeSession?.runnerId"
          :sessionId="activeSessionId || ''"
          :gitRoot="currentGitRoot"
          :terminalType="terminalType"
          :queuedMessages="queuedMessages"
          :sendingQueuedIds="sendingQueuedIds"
          :isRunning="isRunning"
          :dirLock="activeSession?.dirLock"
          :isUnlocking="isUnlockingSession"
          :session="activeSession"
          :subagents="activeSubagents"
          :parentSession="activeParentSession"
          :waitingSubagents="waitingSubagents"
          :killingIds="killingSubagentIds"
          v-model:activeView="activeView"
          v-model:selectedFilePath="selectedFilePath"
          v-model:selectedCommit="selectedCommit"
          v-model:isFileTreeOpen="isFileTreeOpen"
          v-model:selectedAgentId="selectedAgentId"
          v-model:selectedDir="selectedDir"
          v-model:selectedModel="selectedModel"
          v-model:allowCrossSession="allowCrossSession"
          v-model:worktree="worktree"
          v-model:prompt="welcomePrompt"
          v-model:isDetailsOpen="isWorkspaceDetailsOpen"
          v-model:chatInputText="chatInputText"
          v-model:isTerminalOpen="isTerminalOpen"
          v-model:isArtifactDrawerOpen="isArtifactDrawerOpen"
          v-model:isVCSSidebarOpen="isVCSSidebarOpen"
          @submit="handleStartWelcomeChat"
          @send="handleSendMessage"
          @stop="handleStopExecution"
          @archive-session="handleArchiveSession"
          @unlock="handleUnlockSession(activeSessionId || '')"
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
          @edit-queued="handleEditQueued"
          @delete-queued="handleDeleteQueued"
          @send-now-queued="handleSendNowQueued"
          @retry="handleRetryLastUserMessage"
          @open-session="(id: string) => handleSelectSession(id, closeSidebarOnMobile)"
          @kill-subagent="handleKillSubagent"
        />
      </router-view>
      <!-- Hosted here (not in ChatView) so the sidebar terminal works on every page. -->
      <TerminalPanel
        :sessionId="activeSessionId || ''"
        :terminalType="terminalType"
        :isOpen="isTerminalOpen"
        @hide="isTerminalOpen = false"
        @close="isTerminalOpen = false"
      />
    </main>

    <!-- File Search Modal (triggered from Command Palette or action buttons) -->
    <FileSearchModal
      :isOpen="isFileSearchOpen"
      :sessionId="activeSessionId || ''"
      :runDir="activeSession?.runDir || selectedDir"
      @close="isFileSearchOpen = false"
      @select-file="
        (path, scope) => {
          const normalizedPath = path.startsWith('/') ? path.slice(1) : path;
          selectedFilePath = normalizedPath;
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

    <!-- Session Search Modal (triggered from Sidebar or Command Palette) -->
    <SessionSearchModal
      :isOpen="isSessionSearchOpen"
      @close="isSessionSearchOpen = false"
      @select-session="
        (session) => {
          handleSelectSession(session.chatID, closeSidebarOnMobile);
          isSessionSearchOpen = false;
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
