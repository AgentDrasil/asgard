<script setup lang="ts">
import { ref, onMounted, watch } from "vue";
import { useRoute, useRouter } from "vue-router";
import { v4 as uuidv4 } from "uuid";
import Sidebar from "./components/Sidebar.vue";
import WelcomeScreen from "./components/WelcomeScreen.vue";
import ChatArea from "./components/ChatArea.vue";
import ChatInput from "./components/ChatInput.vue";
import { getAgents, getSessions, getSession, deleteSessionFromLocal } from "./lib/api";
import { runAgentStream } from "./lib/agent";
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

const activeSession = ref<ChatSession | null>(null);
const activeAgent = ref<AgentInfo | null>(null);

const loadSessionData = async (id: string) => {
  activeSessionId.value = id;
  messages.value = [];
  const session = await getSession(id);
  if (session && session.messages) {
    messages.value = session.messages;
  }
};

const handleSelectSession = (id: string) => {
  if (route.params.id !== id) {
    router.push(`/chat/${id}`);
  }
};

const handleNewChat = () => {
  if (route.path !== "/newchat") {
    router.push("/newchat");
  }
};

// Watch route parameter changes to update active session
watch(
  () => route.params.id,
  async (newId) => {
    if (newId && typeof newId === "string") {
      await loadSessionData(newId);
    } else {
      activeSessionId.value = null;
      messages.value = [];
      welcomePrompt.value = "";
    }
  },
  { immediate: true },
);

// Initialize agents and sessions
onMounted(async () => {
  const loadedAgents = await getAgents();
  agents.value = loadedAgents;
  if (loadedAgents.length > 0) {
    selectedAgentId.value = loadedAgents[0].id;
    if (loadedAgents[0].run_dirs.length > 0) {
      selectedDir.value = loadedAgents[0].run_dirs[0];
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
watch(activeSessionId, (newId) => {
  if (newId) {
    const session = sessions.value.find((s) => s.chatID === newId) || null;
    activeSession.value = session;
    if (session) {
      activeAgent.value = agents.value.find((a) => a.id === session.currentAgent) || null;
    }
  } else {
    activeSession.value = null;
    activeAgent.value = agents.value.find((a) => a.id === selectedAgentId.value) || null;
  }
});

const handleDeleteSession = async (id: string) => {
  await deleteSessionFromLocal(id);
  const updated = await getSessions();
  sessions.value = updated;
  if (route.params.id === id || activeSessionId.value === id) {
    handleNewChat();
  }
};

const handleSendMessage = async (text: string) => {
  let currentThreadId = activeSessionId.value;

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

  loading.value = true;

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

  await runAgentStream(
    currentSession.currentAgent,
    {
      prompt: text,
      runDir: currentSession.runDir,
      threadId: currentThreadId,
      runId,
      userMsgId,
    },
    {
      onText: (textContent) => {
        if (!hasAssistantMsg) {
          // If reasoning is present, ensure assistant message is pushed after reasoning
          hasAssistantMsg = true;
          messages.value.push({
            id: assistantMsgId,
            role: "assistant",
            content: textContent,
            timestamp: Date.now(),
          });
          if (!currentSession.title) {
            refreshSessionTitle(currentThreadId);
          }
        } else {
          messages.value = messages.value.map((m) =>
            m.id === assistantMsgId ? { ...m, content: textContent } : m,
          );
        }
      },
      onStatus: (statusText, state) => {
        // Track reasoning steps inside the Thinking Process dropdown
        if (statusText) {
          if (!hasReasoningMsg) {
            // Push reasoning message before assistant message if assistant exists
            const reasoningObj: ChatMessage = {
              id: reasoningMsgId,
              role: "reasoning",
              content: statusText,
              timestamp: Date.now(),
            };
            if (hasAssistantMsg) {
              const assistantIdx = messages.value.findIndex((m) => m.id === assistantMsgId);
              if (assistantIdx > -1) {
                messages.value.splice(assistantIdx, 0, reasoningObj);
              } else {
                messages.value.push(reasoningObj);
              }
            } else {
              messages.value.push(reasoningObj);
            }
            hasReasoningMsg = true;
          } else {
            messages.value = messages.value.map((m) =>
              m.id === reasoningMsgId ? { ...m, content: statusText } : m,
            );
          }
        }

        // Add a line entry for step transition notifications
        if (state && state !== "running" && state !== "input-required" && statusText) {
          messages.value.push({
            id: `activity-${uuidv4()}`,
            role: "activity",
            activityType: "STEP",
            content: statusText,
            timestamp: Date.now(),
          });
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
        loading.value = false;
      },
      onComplete: async () => {
        loading.value = false;
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

const isSidebarOpen = ref(true);
const toggleSidebar = () => {
  isSidebarOpen.value = !isSidebarOpen.value;
};
</script>

<template>
  <div class="flex w-full h-full bg-base-100 overflow-hidden">
    <!-- Sidebar -->
    <Sidebar
      :isOpen="isSidebarOpen"
      :sessions="sessions"
      :activeSessionId="activeSessionId"
      @select-session="handleSelectSession"
      @new-chat="handleNewChat"
      @delete-session="handleDeleteSession"
      @toggle-sidebar="toggleSidebar"
    />

    <!-- Main Content Area -->
    <main class="flex-1 flex flex-col h-full bg-base-100 overflow-hidden">
      <template v-if="activeSessionId">
        <ChatArea
          :messages="messages"
          :loading="loading"
          :activeAgent="activeAgent"
          :runDir="activeSession?.runDir || selectedDir"
        />
        <ChatInput @send="handleSendMessage" :loading="loading" />
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
