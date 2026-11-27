import { ref, computed, customRef, type Ref } from "vue";
import type { ChatSession, AgentInfo, ChatMessage, QueuedMessage } from "../../types";
import { createIncrementalToolMerger } from "../../utils/messageUtils";

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

  // rawMessages is a shallow ref: assigning a whole new array (filter, map,
  // snapshot merge, reset) invalidates the merged view from index 0, while
  // upsertMessage/replaceMessage mutate in place and only invalidate from the
  // touched index. Messages are never mutated; every update swaps the object
  // (refreshActiveSnapshot compares object identity to detect live changes).
  // Contract: the array identity does NOT change on in-place upserts, so
  // callers must watch `messages` (a new array per change), never rawMessages.
  // Never mutate rawMessages in place (push/splice); only assign a whole new
  // array or go through upsertMessage/replaceMessage, which keep idIndex and
  // dirtyFrom in sync.
  let rawArray: ChatMessage[] = [];
  let dirtyFrom = 0;
  let idIndex: Map<string, number> | null = null;
  let triggerRaw: () => void = () => {};
  const rawMessages = customRef<ChatMessage[]>((track, trigger) => {
    triggerRaw = trigger;
    return {
      get() {
        track();
        return rawArray;
      },
      set(next) {
        rawArray = next;
        dirtyFrom = 0;
        idIndex = null;
        trigger();
      },
    };
  });
  const indexOfId = (id: string): number => {
    if (!idIndex) {
      idIndex = new Map();
      for (let i = 0; i < rawArray.length; i++) {
        if (!idIndex.has(rawArray[i].id)) idIndex.set(rawArray[i].id, i);
      }
    }
    return idIndex.get(id) ?? -1;
  };
  const touch = (from: number) => {
    if (from < dirtyFrom) dirtyFrom = from;
    triggerRaw();
  };
  // Appends `incoming`, or replaces the message with the same id by
  // `merge(existing)` (a new object). O(1) amortized.
  const upsertMessage = (
    incoming: ChatMessage,
    merge: (existing: ChatMessage) => ChatMessage = (existing) => ({ ...existing, ...incoming }),
  ) => {
    const idx = indexOfId(incoming.id);
    if (idx > -1) {
      rawArray[idx] = merge(rawArray[idx]);
      touch(idx);
    } else {
      idIndex!.set(incoming.id, rawArray.length);
      rawArray.push(incoming);
      touch(rawArray.length - 1);
    }
  };
  // Replaces the message with this id by a new object; no-op when absent.
  const replaceMessage = (id: string, next: ChatMessage) => {
    const idx = indexOfId(id);
    if (idx < 0) return;
    rawArray[idx] = next;
    touch(idx);
  };
  const mergeIncremental = createIncrementalToolMerger();
  const messages = computed<ChatMessage[]>(() => {
    const raw = rawMessages.value;
    const from = dirtyFrom;
    dirtyFrom = raw.length;
    return mergeIncremental(raw, from);
  });
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
    upsertMessage({
      id: `error-${crypto.randomUUID()}`,
      role: "error",
      content,
      agentName: agentName || activeAgent.value?.name,
      timestamp: Date.now(),
    });
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
    upsertMessage,
    replaceMessage,
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
