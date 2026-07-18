<script setup lang="ts">
import { ref, onMounted, watch } from "vue";
import { v4 as uuidv4 } from "uuid";
import Sidebar from "./components/Sidebar.vue";
import WelcomeScreen from "./components/WelcomeScreen.vue";
import ChatArea from "./components/ChatArea.vue";
import ChatInput from "./components/ChatInput.vue";
import { getAgents, getSessions, saveSessionToLocal, deleteSessionFromLocal } from "./lib/api";
import { runAgentStream } from "./lib/agent";
import type { AgentInfo, ChatSession, ChatMessage } from "./types";

const agents = ref<AgentInfo[]>([]);
const sessions = ref<ChatSession[]>([]);

const selectedAgentId = ref("");
const selectedDir = ref("");
const activeSessionId = ref<string | null>(null);
const welcomePrompt = ref("");

const messages = ref<ChatMessage[]>([]);
const loading = ref(false);

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

const activeSession = ref<ChatSession | null>(null);
const activeAgent = ref<AgentInfo | null>(null);

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

const handleSelectSession = (id: string) => {
  activeSessionId.value = id;
  // Local placeholder logic: clear current messaging state (sessions store chatID & params, message list is live)
  messages.value = [];
};

const handleNewChat = () => {
  activeSessionId.value = null;
  messages.value = [];
  welcomePrompt.value = "";
};

const handleDeleteSession = async (id: string) => {
  await deleteSessionFromLocal(id);
  const updated = await getSessions();
  sessions.value = updated;
  if (activeSessionId.value === id) {
    handleNewChat();
  }
};

const handleSendMessage = async (text: string) => {
  let currentThreadId = activeSessionId.value;

  // Create new session if none exists
  if (!currentThreadId) {
    currentThreadId = uuidv4();
    const firstLine = text.split("\n")[0];
    const title = firstLine.length > 30 ? firstLine.substring(0, 30) + "..." : firstLine;

    const newSession: ChatSession = {
      chatID: currentThreadId,
      title,
      currentAgent: selectedAgentId.value,
      runDir: selectedDir.value,
    };

    await saveSessionToLocal(newSession);
    const updated = await getSessions();
    sessions.value = updated;
    activeSessionId.value = currentThreadId;
  }

  const currentSession = sessions.value.find((s) => s.chatID === currentThreadId) || {
    chatID: currentThreadId,
    currentAgent: selectedAgentId.value,
    runDir: selectedDir.value,
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
          messages.value.push({
            id: assistantMsgId,
            role: "assistant",
            content: textContent,
            timestamp: Date.now(),
          });
          hasAssistantMsg = true;
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
            messages.value.push({
              id: reasoningMsgId,
              role: "reasoning",
              content: statusText,
              timestamp: Date.now(),
            });
            hasReasoningMsg = true;
          } else {
            messages.value = messages.value.map((m) =>
              m.id === reasoningMsgId ? { ...m, content: statusText } : m,
            );
          }
        }

        // Add a line entry for step transition notifications
        if (state && state !== "running" && state !== "input-required") {
          messages.value.push({
            id: `activity-${uuidv4()}`,
            role: "activity",
            activityType: "STEP",
            content: `Status: ${state}`,
            timestamp: Date.now(),
          });
        }
      },
      onError: (err) => {
        messages.value.push({
          id: `error-${uuidv4()}`,
          role: "activity",
          activityType: "ERROR",
          content: err.message || "An execution error occurred.",
          timestamp: Date.now(),
        });
        loading.value = false;
      },
      onComplete: () => {
        loading.value = false;
      },
    },
  );
};

const handleStartWelcomeChat = () => {
  if (welcomePrompt.value.trim()) {
    handleSendMessage(welcomePrompt.value);
  }
};
</script>

<template>
  <div class="flex w-full h-full bg-base-100 overflow-hidden">
    <!-- Sidebar -->
    <Sidebar
      :sessions="sessions"
      :activeSessionId="activeSessionId"
      @select-session="handleSelectSession"
      @new-chat="handleNewChat"
      @delete-session="handleDeleteSession"
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
