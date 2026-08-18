import type { Ref } from "vue";
import type { RouteLocationNormalizedLoaded, Router } from "vue-router";
import type { AgentInfo } from "../types";
import { deleteSessionFromLocal } from "../lib/api";

export function useSessions(
  route: RouteLocationNormalizedLoaded,
  router: Router,
  agents: Ref<AgentInfo[]>,
  selectedAgentId: Ref<string>,
  selectedDir: Ref<string>,
  activeSessionId: Ref<string | null>,
  loadSessions: () => Promise<void>,
) {
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
    await loadSessions();
    if (route.params.id === id || activeSessionId.value === id) {
      handleNewChat();
    }
  };

  return {
    handleSelectSession,
    handleNewChat,
    handleDeleteSession,
  };
}
