import { ref, type Ref } from "vue";
import type { Router } from "vue-router";
import type { AgentInfo, SendNowResult } from "../types";
import { t } from "../i18n";
import { useToast } from "./useToast";
import { createSessionState, MAX_QUEUED_MESSAGES } from "./sessionStore/state";
import { createConnection } from "./sessionStore/connection";
import { createActiveChat } from "./sessionStore/activeChat";
import { createQueue } from "./sessionStore/queue";
import { createSender } from "./sessionStore/send";
import { createSessionList } from "./sessionStore/list";

export { MAX_QUEUED_MESSAGES };

export interface SessionStoreOptions {
  agents?: Ref<AgentInfo[]>;
  router?: Router;
}

/**
 * Facade over the session store parts (see ./sessionStore): shared state, the
 * live connection, the open chat, the message queue, sending/stopping and the
 * session list. Call sites use this single object.
 */
export function useSessionStore(options: SessionStoreOptions = {}) {
  const { agents = ref<AgentInfo[]>([]), router } = options;

  const state = createSessionState(agents);
  const connection = createConnection(state);
  const chat = createActiveChat(state, connection);
  const queue = createQueue(state);
  const sender = createSender(state, connection, chat.closeSession, router);
  const list = createSessionList(state, chat.closeSession, router);

  return {
    sessions: state.sessions,
    activeSessionId: state.activeSessionId,
    activeSession: state.activeSession,
    activeAgent: state.activeAgent,
    rawMessages: state.rawMessages,
    messages: state.messages,
    artifacts: state.artifacts,
    queuedMessages: state.queuedMessages,
    canEnqueue: state.canEnqueue,
    isRunning: state.isRunning,
    waitingSubagents: state.waitingSubagents,
    loading: state.loading,
    workingAgentLabel: state.workingAgentLabel,
    isInputBusy: state.isInputBusy,
    isStoppingSession: sender.isStoppingSession,
    connectionState: connection.connectionState,
    retryConnection: connection.retryConnection,
    openSession: chat.openSession,
    closeSession: chat.closeSession,
    loadSessions: list.loadSessions,
    archiveSessionById: list.archiveSessionById,
    unlockSessionById: list.unlockSessionById,
    updateMessageReply: chat.updateMessageReply,
    sendMessage: sender.sendMessage,
    stopExecution: sender.stopExecution,
    editQueuedMessage: queue.editQueuedMessage,
    deleteQueuedMessage: queue.deleteQueuedMessage,
    sendingQueuedIds: queue.sendingQueuedIds,
    sendQueuedMessageNow: queue.sendQueuedMessageNow,
  };
}

export type SessionStore = ReturnType<typeof useSessionStore>;

// Turns a send-now outcome into the matching toast.
export function notifySendNowResult(res: SendNowResult | null) {
  if (!res) return;
  const toast = useToast();
  switch (res.kind) {
    case "injected":
      toast.success(t("chat.sendNowInjected"));
      break;
    case "not_injected":
      toast.info(
        res.reason === "not_supported"
          ? t("chat.sendNowNotSupported")
          : res.reason === "not_running"
            ? t("chat.sendNowNotRunning")
            : res.reason === "descendant_holds_checkout"
              ? t("chat.sendNowHeldByChild")
              : t("chat.sendNowFailed"),
      );
      break;
    case "gone":
      toast.info(t("chat.sendNowGone"));
      break;
    default:
      toast.error(t("chat.sendNowError"));
  }
}
