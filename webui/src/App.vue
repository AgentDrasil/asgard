<script setup lang="ts">
import { ref, onMounted, watch } from "vue";
import { useRoute, useRouter } from "vue-router";
import { v4 as uuidv4 } from "uuid";
import Sidebar from "./components/Sidebar.vue";
import WelcomeScreen from "./components/WelcomeScreen.vue";
import ChatArea from "./components/ChatArea.vue";
import ChatInput from "./components/ChatInput.vue";
import DiffView from "./components/DiffView.vue";
import { getAgents, getSessions, getSession, deleteSessionFromLocal } from "./lib/api";
import { runAgentStream } from "./lib/agent";
import { Icon } from "@iconify/vue";
import type { AgentInfo, ChatSession, ChatMessage } from "./types";

const route = useRoute();
const router = useRouter();

const agents = ref<AgentInfo[]>([]);
const sessions = ref<ChatSession[]>([]);

const selectedAgentId = ref("");
const selectedDir = ref("");
const activeSessionId = ref<string | null>(null);
const welcomePrompt = ref("");

const messages = ref<ChatMessage[]>([]);
const loading = ref(false);
const isStreaming = ref(false);
const isSidebarOpen = ref(typeof window !== "undefined" && window.innerWidth >= 768);
const isWorkspaceDetailsOpen = ref(true);
// Incremented each time loadSessionData is called; lets in-flight loads detect they've been superseded.
let loadGen = 0;

const activeSession = ref<ChatSession | null>(null);
const activeAgent = ref<AgentInfo | null>(null);

// Diff view state
const showDiffView = ref(false);
const chatInputText = ref("");
const currentGitRoot = ref("");

const closeSidebarOnMobile = () => {
  if (typeof window !== "undefined" && window.innerWidth < 768) {
    isSidebarOpen.value = false;
  }
};

// mergeToolMessages collapses consecutive tool_call → tool_result pairs in
// a flat DB message list into a single activity bubble per tool invocation.
// Two messages are treated as a pair when:
//  - they are adjacent in the list
//  - the first has role "tool_call" and the second has role "tool_result"
//  - they share the same step_index (when present)
//
// The merged message keeps the tool_call role/activityType and concatenates
// the content as "call\nresult", separating distinct pairs with a blank line.
function mergeToolMessages(msgs: ChatMessage[]): ChatMessage[] {
  const out: ChatMessage[] = [];
  let i = 0;
  while (i < msgs.length) {
    const cur = msgs[i];
    const next = msgs[i + 1];
    const curIsCall = cur.role === "tool_call";
    const nextIsResult =
      next?.role === "tool_result" &&
      (cur.stepIndex == null || next.stepIndex == null || cur.stepIndex === next.stepIndex);

    if (curIsCall && nextIsResult) {
      // Check if the previous out entry is also a merged tool bubble we can append to
      const prev = out[out.length - 1];
      if (
        prev?.role === "tool_call" &&
        (prev?.activityType === "TOOL_CALL" || prev?.activityType === "TOOL")
      ) {
        prev.activityType = "TOOL";
        // Append this pair to the running tool log with a special delimiter
        prev.content =
          prev.content +
          "\n---TOOL_ITEM_DELIMITER---\n" +
          cur.content +
          "\n---TOOL_ITEM_DELIMITER---\n" +
          next.content;
      } else {
        out.push({
          ...cur,
          activityType: "TOOL",
          content: cur.content + "\n---TOOL_ITEM_DELIMITER---\n" + next.content,
        });
      }
      i += 2; // consumed both
    } else if (curIsCall) {
      // Handle isolated tool_call (e.g. status updates)
      const prev = out[out.length - 1];
      if (
        prev?.role === "tool_call" &&
        (prev?.activityType === "TOOL_CALL" || prev?.activityType === "TOOL")
      ) {
        prev.activityType = "TOOL";
        prev.content = prev.content + "\n---TOOL_ITEM_DELIMITER---\n" + cur.content;
      } else {
        out.push({
          ...cur,
          activityType: "TOOL",
        });
      }
      i += 1;
    } else {
      out.push(cur);
      i += 1;
    }
  }
  return out;
}

const loadSessionData = async (id: string) => {
  activeSessionId.value = id;
  const myGen = ++loadGen;
  const session = await getSession(id);
  // Bail out if a newer load has started or we're in the middle of a stream
  if (myGen !== loadGen || isStreaming.value) return;
  messages.value = mergeToolMessages(session?.messages ?? []);
};

const handleSelectSession = (id: string) => {
  if (route.params.id !== id) {
    router.push(`/chat/${id}`);
  }
  closeSidebarOnMobile();
};

const handleNewChat = () => {
  if (route.path !== "/newchat") {
    router.push("/newchat");
  }
  closeSidebarOnMobile();
};

// Watch route parameter changes to update active session
watch(
  () => route.params.id,
  async (newId) => {
    // Close diff view when switching sessions
    showDiffView.value = false;
    chatInputText.value = "";

    if (newId && typeof newId === "string") {
      // Don't reload while a stream is actively populating messages
      if (!isStreaming.value) {
        await loadSessionData(newId);
      } else {
        activeSessionId.value = newId;
      }
    } else {
      activeSessionId.value = null;
      messages.value = [];
      welcomePrompt.value = "";
    }
  },
  { immediate: true },
);

import { initPushNotifications } from "./lib/push";

// Initialize agents and sessions
onMounted(async () => {
  initPushNotifications().catch((err) => console.error("Push notification init error:", err));

  const loadedAgents = await getAgents();

  agents.value = loadedAgents;
  const mainAgents = loadedAgents.filter((a) => a.main_agent !== false);
  const initialAgent = mainAgents.length > 0 ? mainAgents[0] : loadedAgents[0];
  if (initialAgent) {
    selectedAgentId.value = initialAgent.id;
    if (initialAgent.run_dirs.length > 0) {
      selectedDir.value = initialAgent.run_dirs[0];
    }
  }

  const loadedSessions = await getSessions();
  sessions.value = loadedSessions;

  if (route.params.id && typeof route.params.id === "string") {
    await loadSessionData(route.params.id);
  }
});

// Update selected workspace directory when active agent changes
watch(selectedAgentId, (newAgentId) => {
  const currentAgent = agents.value.find((a) => a.id === newAgentId);
  if (currentAgent && currentAgent.run_dirs.length > 0) {
    selectedDir.value = currentAgent.run_dirs[0];
  } else {
    selectedDir.value = "";
  }
});

// Watch session select and update references
watch(
  [activeSessionId, sessions],
  ([newId]) => {
    if (newId) {
      const session = sessions.value.find((s) => s.chatID === newId) || null;
      activeSession.value = session;
      if (session) {
        activeAgent.value =
          agents.value.find(
            (a) => a.id === session.currentAgent || a.name === session.currentAgent,
          ) || null;
      }
    } else {
      activeSession.value = null;
      activeAgent.value = agents.value.find((a) => a.id === selectedAgentId.value) || null;
    }
  },
  { immediate: true, deep: true },
);

// Update document title dynamically based on active session title
watch(
  [activeSessionId, () => activeSession.value?.title],
  ([id, title]) => {
    if (id && title && title.trim()) {
      document.title = `${title.trim()}`;
    } else {
      document.title = "Asgard - New Chat";
    }
  },
  { immediate: true },
);

const handleDeleteSession = async (id: string) => {
  await deleteSessionFromLocal(id);
  const updated = await getSessions();
  sessions.value = updated;
  if (route.params.id === id || activeSessionId.value === id) {
    handleNewChat();
  }
};

const handleSendMessage = async (text: string) => {
  // Clear diff comments and close diff view on send
  chatInputText.value = "";

  let currentThreadId = activeSessionId.value;

  // Mark streaming as active BEFORE router.push so the route watcher doesn't
  // trigger loadSessionData and race-wipe our in-progress messages.
  isStreaming.value = true;
  loading.value = true;

  // Create new session if none exists
  if (!currentThreadId) {
    currentThreadId = uuidv4();
    activeSessionId.value = currentThreadId;
    await router.push(`/chat/${currentThreadId}`);
  }

  const currentSession = sessions.value.find((s) => s.chatID === currentThreadId) || {
    chatID: currentThreadId,
    currentAgent: selectedAgentId.value,
    runDir: selectedDir.value,
    title: "",
  };

  // 1. Add User Message
  const userMsgId = uuidv4();
  messages.value.push({
    id: userMsgId,
    role: "user",
    content: text,
    timestamp: Date.now(),
  });

  const runId = uuidv4();
  const assistantMsgId = uuidv4();
  const reasoningMsgId = `reasoning-${runId}`;

  // Placeholders for assistant response & reasoning details
  let hasAssistantMsg = false;
  let hasReasoningMsg = false;
  let toolLog = ""; // accumulated tool activity text for the single shared bubble

  const refreshSessionTitle = async (chatID: string) => {
    const sess = await getSession(chatID);
    if (sess && sess.title) {
      const idx = sessions.value.findIndex((s) => s.chatID === chatID);
      if (idx > -1) {
        sessions.value[idx] = { ...sessions.value[idx], title: sess.title };
      }
    }
  };

  // Schedule a title refresh fallback shortly after stream initiation if title is empty
  if (!currentSession.title) {
    setTimeout(() => refreshSessionTitle(currentThreadId), 1500);
  }

  // Look up agent ID (handle case where currentAgent stored agent name vs agent ID)
  const matchedAgent = agents.value.find(
    (a) => a.id === currentSession.currentAgent || a.name === currentSession.currentAgent,
  );
  const targetAgentId = matchedAgent ? matchedAgent.id : currentSession.currentAgent;

  await runAgentStream(
    targetAgentId,
    {
      prompt: text,
      runDir: currentSession.runDir || selectedDir.value,
      threadId: currentThreadId,
      runId,
      userMsgId,
    },
    {
      onText: (textContent, inputTokens, maxTokens) => {
        if (!hasAssistantMsg) {
          hasAssistantMsg = true;
          messages.value.push({
            id: assistantMsgId,
            role: "assistant",
            content: textContent,
            timestamp: Date.now(),
            ...(inputTokens ? { inputTokens } : {}),
            ...(maxTokens ? { maxTokens } : {}),
          });
          if (!currentSession.title) {
            refreshSessionTitle(currentThreadId);
          }
        } else {
          messages.value = messages.value.map((m) =>
            m.id === assistantMsgId
              ? {
                  ...m,
                  content: textContent,
                  ...(inputTokens ? { inputTokens } : {}),
                  ...(maxTokens ? { maxTokens } : {}),
                }
              : m,
          );
        }
      },
      onStatus: (statusText, entryType, _state, metadata) => {
        if (!statusText) return;

        const agentName =
          (metadata?.["agent_name"] as string) || activeAgent.value?.name || "Agent";

        if (entryType === "ask_user") {
          const askMsgId = (metadata?.["message_id"] as string) || `ask-${Date.now()}`;
          const exists = messages.value.some((m) => m.id === askMsgId);
          if (!exists) {
            messages.value.push({
              id: askMsgId,
              role: "ask_user",
              content: statusText,
              agentName: agentName,
              timestamp: Date.now(),
            });
          }
          return;
        }

        // Accumulate all tool activity into a single shared log.
        // New tool invocations (tool_call) are separated from the previous pair
        // by a blank line; tool_result is appended directly on the next line.
        if (!toolLog) {
          toolLog = statusText;
        } else {
          toolLog += "\n---TOOL_ITEM_DELIMITER---\n" + statusText;
        }

        const exists = messages.value.some((m) => m.id === reasoningMsgId);
        if (!exists) {
          const bubble: ChatMessage = {
            id: reasoningMsgId,
            role: "tool_call",
            activityType: "TOOL",
            content: toolLog,
            agentName: agentName,
            timestamp: Date.now(),
          };
          if (hasAssistantMsg) {
            const assistantIdx = messages.value.findIndex((m) => m.id === assistantMsgId);
            if (assistantIdx > -1) {
              messages.value.splice(assistantIdx, 0, bubble);
            } else {
              messages.value.push(bubble);
            }
          } else {
            messages.value.push(bubble);
          }
          if (!hasReasoningMsg) hasReasoningMsg = true;
        } else {
          messages.value = messages.value.map((m) =>
            m.id === reasoningMsgId ? { ...m, content: toolLog } : m,
          );
        }
      },
      onError: async (err) => {
        messages.value.push({
          id: `error-${uuidv4()}`,
          role: "activity",
          activityType: "ERROR",
          content: err.message || "An execution error occurred.",
          timestamp: Date.now(),
        });
        isStreaming.value = false;
        loading.value = false;
      },
      onComplete: async () => {
        // Clear streaming flag before reloading so loadSessionData is allowed to run
        isStreaming.value = false;
        loading.value = false;
        await loadSessionData(currentThreadId);
        if (!currentSession.title) {
          await refreshSessionTitle(currentThreadId);
        }
        const updated = await getSessions();
        sessions.value = updated;
      },
    },
  );
};

const handleStartWelcomeChat = () => {
  if (welcomePrompt.value.trim()) {
    handleSendMessage(welcomePrompt.value);
  }
};

const toggleSidebar = () => {
  isSidebarOpen.value = !isSidebarOpen.value;
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
        @click="handleNewChat"
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
      @select-session="handleSelectSession"
      @new-chat="handleNewChat"
      @delete-session="handleDeleteSession"
      @toggle-sidebar="toggleSidebar"
    />

    <!-- Main Content Area -->
    <main class="flex-1 flex flex-col h-full bg-base-100 overflow-hidden min-w-0">
      <template v-if="activeSessionId">
        <!-- Diff View (replaces chat area when open) -->
        <DiffView
          v-if="showDiffView"
          :runDir="activeSession?.runDir || selectedDir"
          :gitRoot="currentGitRoot"
          v-model:chatInputText="chatInputText"
          @close="showDiffView = false"
        />
        <!-- Normal Chat Area -->
        <ChatArea
          v-else
          :messages="messages"
          :loading="loading"
          :activeAgent="activeAgent"
          :runDir="activeSession?.runDir || selectedDir"
          :sessionId="activeSessionId"
          v-model:isDetailsOpen="isWorkspaceDetailsOpen"
          @open-diff="
            (gitRoot) => {
              currentGitRoot = gitRoot;
              showDiffView = true;
            }
          "
        />
        <ChatInput @send="handleSendMessage" :loading="loading" v-model="chatInputText" />
      </template>
      <template v-else>
        <WelcomeScreen
          :agents="agents"
          v-model:selectedAgentId="selectedAgentId"
          v-model:selectedDir="selectedDir"
          v-model:prompt="welcomePrompt"
          @submit="handleStartWelcomeChat"
          :loading="loading"
        />
      </template>
    </main>
  </div>
</template>
