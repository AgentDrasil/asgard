import type {
  AgentInfo,
  ChatSession,
  ConfigFileResponse,
  ConfigSaveResponse,
  DirInfo,
  FileSearchResult,
  FileTreeEntry,
  FirebaseWebpushWebConfig,
  GitActionResult,
  GitDiffFile,
  GitLogResponse,
  SystemLogEntry,
  SystemLogsResponse,
  SystemStatusResponse,
  TriggerAgentMessageParams,
  WorkspaceFileContent,
} from "../types";

// Centralized fetch wrapper that handles 401 Unauthorized by redirecting for SSO refresh
export async function apiFetch(input: RequestInfo | URL, init?: RequestInit): Promise<Response> {
  const response = await fetch(input, init);
  if (response.status === 401) {
    console.log("apiFetch: 401 received, redirecting to refresh session via SSO...");
    const url = new URL(window.location.href);
    url.searchParams.set("_auth_refresh", Date.now().toString());
    window.location.href = url.toString();
  }
  return response;
}

// Fetch system diagnostics status (returns null on 404 or non-ok)
export async function getSystemStatus(): Promise<SystemStatusResponse | null> {
  try {
    const res = await apiFetch("/api/system/status");
    if (res.status === 404) return null;
    if (res.ok) return await res.json();
  } catch (err) {
    console.error("getSystemStatus error:", err);
  }
  return null;
}

// Fetch diagnostic system logs (returns empty array on non-ok or network failure)
export async function getSystemLogs(level?: string): Promise<SystemLogEntry[]> {
  try {
    const url =
      level && level !== "all"
        ? `/api/system/logs?level=${encodeURIComponent(level)}`
        : "/api/system/logs";
    const res = await apiFetch(url);
    if (!res.ok) return [];
    const data: SystemLogsResponse = await res.json();
    return data.logs || [];
  } catch (err) {
    console.error("getSystemLogs error:", err);
    return [];
  }
}

// Fetch loaded agents from backend
export async function getAgents(): Promise<AgentInfo[]> {
  try {
    const res = await apiFetch("/api/agents");
    if (!res.ok) throw new Error("Failed to fetch agents");
    return await res.json();
  } catch (err) {
    console.error("getAgents error:", err);
    return [];
  }
}

// Reload agents via /api/manage/reload
export async function reloadAgents(): Promise<{ success: boolean; error?: string }> {
  try {
    const res = await apiFetch("/api/manage/reload", { method: "POST" });
    if (res.ok) {
      return { success: true };
    }
    const body = await res.json().catch(() => null);
    return {
      success: false,
      error: body?.error || `Reload failed with status ${res.status}`,
    };
  } catch (err: any) {
    console.error("reloadAgents error:", err);
    return {
      success: false,
      error: err?.message || "Failed to reload agents",
    };
  }
}

// Fetch raw config file content
export async function getConfigFile(): Promise<ConfigFileResponse | null> {
  try {
    const res = await apiFetch("/api/manage/config");
    if (res.ok) return await res.json();
  } catch (err) {
    console.error("getConfigFile error:", err);
  }
  return null;
}

// Save raw config file content
export async function saveConfigFile(content: string): Promise<ConfigSaveResponse> {
  try {
    const res = await apiFetch("/api/manage/config", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ content }),
    });
    const body = await res.json().catch(() => null);
    if (res.ok) {
      return { status: "success", message: body?.message || "config saved" };
    }
    return { error: body?.error || `Failed to save configuration (${res.status})` };
  } catch (err: any) {
    console.error("saveConfigFile error:", err);
    return { error: err?.message || "Failed to save configuration" };
  }
}

// Trigger server restart (tolerates connection abort / reset)
export async function restartServer(): Promise<boolean> {
  try {
    const res = await apiFetch("/api/manage/restart", { method: "POST" });
    return res.ok;
  } catch (err) {
    // When the server terminates gracefully, the HTTP connection might be dropped/reset
    console.warn("restartServer network connection dropped as expected during shutdown:", err);
    return true;
  }
}

export async function getSession(chatID: string): Promise<ChatSession | null> {
  try {
    const res = await apiFetch(`/api/sessions/${encodeURIComponent(chatID)}`);
    if (res.ok) return await res.json();
  } catch (err) {
    console.error("Failed to fetch session from backend:", err);
  }
  return null;
}

export async function getSessions(): Promise<ChatSession[]> {
  try {
    const res = await apiFetch("/api/sessions");
    if (res.ok) return await res.json();
  } catch (err) {
    console.error("Failed to fetch sessions from backend:", err);
  }
  return [];
}

export async function createSession(
  currentAgent?: string,
  runDir?: string,
): Promise<ChatSession | null> {
  try {
    const res = await apiFetch("/api/sessions", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ currentAgent, runDir }),
    });
    if (res.ok) return await res.json();
  } catch (err) {
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

export async function getDirInfo(dir: string): Promise<DirInfo> {
  if (!dir) return { subdirs: [], gitRoot: "" };
  try {
    const res = await apiFetch(`/api/subdirs?dir=${encodeURIComponent(dir)}`);
    if (!res.ok) return { subdirs: [], gitRoot: "" };
    const data = await res.json();
    return {
      subdirs: data.subdirs || [],
      gitRoot: data.git_root || data.gitRoot || "",
    };
  } catch (err) {
    console.error("getDirInfo error:", err);
    return { subdirs: [], gitRoot: "" };
  }
}

export async function getSubdirs(dir: string): Promise<string[]> {
  const info = await getDirInfo(dir);
  return info.subdirs;
}

export async function getGitDiff(sessionId: string, commit?: string): Promise<GitDiffFile[]> {
  if (!sessionId) return [];
  const params = new URLSearchParams();
  params.set("session_id", sessionId);

  if (commit) {
    params.set("commit", commit);
  }

  try {
    const res = await apiFetch(`/api/git/diff?${params.toString()}`);
    if (!res.ok) return [];
    const data = await res.json();
    return data.files || [];
  } catch (err) {
    console.error("getGitDiff error:", err);
    return [];
  }
}

export async function getGitLog(sessionId: string, limit = 10): Promise<GitLogResponse | null> {
  if (!sessionId) return null;
  try {
    const res = await apiFetch(
      `/api/git/log?session_id=${encodeURIComponent(sessionId)}&limit=${limit}`,
    );
    if (!res.ok) return null;
    return await res.json();
  } catch (err) {
    console.error("getGitLog error:", err);
    return null;
  }
}

export async function gitPush(sessionId: string): Promise<GitActionResult> {
  if (!sessionId) return { success: false, error: "Session ID is required" };
  try {
    const res = await apiFetch("/api/git/push", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ session_id: sessionId }),
    });
    const data = await res.json();
    return {
      success: res.ok && data.success,
      output: data.output,
      error: data.error,
    };
  } catch (err: any) {
    console.error("gitPush error:", err);
    return { success: false, error: err?.message || "Failed to push" };
  }
}

export async function gitPull(sessionId: string): Promise<GitActionResult> {
  if (!sessionId) return { success: false, error: "Session ID is required" };
  try {
    const res = await apiFetch("/api/git/pull", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ session_id: sessionId }),
    });
    const data = await res.json();
    return {
      success: res.ok && data.success,
      output: data.output,
      error: data.error,
    };
  } catch (err: any) {
    console.error("gitPull error:", err);
    return { success: false, error: err?.message || "Failed to pull" };
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

export async function registerPushToken(token: string): Promise<boolean> {
  try {
    const res = await apiFetch("/api/push/tokens", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ token }),
    });
    return res.ok;
  } catch (err) {
    console.error("registerPushToken error:", err);
    return false;
  }
}

export async function getBackendConfig(): Promise<{
  firebase_webpush_web?: FirebaseWebpushWebConfig;
}> {
  try {
    const res = await apiFetch("/api/config");
    if (res.ok) return await res.json();
  } catch (err) {
    console.error("getBackendConfig error:", err);
  }
  return {};
}

export async function triggerAgentMessage(
  agentId: string,
  params: TriggerAgentMessageParams,
): Promise<{ status: string; chatId: string; conflict?: boolean } | null> {
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
      }),
    });
    if (res.status === 409) {
      return { status: "conflict", chatId: params.chatId || "", conflict: true };
    }
    if (res.ok) return await res.json();
  } catch (err) {
    console.error("Failed to trigger agent message:", err);
  }
  return null;
}

export async function getFileTree(sessionId: string, subPath = ""): Promise<FileTreeEntry[]> {
  if (!sessionId) return [];
  try {
    let url = `/api/files/tree?session_id=${encodeURIComponent(sessionId)}`;
    if (subPath) url += `&path=${encodeURIComponent(subPath)}`;
    const res = await apiFetch(url);
    if (!res.ok) {
      const body = await res.json().catch(() => null);
      throw new Error(body?.error || `Failed to load file tree (${res.status})`);
    }
    const data = await res.json();
    return data.entries || [];
  } catch (err) {
    console.error("getFileTree error:", err);
    throw err;
  }
}

export async function getFileContent(
  sessionId: string,
  path: string,
): Promise<WorkspaceFileContent | null> {
  if (!sessionId || !path) return null;
  try {
    const res = await apiFetch(
      `/api/files/content?session_id=${encodeURIComponent(sessionId)}&path=${encodeURIComponent(path)}`,
    );
    if (!res.ok) {
      const body = await res.json().catch(() => null);
      throw new Error(body?.error || `Failed to load file (${res.status})`);
    }
    return await res.json();
  } catch (err) {
    console.error("getFileContent error:", err);
    throw err;
  }
}

export async function searchFiles(
  sessionId: string,
  query: string,
  limit = 50,
  signal?: AbortSignal,
): Promise<FileSearchResult[]> {
  if (!sessionId) return [];
  try {
    const res = await apiFetch(
      `/api/files/search?session_id=${encodeURIComponent(sessionId)}&query=${encodeURIComponent(query)}&limit=${limit}`,
      { signal },
    );
    if (!res.ok) return [];
    const data = await res.json();
    return data.files || [];
  } catch (err: any) {
    if (err?.name !== "AbortError") {
      console.error("searchFiles error:", err);
    }
    return [];
  }
}
