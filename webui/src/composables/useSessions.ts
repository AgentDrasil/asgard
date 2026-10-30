import { ref, type Ref } from "vue";
import type { RouteLocationNormalizedLoaded, Router } from "vue-router";
import type { AgentInfo } from "../types";
import { deleteSessionFromLocal, unlockSession } from "../lib/api";
import { i18n } from "../i18n";
import { useToast } from "./useToast";

export function useSessions(
  route: RouteLocationNormalizedLoaded,
  router: Router,
  agents: Ref<AgentInfo[]>,
  selectedAgentId: Ref<string>,
  selectedDir: Ref<string>,
  activeSessionId: Ref<string | null>,
  loadSessions: () => Promise<void>,
  archiveSessionById?: (id: string) => Promise<boolean>,
  unlockSessionById?: (id: string) => Promise<boolean>,
) {
  const toast = useToast();

  const handleSelectSession = (id: string, onSelect?: () => void) => {
    if (route.params.id !== id) {
      router.push(`/chat/${id}`);
    }
    if (onSelect) onSelect();
  };

  const handleNewChat = (onNewChat?: () => void, agentId?: string, runDir?: string) => {
    let effectiveAgent = agents.value.find(
      (a) => a.id === selectedAgentId.value || a.name === selectedAgentId.value,
    );
    if (agentId) {
      const foundAgent = agents.value.find((a) => a.id === agentId || a.name === agentId);
      if (foundAgent) {
        selectedAgentId.value = foundAgent.id;
        effectiveAgent = foundAgent;
      }
    }
    if (runDir) {
      selectedDir.value = runDir;
    } else {
      selectedDir.value = effectiveAgent?.run_dirs?.[0] || "";
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

  const handleArchiveSession = async (id: string) => {
    if (!id) return false;
    try {
      if (archiveSessionById) {
        const success = await archiveSessionById(id);
        if (success) {
          toast.success("Session archived successfully");
          return true;
        } else {
          toast.error("Failed to archive session, please try again");
          return false;
        }
      }
      return false;
    } catch (err: any) {
      console.error("handleArchiveSession error:", err);
      toast.error(err?.message || "Unexpected error while archiving");
      return false;
    }
  };

  const isUnlockingSession = ref(false);

  const handleUnlockSession = async (id: string): Promise<boolean> => {
    if (!id || isUnlockingSession.value) return false;
    isUnlockingSession.value = true;
    try {
      if (unlockSessionById) {
        const success = await unlockSessionById(id);
        if (success) {
          toast.success(i18n.global.t("dirlock.unlockSuccess"));
          return true;
        } else {
          toast.error(i18n.global.t("dirlock.unlockFailed"));
          return false;
        }
      } else {
        const res = await unlockSession(id);
        if (res.status === "ok" || res.status === "unlocked") {
          await loadSessions();
          toast.success(i18n.global.t("dirlock.unlockSuccess"));
          return true;
        }
        toast.error(i18n.global.t("dirlock.unlockFailed"));
        return false;
      }
    } catch (err: any) {
      console.error("handleUnlockSession error:", err);
      toast.error(err?.message || i18n.global.t("dirlock.unlockFailed"));
      return false;
    } finally {
      isUnlockingSession.value = false;
    }
  };

  return {
    handleSelectSession,
    handleNewChat,
    handleDeleteSession,
    handleArchiveSession,
    handleUnlockSession,
    isUnlockingSession,
  };
}
