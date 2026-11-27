import { ref } from "vue";
import type { SendNowResult } from "../../types";
import {
  updateQueuedMessage,
  deleteQueuedMessage,
  sendQueuedMessageNow,
  getQueuedMessages,
} from "../../lib/api";
import type { SessionState } from "./state";

/** Queued-message operations of the open chat. */
export function createQueue(state: SessionState) {
  const { activeSessionId, queuedMessages } = state;

  const editQueuedMessage = async (messageId: string, newText: string): Promise<boolean> => {
    if (!activeSessionId.value || !messageId) return false;
    const res = await updateQueuedMessage(activeSessionId.value, messageId, newText);
    if (res) {
      const idx = queuedMessages.value.findIndex((m) => m.id === messageId);
      if (idx > -1) {
        const updated = [...queuedMessages.value];
        updated[idx] = res;
        queuedMessages.value = updated;
      }
      return true;
    }
    // If update returned null (e.g. 404), upcoming SSE queue event will resync silently.
    return false;
  };

  const deleteQueuedMessageItem = async (messageId: string): Promise<boolean> => {
    if (!activeSessionId.value || !messageId) return false;
    const ok = await deleteQueuedMessage(activeSessionId.value, messageId);
    if (ok) {
      queuedMessages.value = queuedMessages.value.filter((m) => m.id !== messageId);
      return true;
    }
    // If delete returned false (e.g. 404), upcoming SSE queue event will resync silently.
    return false;
  };

  // Ids of queued messages with a send-now call in flight (spinner + guard
  // against a double click firing two calls).
  const sendingQueuedIds = ref<string[]>([]);

  const sendQueuedMessageNowItem = async (messageId: string): Promise<SendNowResult | null> => {
    const sessionId = activeSessionId.value;
    if (!sessionId || !messageId) return null;
    if (sendingQueuedIds.value.includes(messageId)) return null;
    sendingQueuedIds.value = [...sendingQueuedIds.value, messageId];
    try {
      const res = await sendQueuedMessageNow(sessionId, messageId);
      if (activeSessionId.value !== sessionId) return res;
      if (res.kind === "injected") {
        // The transcript gets the message through the normal SSE event.
        if (res.queue) queuedMessages.value = res.queue;
      } else {
        // The queue SSE event may have flashed the list while the message was
        // claimed; resync to the final server state.
        const queue = await getQueuedMessages(sessionId);
        if (queue && activeSessionId.value === sessionId) queuedMessages.value = queue;
      }
      return res;
    } finally {
      sendingQueuedIds.value = sendingQueuedIds.value.filter((id) => id !== messageId);
    }
  };

  return {
    editQueuedMessage,
    deleteQueuedMessage: deleteQueuedMessageItem,
    sendingQueuedIds,
    sendQueuedMessageNow: sendQueuedMessageNowItem,
  };
}
