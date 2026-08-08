import { ref, watch, type Ref } from "vue";
import type { RouteLocationNormalizedLoaded, Router } from "vue-router";
import type { ChatSession, AgentInfo, ChatMessage } from "../types";
import { getSessions, getSession, deleteSessionFromLocal } from "../lib/api";
import { mergeToolMessages } from "../utils/messageUtils";

export function useSessions(
  route: RouteLocationNormalizedLoaded,
  router: Router,
  agents: Ref<AgentInfo[]>,
  selectedAgentId: Ref<string>,
  selectedDir: Ref<string>,
  isStreaming: Ref<boolean>,
  messages: Ref<ChatMessage[]>,
  welcomePrompt: Ref<string>,
  showDiffView: Ref<boolean>,
  chatInputText: Ref<string>,
) {
  const sessions = ref<ChatSession[]>([]);
  const activeSessionId = ref<string | null>(null);
  const activeSession = ref<ChatSession | null>(null);
  const activeAgent = ref<AgentInfo | null>(null);
  let loadGen = 0;

  const loadSessionData = async (id: string) => {
    activeSessionId.value = id;
    const myGen = ++loadGen;
    const session = await getSession(id);
    if (session) {
      activeSession.value = session;
      isStreaming.value = !!session.isRunning;
    }
    if (myGen !== loadGen) return;
    messages.value = mergeToolMessages(session?.messages ?? []);
  };

  const loadSessions = async () => {
    const loadedSessions = await getSessions();
    sessions.value = loadedSessions;
  };

  const handleSelectSession = (id: string, onSelect?: () => void) => {
    if (route.params.id !== id) {
      router.push(`/chat/${id}`);
    }
    if (onSelect) onSelect();
  };

  const handleNewChat = (onNewChat?: () => void, agentId?: string, runDir?: string) => {
    if (agentId) {
      const foundAgent = agents.value.find((a) => a.id === agentId || a.name === agentId);
      if (foundAgent) {
        selectedAgentId.value = foundAgent.id;
      }
    }
    if (runDir) {
      selectedDir.value = runDir;
    }
    if (route.path !== "/newchat") {
      router.push("/newchat");
    }
    if (onNewChat) onNewChat();
  };

  const handleDeleteSession = async (id: string) => {
    await deleteSessionFromLocal(id);
    const updated = await getSessions();
    sessions.value = updated;
    if (route.params.id === id || activeSessionId.value === id) {
      handleNewChat();
    }
  };

  // Watch route parameter changes to update active session
  watch(
    () => route.params.id,
    async (newId) => {
      showDiffView.value = false;
      chatInputText.value = "";

      if (newId && typeof newId === "string") {
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

  return {
    sessions,
    activeSessionId,
    activeSession,
    activeAgent,
    loadSessions,
    loadSessionData,
    handleSelectSession,
    handleNewChat,
    handleDeleteSession,
  };
}
