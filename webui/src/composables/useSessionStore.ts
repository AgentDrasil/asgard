import { ref, computed, watch, type Ref } from "vue";
import type { Router } from "vue-router";
import type { ChatSession, AgentInfo, ChatMessage, SessionEvent } from "../types";
import { getSession, getSessions, createSession, triggerAgentMessage } from "../lib/api";
import { useSessionEvents } from "./useSessionEvents";
import { mergeToolMessages } from "../utils/messageUtils";

export interface SessionStoreOptions {
  agents?: Ref<AgentInfo[]>;
  router?: Router;
}

export function useSessionStore(options: SessionStoreOptions = {}) {
  const { agents = ref<AgentInfo[]>([]), router } = options;

  const sessions = ref<ChatSession[]>([]);
  const activeSessionId = ref<string | null>(null);
  const activeSession = ref<ChatSession | null>(null);
  const activeAgent = ref<AgentInfo | null>(null);

  const rawMessages = ref<ChatMessage[]>([]);
  const messages = computed<ChatMessage[]>(() => mergeToolMessages(rawMessages.value));
  const artifacts = ref<string[]>([]);

  const isRunning = ref(false);
  const loading = ref(false);
  const workingAgentLabel = ref<string | null>(null);

  let loadGen = 0;

  const isInputBusy = computed<boolean>(() => isRunning.value || loading.value);

  const pushErrorMessage = (content: string, agentName?: string) => {
    if (!content) return;
    const exists = rawMessages.value.some((m) => m.role === "error" && m.content === content);
    if (exists) return;
    rawMessages.value = [
      ...rawMessages.value,
      {
        id: `error-${crypto.randomUUID()}`,
        role: "error",
        content,
        agentName: agentName || activeAgent.value?.name,
        timestamp: Date.now(),
      },
    ];
  };

  const mergeArtifactsList = (newFiles: string[]) => {
    if (!newFiles || newFiles.length === 0) return;
    const combined = Array.from(new Set([...artifacts.value, ...newFiles]));
    artifacts.value = combined;
    if (activeSession.value) {
      activeSession.value.artifacts = combined;
    }
  };

  const handleSessionMessageEvent = (ev: SessionEvent) => {
    if (!ev.message) return;
    if (ev.chatId && ev.chatId !== activeSessionId.value) return;
    const incoming = ev.message;

    const idx = rawMessages.value.findIndex((m) => m.id === incoming.id);
    if (idx > -1) {
      const updated = [...rawMessages.value];
      updated[idx] = { ...updated[idx], ...incoming };
      rawMessages.value = updated;
    } else {
      rawMessages.value = [...rawMessages.value, incoming];
    }

    if (incoming.artifactFiles && incoming.artifactFiles.length > 0) {
      mergeArtifactsList(incoming.artifactFiles);
    }
  };

  const handleSessionStatusEvent = (ev: SessionEvent) => {
    if (ev.chatId && ev.chatId !== activeSessionId.value) return;

    if (ev.payload?.isRunning !== undefined) {
      const running = !!ev.payload.isRunning;
      isRunning.value = running;
      loading.value = running;
      if (activeSession.value) {
        activeSession.value.isRunning = running;
      }
      const sIdx = sessions.value.findIndex((s) => s.chatID === ev.chatId);
      if (sIdx > -1) {
        sessions.value[sIdx] = { ...sessions.value[sIdx], isRunning: running };
      }
    }

    const agentIdOrName = (ev.payload?.agent_name || ev.payload?.agent) as string | undefined;
    if (agentIdOrName) {
      const matched = agents.value.find((a) => a.id === agentIdOrName || a.name === agentIdOrName);
      workingAgentLabel.value = matched?.name || agentIdOrName;
    }

    if (ev.payload?.isRunning === false) {
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

  const handleSessionDoneEvent = (ev: SessionEvent) => {
    if (ev.chatId === activeSessionId.value) {
      isRunning.value = false;
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

  const handleSessionResyncEvent = async (ev: SessionEvent) => {
    if (ev.chatId === activeSessionId.value && activeSessionId.value) {
      const session = await getSession(activeSessionId.value);
      if (session && activeSessionId.value === ev.chatId) {
        activeSession.value = session;
        const snapshotMsgs = session.messages || [];
        const pendingOptimistic = rawMessages.value.filter(
          (m) =>
            m.role === "user" &&
            m.id.startsWith("user-") &&
            !snapshotMsgs.some((sm) => sm.id === m.id),
        );
        rawMessages.value = [...snapshotMsgs, ...pendingOptimistic];
        artifacts.value = session.artifacts ? [...session.artifacts] : [];
        isRunning.value = !!session.isRunning;
        loading.value = !!session.isRunning;
      }
    }
  };

  const events = useSessionEvents({
    onMessage: handleSessionMessageEvent,
    onStatus: handleSessionStatusEvent,
    onTitle: handleSessionTitleEvent,
    onArtifact: handleSessionArtifactEvent,
    onDone: handleSessionDoneEvent,
    onResync: handleSessionResyncEvent,
  });

  const openSession = async (id: string) => {
    if (!id) return;
    const isNewSession = activeSessionId.value !== id;
    activeSessionId.value = id;
    const myGen = ++loadGen;

    if (isNewSession) {
      activeSession.value = null;
      rawMessages.value = [];
      artifacts.value = [];
      isRunning.value = false;
      loading.value = false;
      workingAgentLabel.value = null;
    }

    // Connect SSE stream immediately
    events.connect(id);

    const session = await getSession(id);
    if (myGen !== loadGen) return;

    if (session) {
      activeSession.value = session;

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
      const running = isRunning.value || !!session.isRunning;
      isRunning.value = running;
      loading.value = running;
      if (!running) {
        workingAgentLabel.value = null;
      }

      const idx = sessions.value.findIndex((s) => s.chatID === id);
      if (idx > -1) {
        sessions.value[idx] = { ...sessions.value[idx], ...session };
      }
    } else {
      activeSession.value = null;
      rawMessages.value = [];
      artifacts.value = [];
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
    isRunning.value = false;
    loading.value = false;
    workingAgentLabel.value = null;
    events.disconnect();
  };

  const loadSessions = async () => {
    const loaded = await getSessions();
    sessions.value = loaded;
  };

  const updateMessageReply = (msgId: string, replyText: string) => {
    rawMessages.value = rawMessages.value.map((m) =>
      m.id === msgId ? { ...m, replied: true, replyText } : m,
    );
  };

  const sendMessage = async (
    text: string,
    opts?: {
      selectedAgentId?: string;
      selectedDir?: string;
      selectedModel?: string;
    },
  ) => {
    let currentThreadId = activeSessionId.value;

    if (currentThreadId) {
      const activeSess = sessions.value.find((s) => s.chatID === currentThreadId);
      if (activeSess?.isRunning || isInputBusy.value) {
        return;
      }
    }

    loading.value = true;
    isRunning.value = true;
    workingAgentLabel.value = null;

    const userMsgId = `user-${crypto.randomUUID()}`;
    const userMsg: ChatMessage = {
      id: userMsgId,
      role: "user",
      content: text,
      timestamp: Date.now(),
    };
    rawMessages.value = [...rawMessages.value, userMsg];

    if (!currentThreadId) {
      const targetAgent = opts?.selectedAgentId || "";
      const targetDir = opts?.selectedDir || "";
      const created = await createSession(targetAgent, targetDir);
      if (created && created.chatID) {
        currentThreadId = created.chatID;
        activeSessionId.value = currentThreadId;
        activeSession.value = created;
        sessions.value = [created, ...sessions.value.filter((s) => s.chatID !== created.chatID)];
        events.connect(currentThreadId);
        if (router) {
          await router.push(`/chat/${currentThreadId}`);
        }
      } else {
        rawMessages.value = rawMessages.value.filter((m) => m.id !== userMsgId);
        loading.value = false;
        isRunning.value = false;
        pushErrorMessage("Failed to create session. Please try again.");
        return;
      }
    }

    const currentSession = sessions.value.find((s) => s.chatID === currentThreadId) || {
      chatID: currentThreadId || "",
      currentAgent: opts?.selectedAgentId || "",
      runDir: opts?.selectedDir || "",
      title: "",
    };

    const matchedAgent = agents.value.find(
      (a) => a.id === currentSession.currentAgent || a.name === currentSession.currentAgent,
    );
    const targetAgentId = matchedAgent ? matchedAgent.id : currentSession.currentAgent;

    const res = await triggerAgentMessage(targetAgentId, {
      prompt: text,
      chatId: currentThreadId,
      runDir: currentSession.runDir || opts?.selectedDir,
      model: opts?.selectedModel,
      metadata: { message_id: userMsgId },
    });

    if (res?.conflict) {
      rawMessages.value = rawMessages.value.filter((m) => m.id !== userMsgId);
      loading.value = false;
      isRunning.value = false;
      pushErrorMessage("Session is already running a task. Please wait.");
    } else if (!res) {
      rawMessages.value = rawMessages.value.filter((m) => m.id !== userMsgId);
      loading.value = false;
      isRunning.value = false;
      pushErrorMessage("Failed to trigger agent execution. Please try again.");
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

  return {
    sessions,
    activeSessionId,
    activeSession,
    activeAgent,
    rawMessages,
    messages,
    artifacts,
    isRunning,
    loading,
    workingAgentLabel,
    isInputBusy,
    openSession,
    closeSession,
    loadSessions,
    updateMessageReply,
    sendMessage,
  };
}
