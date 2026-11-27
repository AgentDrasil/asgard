import { ref, computed, type Ref } from "vue";
import type { ChatSession, AgentInfo, ChatMessage, QueuedMessage } from "../../types";
import { mergeToolMessages } from "../../utils/messageUtils";

export const MAX_QUEUED_MESSAGES = 3;

/**
 * Reactive state shared by every part of the session store, plus the two
 * generation counters that guard against stale async results. The parts
 * (connection, active chat, queue, send, list) all operate on this object.
 */
export function createSessionState(agents: Ref<AgentInfo[]>) {
  const sessions = ref<ChatSession[]>([]);
  const activeSessionId = ref<string | null>(null);
  const activeSession = ref<ChatSession | null>(null);
  const activeAgent = ref<AgentInfo | null>(null);

  const rawMessages = ref<ChatMessage[]>([]);
  const messages = computed<ChatMessage[]>(() => mergeToolMessages(rawMessages.value));
  const artifacts = ref<string[]>([]);
  const queuedMessages = ref<QueuedMessage[]>([]);
  const canEnqueue = computed<boolean>(() => queuedMessages.value.length < MAX_QUEUED_MESSAGES);

  const isRunning = ref(false);
  // The active chat is between turns, held open while its subagents run.
  const waitingSubagents = ref(false);
  const loading = ref(false);
  const workingAgentLabel = ref<string | null>(null);

  // `load` is bumped by openSession/closeSession so an older in-flight open is
  // ignored. `runState` is bumped whenever a live status/done/ask_user event
  // changes the running state, so a snapshot fetched before it cannot override it.
  const gens = { load: 0, runState: 0 };

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

  return {
    agents,
    sessions,
    activeSessionId,
    activeSession,
    activeAgent,
    rawMessages,
    messages,
    artifacts,
    queuedMessages,
    canEnqueue,
    isRunning,
    waitingSubagents,
    loading,
    workingAgentLabel,
    isInputBusy,
    gens,
    pushErrorMessage,
    mergeArtifactsList,
  };
}

export type SessionState = ReturnType<typeof createSessionState>;
