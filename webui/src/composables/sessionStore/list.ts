import { archiveSession, unlockSession, getSessions } from "../../lib/api";
import type { Router } from "vue-router";
import { descendantIds } from "../../utils/sessionTree";
import type { SessionState } from "./state";

/** The session list: loading it, archiving and unlocking sessions. */
export function createSessionList(state: SessionState, closeSession: () => void, router?: Router) {
  const { sessions, activeSessionId, activeSession } = state;

  const loadSessions = async () => {
    const loaded = await getSessions();
    sessions.value = loaded;
  };

  const archiveSessionById = async (id: string): Promise<boolean> => {
    const success = await archiveSession(id);
    if (success) {
      const removed = descendantIds(sessions.value, id);
      sessions.value = sessions.value.filter((s) => !removed.has(s.chatID));
      if (activeSessionId.value && removed.has(activeSessionId.value)) {
        if (router) {
          await router.push("/dashboard");
        } else {
          closeSession();
        }
      }
    }
    return success;
  };

  const unlockSessionById = async (id: string): Promise<boolean> => {
    const res = await unlockSession(id);
    if (res.status === "ok" || res.status === "unlocked") {
      if (activeSession.value && activeSession.value.chatID === id) {
        activeSession.value.dirLock = undefined;
      }
      const sIdx = sessions.value.findIndex((s) => s.chatID === id);
      if (sIdx > -1) {
        sessions.value[sIdx] = { ...sessions.value[sIdx], dirLock: undefined };
      }
      return true;
    }
    return false;
  };

  return { loadSessions, archiveSessionById, unlockSessionById };
}
