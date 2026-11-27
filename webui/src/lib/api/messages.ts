import type {
  QueuedMessage,
  SendNowResult,
  SendNowReason,
  TriggerAgentMessageParams,
} from "../../types";
import { apiFetch } from "./client";

export async function triggerAgentMessage(
  agentId: string,
  params: TriggerAgentMessageParams,
): Promise<{
  status: string;
  chatId: string;
  conflict?: boolean;
  runnerOffline?: boolean;
  queued?: boolean;
  messageId?: string;
  /** The message went into the live turn of a chat held open for its subagents. */
  injected?: boolean;
} | null> {
  try {
    const res = await apiFetch(`/api/agents/${encodeURIComponent(agentId)}/message`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        prompt: params.prompt,
        chatId: params.chatId,
        runDir: params.runDir,
        model: params.model,
        metadata: params.metadata,
        attachments: params.attachments,
        allowCrossSession: params.allowCrossSession,
      }),
    });
    if (res.status === 503) {
      return { status: "runner_offline", chatId: params.chatId || "", runnerOffline: true };
    }
    if (res.status === 409) {
      return { status: "conflict", chatId: params.chatId || "", conflict: true };
    }
    if (res.status === 202) {
      const body = await res.json().catch(() => null);
      if (body?.status === "queued") {
        return {
          status: "queued",
          chatId: params.chatId || "",
          queued: true,
          messageId: body?.messageId,
        };
      }
      return body;
    }
    if (res.ok) return await res.json();
  } catch (err) {
    console.error("Failed to trigger agent message:", err);
  }
  return null;
}

export async function getQueuedMessages(sessionId: string): Promise<QueuedMessage[] | null> {
  if (!sessionId) return null;
  try {
    const res = await apiFetch(`/api/sessions/${encodeURIComponent(sessionId)}/queue`);
    if (res.ok) {
      const data = await res.json();
      return Array.isArray(data) ? data : data?.queue || [];
    }
  } catch (err) {
    console.error("getQueuedMessages error:", err);
  }
  return null;
}

export async function enqueueMessage(
  sessionId: string,
  prompt: string,
): Promise<QueuedMessage | null> {
  if (!sessionId) return null;
  try {
    const res = await apiFetch(`/api/sessions/${encodeURIComponent(sessionId)}/queue`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ prompt }),
    });
    if (res.ok) {
      return await res.json();
    }
  } catch (err) {
    console.error("enqueueMessage error:", err);
  }
  return null;
}

export async function updateQueuedMessage(
  sessionId: string,
  messageId: string,
  prompt: string,
): Promise<QueuedMessage | null> {
  if (!sessionId || !messageId) return null;
  try {
    const res = await apiFetch(
      `/api/sessions/${encodeURIComponent(sessionId)}/queue/${encodeURIComponent(messageId)}`,
      {
        method: "PATCH",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ prompt }),
      },
    );
    if (res.ok) {
      return await res.json();
    }
  } catch (err) {
    console.error("updateQueuedMessage error:", err);
  }
  return null;
}

export async function deleteQueuedMessage(sessionId: string, messageId: string): Promise<boolean> {
  if (!sessionId || !messageId) return false;
  try {
    const res = await apiFetch(
      `/api/sessions/${encodeURIComponent(sessionId)}/queue/${encodeURIComponent(messageId)}`,
      {
        method: "DELETE",
      },
    );
    return res.ok;
  } catch (err) {
    console.error("deleteQueuedMessage error:", err);
  }
  return false;
}

export async function sendQueuedMessageNow(
  sessionId: string,
  messageId: string,
): Promise<SendNowResult> {
  if (!sessionId || !messageId) return { kind: "error" };
  try {
    const res = await apiFetch(
      `/api/sessions/${encodeURIComponent(sessionId)}/queue/${encodeURIComponent(messageId)}/send-now`,
      {
        method: "POST",
      },
    );
    if (res.status === 404) return { kind: "gone" };
    if (!res.ok) return { kind: "error" };
    const data = await res.json().catch(() => null);
    if (data?.injected === true) {
      return { kind: "injected", queue: Array.isArray(data.queue) ? data.queue : null };
    }
    const reason: SendNowReason =
      data?.reason === "not_running" ||
      data?.reason === "not_supported" ||
      data?.reason === "descendant_holds_checkout"
        ? data.reason
        : "failed";
    return { kind: "not_injected", reason };
  } catch (err) {
    console.error("sendQueuedMessageNow error:", err);
  }
  return { kind: "error" };
}
