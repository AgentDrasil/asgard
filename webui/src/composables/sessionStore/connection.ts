import type {
  ChatSession,
  ChatMessage,
  SessionEvent,
  DirLockStatus,
  SubagentState,
} from "../../types";
import { getSession } from "../../lib/api";
import { useSessionEvents } from "../useSessionEvents";
import { mergeSubagentArtifacts } from "../../utils/artifactSources";
import type { SessionState } from "./state";

/**
 * Live connection: wraps useSessionEvents (the SSE stream), applies every
 * stream event to the shared state, and owns the snapshot refresh that
 * reconciles a re-fetched chat with what the stream delivered meanwhile.
 */
export function createConnection(state: SessionState) {
  const {
    agents,
    sessions,
    activeSessionId,
    activeSession,
    activeAgent,
    rawMessages,
    upsertMessage,
    artifacts,
    queuedMessages,
    isRunning,
    waitingSubagents,
    loading,
    workingAgentLabel,
    gens,
    mergeArtifactsList,
  } = state;

  const handleSessionMessageEvent = (ev: SessionEvent) => {
    if (!ev.message) return;
    if (ev.chatId && ev.chatId !== activeSessionId.value) return;
    const incoming = ev.message;

    upsertMessage(incoming);

    if (incoming.artifactFiles && incoming.artifactFiles.length > 0) {
      mergeArtifactsList(incoming.artifactFiles);
    }

    if (incoming.role === "ask_user") {
      // An ask_user pauses the run until the user replies. Once replied, the
      // suspended agent resumes in-place and the backend keeps the session
      // running (it is still in activeExecutions), yet no fresh isRunning=true
      // status event is emitted. Mirror that here so the composer returns to
      // queue mode as soon as the reply is broadcast.
      const resuming = !!incoming.replied;
      gens.runState++;
      isRunning.value = resuming;
      loading.value = resuming;
      workingAgentLabel.value = resuming
        ? incoming.agentName || activeAgent.value?.name || null
        : null;
      if (activeSession.value) {
        activeSession.value.isRunning = resuming;
      }
      const sIdx = sessions.value.findIndex((s) => s.chatID === ev.chatId);
      if (sIdx > -1) {
        sessions.value[sIdx] = { ...sessions.value[sIdx], isRunning: resuming };
      }
    } else if (incoming.agentName && incoming.role !== "user" && isRunning.value) {
      const matched = agents.value.find(
        (a) => a.id === incoming.agentName || a.name === incoming.agentName,
      );
      workingAgentLabel.value = matched?.name || incoming.agentName;
    }
  };

  const handleSessionStatusEvent = (ev: SessionEvent) => {
    const sIdx = ev.chatId ? sessions.value.findIndex((s) => s.chatID === ev.chatId) : -1;

    if (ev.payload && "dirLock" in ev.payload) {
      const dirLock = (ev.payload.dirLock as DirLockStatus) || undefined;
      if (activeSession.value && ev.chatId === activeSessionId.value) {
        activeSession.value.dirLock = dirLock;
      }
      if (sIdx > -1) {
        sessions.value[sIdx] = { ...sessions.value[sIdx], dirLock };
      }
    }

    if (ev.chatId && ev.chatId !== activeSessionId.value) {
      return;
    }

    if (ev.payload?.isRunning !== undefined) {
      gens.runState++;
      waitingSubagents.value = !ev.payload.isRunning && ev.payload.waitingSubagents === true;
      const hasUnrepliedAskUser = rawMessages.value.some(
        (m) => m.role === "ask_user" && !m.replied,
      );
      const running = !hasUnrepliedAskUser && !!ev.payload.isRunning;
      isRunning.value = running;
      loading.value = running;
      if (activeSession.value) {
        activeSession.value.isRunning = running;
      }
      if (sIdx > -1) {
        sessions.value[sIdx] = { ...sessions.value[sIdx], isRunning: running };
      }
    }

    const agentIdOrName = (ev.payload?.agent_name || ev.payload?.agent) as string | undefined;
    if (agentIdOrName && isRunning.value) {
      const matched = agents.value.find((a) => a.id === agentIdOrName || a.name === agentIdOrName);
      workingAgentLabel.value = matched?.name || agentIdOrName;
    }

    if (ev.payload?.isRunning === false || !isRunning.value) {
      workingAgentLabel.value = null;
      loading.value = false;
      isRunning.value = false;
    }
  };

  const handleSessionTitleEvent = (ev: SessionEvent) => {
    const title = ev.payload?.title as string | undefined;
    if (!title) return;
    if (activeSession.value && ev.chatId === activeSessionId.value) {
      activeSession.value.title = title;
    }
    const idx = sessions.value.findIndex((s) => s.chatID === ev.chatId);
    if (idx > -1) {
      sessions.value[idx] = { ...sessions.value[idx], title };
    }
  };

  const handleSessionArtifactEvent = (ev: SessionEvent) => {
    if (ev.chatId && ev.chatId !== activeSessionId.value) return;
    const files: string[] = Array.isArray(ev.payload?.artifacts)
      ? (ev.payload?.artifacts as string[])
      : typeof ev.payload?.artifact === "string"
        ? [ev.payload.artifact]
        : [];
    if (files.length > 0) {
      mergeArtifactsList(files);
    }
  };

  // handleSubagentArtifactEvent adds what a descendant subagent produced to the
  // active chat's list (published on the streams of all its ancestors).
  // Events that arrive while the session is still loading (the stream connects
  // before the session is fetched) are kept and applied once it is there.
  const MAX_PENDING_SUBAGENT_ARTIFACTS = 200;
  let pendingSubagentArtifacts: SessionEvent[] = [];
  // Session fetches in flight, by chat id: an event for the active chat that
  // arrives while its fetch runs may be missing from the fetched snapshot,
  // which would then replace the chat. A stale fetch of another chat (the user
  // switched meanwhile) does not hold back the active chat's events.
  const fetchesInFlight = new Map<string, number>();
  const fetchingActiveSession = () =>
    !!activeSessionId.value && (fetchesInFlight.get(activeSessionId.value) ?? 0) > 0;

  const fetchActiveSession = async (id: string) => {
    fetchesInFlight.set(id, (fetchesInFlight.get(id) ?? 0) + 1);
    let ok = false;
    try {
      const session = await getSession(id);
      ok = true;
      return session;
    } finally {
      const n = (fetchesInFlight.get(id) ?? 1) - 1;
      if (n > 0) fetchesInFlight.set(id, n);
      else fetchesInFlight.delete(id);
      // A failed fetch replaces nothing: apply what it held back. A successful
      // one is flushed by its caller after the snapshot is assigned.
      if (!ok && !fetchingActiveSession()) flushPendingSubagentArtifacts();
    }
  };

  const applySubagentArtifactEvent = (ev: SessionEvent) => {
    if (!activeSession.value) return;
    activeSession.value.subagentArtifacts = mergeSubagentArtifacts(
      activeSession.value.subagentArtifacts,
      ev.payload,
    );
  };

  const clearPendingSubagentArtifacts = () => {
    pendingSubagentArtifacts = [];
  };

  const flushPendingSubagentArtifacts = () => {
    const pending = pendingSubagentArtifacts;
    pendingSubagentArtifacts = [];
    for (const ev of pending) {
      if (!ev.chatId || ev.chatId === activeSessionId.value) applySubagentArtifactEvent(ev);
    }
  };

  const handleSubagentArtifactEvent = (ev: SessionEvent) => {
    if (ev.chatId && ev.chatId !== activeSessionId.value) return;
    if (!activeSession.value || fetchingActiveSession()) {
      if (pendingSubagentArtifacts.length < MAX_PENDING_SUBAGENT_ARTIFACTS) {
        pendingSubagentArtifacts.push(ev);
      }
      return;
    }
    applySubagentArtifactEvent(ev);
  };

  const handleSessionDoneEvent = (ev: SessionEvent) => {
    if (ev.chatId === activeSessionId.value) {
      gens.runState++;
      isRunning.value = false;
      waitingSubagents.value = false;
      loading.value = false;
      workingAgentLabel.value = null;
      if (activeSession.value) {
        activeSession.value.isRunning = false;
      }
    }
    const idx = sessions.value.findIndex((s) => s.chatID === ev.chatId);
    if (idx > -1) {
      sessions.value[idx] = { ...sessions.value[idx], isRunning: false };
    }
  };

  const handleSessionQueueEvent = (ev: SessionEvent) => {
    if (ev.chatId && ev.chatId !== activeSessionId.value) return;
    if (Array.isArray(ev.payload?.queue)) {
      queuedMessages.value = ev.payload.queue;
    }
  };

  // refreshActiveSnapshot refetches the open chat and merges it with what the
  // live stream delivered meanwhile: a message that changed (or arrived) while
  // the fetch ran is newer than the snapshot and wins; one untouched since the
  // fetch began is taken from the snapshot (it may carry updates missed during
  // the gap). Optimistic user messages the server has not echoed are kept, and
  // a running state set by a status/done event after the fetch began wins.
  const refreshActiveSnapshot = async (chatId: string) => {
    if (chatId === activeSessionId.value && activeSessionId.value) {
      const startMsgs = new Map(rawMessages.value.map((m) => [m.id, m]));
      const startRunGen = gens.runState;
      const session = await fetchActiveSession(activeSessionId.value);
      if (!session || activeSessionId.value !== chatId) flushPendingSubagentArtifacts();
      if (session && activeSessionId.value === chatId) {
        activeSession.value = session;
        flushPendingSubagentArtifacts();
        queuedMessages.value = session.queuedMessages || [];
        const snapshotMsgs = session.messages || [];
        const current = new Map(rawMessages.value.map((m) => [m.id, m]));
        const merged: ChatMessage[] = snapshotMsgs.map((sm) => {
          const live = current.get(sm.id);
          return live && startMsgs.get(sm.id) !== live ? { ...sm, ...live } : sm;
        });
        const inSnapshot = new Set(snapshotMsgs.map((m) => m.id));
        for (const m of rawMessages.value) {
          if (inSnapshot.has(m.id)) continue;
          const liveOnly = !startMsgs.has(m.id);
          const optimistic = m.role === "user" && m.id.startsWith("user-");
          if (liveOnly || optimistic) merged.push(m);
        }
        rawMessages.value = merged;
        artifacts.value = session.artifacts ? [...session.artifacts] : [];
        const hasUnrepliedAskUser = rawMessages.value.some(
          (m) => m.role === "ask_user" && !m.replied,
        );
        if (gens.runState !== startRunGen) {
          // A live event settled the running state after the fetch began.
          loading.value = isRunning.value;
          if (!isRunning.value) workingAgentLabel.value = null;
          return;
        }
        const running = !hasUnrepliedAskUser && !!session.isRunning;
        isRunning.value = running;
        waitingSubagents.value = !running && !!session.waitingSubagents;
        loading.value = running;
        if (!running) {
          workingAgentLabel.value = null;
        }
      }
    }
  };

  const handleSessionResyncEvent = (ev: SessionEvent) => refreshActiveSnapshot(ev.chatId);

  const subagentSeqs = new Map<string, number>();

  // handleSessionSubagentEvent applies a child's state change, published on
  // its parent's stream, to the session list. A child not known yet is fetched.
  const handleSessionSubagentEvent = (ev: SessionEvent) => {
    const p = ev.payload;
    const id = typeof p?.id === "string" ? p.id : "";
    if (!id || !ev.chatId) return;
    // Events are versioned per child: one that is not newer than the last
    // applied is stale (delivered out of order) and ignored.
    if (typeof p?.seq === "number") {
      const last = subagentSeqs.get(id);
      if (last !== undefined && p.seq <= last) return;
      subagentSeqs.set(id, p.seq);
    }
    const patch: Partial<ChatSession> = {
      parentChatId: ev.chatId,
      subagentState: p?.state as SubagentState,
      spawnLabel: typeof p?.label === "string" ? p.label : undefined,
      spawnReadonly: !!p?.readonly,
      isRunning: p?.state === "running" || p?.state === "queued",
    };
    const idx = sessions.value.findIndex((s) => s.chatID === id);
    if (idx > -1) {
      sessions.value[idx] = { ...sessions.value[idx], ...patch };
      return;
    }
    const placeholder: ChatSession = {
      chatID: id,
      title: "",
      currentAgent: typeof p?.agent === "string" ? p.agent : "",
      runDir: activeSession.value?.runDir || "",
      ...patch,
    };
    sessions.value = [placeholder, ...sessions.value];
    void getSession(id).then((full) => {
      if (!full) return;
      const i = sessions.value.findIndex((s) => s.chatID === id);
      if (i > -1) sessions.value[i] = { ...sessions.value[i], ...full, messages: undefined };
    });
  };

  const events = useSessionEvents({
    onMessage: handleSessionMessageEvent,
    onStatus: handleSessionStatusEvent,
    onTitle: handleSessionTitleEvent,
    onArtifact: handleSessionArtifactEvent,
    onDone: handleSessionDoneEvent,
    onResync: handleSessionResyncEvent,
    onQueue: handleSessionQueueEvent,
    onSubagent: handleSessionSubagentEvent,
    onSubagentArtifact: handleSubagentArtifactEvent,
    // A reconnect with no known resume point may have missed events (including
    // `done`); a resumable one is replayed or answered with `resync` instead.
    onOpen: ({ reconnected, resumed }) => {
      if (reconnected && !resumed && activeSessionId.value) {
        void refreshActiveSnapshot(activeSessionId.value);
      }
    },
  });
  const connectionState = events.state;
  // retryConnection restarts a stream that gave up (state "failed").
  const retryConnection = () => {
    if (activeSessionId.value) events.connect(activeSessionId.value);
  };

  return {
    events,
    connectionState,
    retryConnection,
    fetchActiveSession,
    refreshActiveSnapshot,
    flushPendingSubagentArtifacts,
    clearPendingSubagentArtifacts,
  };
}
