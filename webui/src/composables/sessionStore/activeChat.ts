import { watch } from "vue";
import type { ChatMessage } from "../../types";
import type { SessionState } from "./state";
import type { createConnection } from "./connection";

/**
 * The open chat: opening/closing it (with the loadGen guard), merging the
 * fetched snapshot with live events, optimistic ask_user replies, and the
 * derived bits (active agent, working label, document title).
 */
export function createActiveChat(
  state: SessionState,
  connection: ReturnType<typeof createConnection>,
) {
  const {
    agents,
    sessions,
    activeSessionId,
    activeSession,
    activeAgent,
    rawMessages,
    replaceMessage,
    artifacts,
    queuedMessages,
    isRunning,
    waitingSubagents,
    loading,
    workingAgentLabel,
    gens,
  } = state;
  const {
    events,
    fetchActiveSession,
    flushPendingSubagentArtifacts,
    clearPendingSubagentArtifacts,
  } = connection;

  const openSession = async (id: string) => {
    if (!id) return;
    const isNewSession = activeSessionId.value !== id;
    activeSessionId.value = id;
    const myGen = ++gens.load;

    if (isNewSession) {
      clearPendingSubagentArtifacts();
      activeSession.value = null;
      rawMessages.value = [];
      artifacts.value = [];
      queuedMessages.value = [];
      isRunning.value = false;
      waitingSubagents.value = false;
      loading.value = false;
      workingAgentLabel.value = null;
    }

    // Connect SSE stream immediately
    events.connect(id);

    const session = await fetchActiveSession(id);
    if (myGen !== gens.load) return;
    if (!session) flushPendingSubagentArtifacts();

    if (session) {
      activeSession.value = session;
      flushPendingSubagentArtifacts();
      queuedMessages.value = session.queuedMessages || [];

      const snapshotMsgs = session.messages || [];
      if (rawMessages.value.length === 0) {
        rawMessages.value = [...snapshotMsgs];
      } else {
        const existingMap = new Map(rawMessages.value.map((m) => [m.id, m]));
        const merged: ChatMessage[] = [];
        for (const sm of snapshotMsgs) {
          const existing = existingMap.get(sm.id);
          // Precedence: { ...sm, ...existing } ensures live state updates (such as replied status
          // or incremental streaming info) received from SSE during the initial connect-to-fetch
          // window are not overwritten by a slightly stale initial snapshot response.
          merged.push(existing ? { ...sm, ...existing } : sm);
          existingMap.delete(sm.id);
        }
        for (const remaining of existingMap.values()) {
          merged.push(remaining);
        }
        rawMessages.value = merged;
      }

      artifacts.value = session.artifacts ? [...session.artifacts] : [];
      const hasUnrepliedAskUser = rawMessages.value.some(
        (m) => m.role === "ask_user" && !m.replied,
      );
      const running = !hasUnrepliedAskUser && (isRunning.value || !!session.isRunning);
      isRunning.value = running;
      waitingSubagents.value = !running && (waitingSubagents.value || !!session.waitingSubagents);
      loading.value = running;
      if (!running) {
        workingAgentLabel.value = null;
      } else if (!workingAgentLabel.value) {
        const lastMsg = snapshotMsgs
          .slice()
          .reverse()
          .find((m) => m.role !== "user" && m.agentName);
        if (lastMsg?.agentName) {
          const matched = agents.value.find(
            (a) => a.id === lastMsg.agentName || a.name === lastMsg.agentName,
          );
          workingAgentLabel.value = matched?.name || lastMsg.agentName;
        }
      }

      const idx = sessions.value.findIndex((s) => s.chatID === id);
      if (idx > -1) {
        sessions.value[idx] = { ...sessions.value[idx], ...session };
      } else {
        sessions.value = [session, ...sessions.value];
      }
    } else {
      activeSession.value = null;
      rawMessages.value = [];
      artifacts.value = [];
      queuedMessages.value = [];
      isRunning.value = false;
      loading.value = false;
      workingAgentLabel.value = null;
    }
  };

  const closeSession = () => {
    activeSessionId.value = null;
    activeSession.value = null;
    rawMessages.value = [];
    artifacts.value = [];
    queuedMessages.value = [];
    isRunning.value = false;
    waitingSubagents.value = false;
    loading.value = false;
    workingAgentLabel.value = null;
    events.disconnect();
    // Invalidate any in-flight openSession so it cannot resurrect this state.
    gens.load++;
  };

  const updateMessageReply = (msgId: string, replyText: string) => {
    const existing = rawMessages.value.find((m) => m.id === msgId);
    if (existing) replaceMessage(msgId, { ...existing, replied: true, replyText });
    // Answering an ask_user resumes the suspended run without a new status
    // event, so optimistically flip the session back to running. The done
    // event still clears it when the run actually finishes.
    if (!isRunning.value) {
      gens.runState++;
      isRunning.value = true;
      loading.value = true;
      if (activeSession.value) {
        activeSession.value.isRunning = true;
      }
      const idx = sessions.value.findIndex((s) => s.chatID === activeSessionId.value);
      if (idx > -1) {
        sessions.value[idx] = { ...sessions.value[idx], isRunning: true };
      }
    }
  };

  // Sync activeAgent based on activeSession currentAgent
  watch(
    [activeSessionId, () => activeSession.value?.currentAgent, agents],
    () => {
      if (activeSession.value) {
        const currentAgentId = activeSession.value.currentAgent;
        const found = agents.value.find(
          (a) => a.id === currentAgentId || a.name === currentAgentId,
        );
        activeAgent.value = found || null;
      } else {
        activeAgent.value = null;
      }
    },
    { immediate: true },
  );

  // If workingAgentLabel is set to raw agentId and agents list becomes available, update to display name
  watch(
    [agents, workingAgentLabel],
    () => {
      if (workingAgentLabel.value && agents.value.length > 0) {
        const matched = agents.value.find(
          (a) => a.id === workingAgentLabel.value || a.name === workingAgentLabel.value,
        );
        if (matched && workingAgentLabel.value !== matched.name) {
          workingAgentLabel.value = matched.name;
        }
      }
    },
    { immediate: true },
  );

  // Sync document title
  watch(
    [activeSessionId, () => activeSession.value?.title],
    ([id, title]) => {
      if (typeof document !== "undefined") {
        if (id && title && title.trim()) {
          document.title = `${title.trim()}`;
        } else {
          document.title = "Asgard - New Chat";
        }
      }
    },
    { immediate: true },
  );

  return { openSession, closeSession, updateMessageReply };
}
