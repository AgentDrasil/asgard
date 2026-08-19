import type {
  AgentInfo,
  ChatSession,
  DirInfo,
  FirebaseWebpushWebConfig,
  GitActionResult,
  GitDiffFile,
  GitLogResponse,
  TriggerAgentMessageParams,
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

export async function getGitDiff(dir: string, commit?: string): Promise<GitDiffFile[]> {
  if (!dir) return [];
  try {
    let url = `/api/git/diff?dir=${encodeURIComponent(dir)}`;
    if (commit) {
      url += `&commit=${encodeURIComponent(commit)}`;
    }
    const res = await apiFetch(url);
    if (!res.ok) return [];
    const data = await res.json();
    return data.files || [];
  } catch (err) {
    console.error("getGitDiff error:", err);
    return [];
  }
}

export async function getGitLog(dir: string, limit = 10): Promise<GitLogResponse | null> {
  if (!dir) return null;
  try {
    const res = await apiFetch(`/api/git/log?dir=${encodeURIComponent(dir)}&limit=${limit}`);
    if (!res.ok) return null;
    return await res.json();
  } catch (err) {
    console.error("getGitLog error:", err);
    return null;
  }
}

export async function gitPush(dir: string): Promise<GitActionResult> {
  if (!dir) return { success: false, error: "Directory is required" };
  try {
    const res = await apiFetch("/api/git/push", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ dir }),
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

export async function gitPull(dir: string): Promise<GitActionResult> {
  if (!dir) return { success: false, error: "Directory is required" };
  try {
    const res = await apiFetch("/api/git/pull", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ dir }),
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
