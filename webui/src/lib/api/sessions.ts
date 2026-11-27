import type { ChatSession, WorkflowRunSummary } from "../../types";
import { apiFetch } from "./client";
import { RunnerRequestError } from "./runners";

export async function getSession(chatID: string): Promise<ChatSession | null> {
  try {
    const res = await apiFetch(`/api/sessions/${encodeURIComponent(chatID)}`);
    if (res.ok) return await res.json();
  } catch (err) {
    console.error("Failed to fetch session from backend:", err);
  }
  return null;
}

export async function getSessions(archived = false, limit?: number): Promise<ChatSession[]> {
  try {
    const params = new URLSearchParams();
    if (archived) params.set("archived", "true");
    if (limit && limit > 0) params.set("limit", limit.toString());
    const query = params.toString();
    const url = query ? `/api/sessions?${query}` : "/api/sessions";
    const res = await apiFetch(url);
    if (!res.ok) throw new Error("Failed to fetch sessions");
    return await res.json();
  } catch (err) {
    console.error("getSessions error:", err);
    return [];
  }
}

export async function archiveSession(chatID: string): Promise<boolean> {
  if (!chatID) return false;
  try {
    const res = await apiFetch(`/api/sessions/${encodeURIComponent(chatID)}/archive`, {
      method: "POST",
    });
    return res.ok;
  } catch (err) {
    console.error("Failed to archive session:", err);
    return false;
  }
}

export async function unlockSession(
  chatID: string,
): Promise<{ status: string; chatId?: string; unlocked?: boolean }> {
  if (!chatID) {
    throw new Error("Session ID is required");
  }
  const res = await apiFetch(`/api/sessions/${encodeURIComponent(chatID)}/unlock`, {
    method: "POST",
  });
  if (!res.ok) {
    const body = await res.json().catch(() => null);
    throw new Error(body?.error || `Failed to unlock session (${res.status})`);
  }
  return await res.json();
}

export async function searchSessions(query: string, signal?: AbortSignal): Promise<ChatSession[]> {
  if (!query || !query.trim()) return [];
  try {
    const res = await apiFetch(`/api/sessions?q=${encodeURIComponent(query.trim())}`, { signal });
    if (!res.ok) {
      const body = await res.json().catch(() => null);
      throw new Error(body?.error || `Failed to search sessions (${res.status})`);
    }
    return await res.json();
  } catch (err: any) {
    if (err?.name !== "AbortError") {
      console.error("searchSessions error:", err);
      throw err;
    }
    return [];
  }
}

export async function createSession(
  currentAgent?: string,
  runDir?: string,
  allowCrossSession?: boolean,
  runnerId?: string,
  worktree?: boolean,
): Promise<ChatSession | null> {
  try {
    const res = await apiFetch("/api/sessions", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        currentAgent,
        runDir,
        allowCrossSession,
        runnerId: runnerId || undefined,
        worktree: worktree || undefined,
      }),
    });
    if (res.ok) return await res.json();
    if (res.status === 409 || res.status === 503) {
      const body = await res.json().catch(() => null);
      throw new RunnerRequestError(res.status, body?.error || "");
    }
  } catch (err) {
    if (err instanceof RunnerRequestError) throw err;
    console.error("Failed to create session on backend:", err);
  }
  return null;
}

export async function deleteSessionFromLocal(chatID: string): Promise<void> {
  try {
    await apiFetch(`/api/sessions?chat_id=${encodeURIComponent(chatID)}`, {
      method: "DELETE",
    });
  } catch (err) {
    console.error("Failed to delete session from backend:", err);
  }
}

export async function sendAskUserReply(
  chatID: string,
  messageID: string,
  replyText: string,
): Promise<boolean> {
  try {
    const res = await apiFetch("/api/ask-user/reply", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        chat_id: chatID,
        message_id: messageID,
        reply_text: replyText,
      }),
    });
    return res.ok;
  } catch (err) {
    console.error("sendAskUserReply error:", err);
    return false;
  }
}

export async function getSessionWorkflows(sessionId: string): Promise<WorkflowRunSummary[]> {
  try {
    const res = await apiFetch(`/api/sessions/${encodeURIComponent(sessionId)}/workflows`);
    if (!res.ok) return [];
    const data = await res.json();
    return Array.isArray(data.runs) ? (data.runs as WorkflowRunSummary[]) : [];
  } catch (err) {
    console.error("getSessionWorkflows error:", err);
    return [];
  }
}

export async function redriveWorkflowRun(runId: string): Promise<boolean> {
  try {
    const res = await apiFetch(`/api/workflows/${encodeURIComponent(runId)}/redrive`, {
      method: "POST",
    });
    return res.ok;
  } catch (err) {
    console.error("redriveWorkflowRun error:", err);
    return false;
  }
}

export async function stopSessionExecution(sessionId: string): Promise<boolean> {
  try {
    const res = await apiFetch(`/api/sessions/${encodeURIComponent(sessionId)}/stop`, {
      method: "POST",
    });
    return res.ok;
  } catch (err) {
    console.error("stopSessionExecution error:", err);
    return false;
  }
}

export async function stopWorkflowRun(runId: string): Promise<boolean> {
  try {
    const res = await apiFetch(`/api/workflows/${encodeURIComponent(runId)}/stop`, {
      method: "POST",
    });
    return res.ok;
  } catch (err) {
    console.error("stopWorkflowRun error:", err);
    return false;
  }
}
