import type {
  AgentInfo,
  Attachment,
  ChatSession,
  CompressionMetrics,
  ConfigFileResponse,
  ConfigSaveResponse,
  DirInfo,
  FileScope,
  FileSearchResult,
  FileTreeEntry,
  FirebaseWebpushWebConfig,
  GitActionResult,
  GitDiffFile,
  GitLogResponse,
  KeybindingsApiResponse,
  KeybindingsOverrides,
  QueuedMessage,
  StoredTokens,
  SystemLogEntry,
  SystemLogsResponse,
  SystemStatusResponse,
  TriggerAgentMessageParams,
  VoiceTokenResponse,
  WorkflowRunSummary,
  WorkspaceFileContent,
} from "../types";

// ---------------------------------------------------------------------------
// Authentication (stateless OIDC)
//
// Tokens live in localStorage and are attached to API requests as a Bearer
// header. When the backend reports that auth is disabled the app keeps its
// historical behaviour: a 401 reloads the page so an external reverse-proxy
// SSO can re-authenticate.
// ---------------------------------------------------------------------------

const AUTH_STORAGE_KEY = "asgard_auth";
const REFRESH_LOCK = "asgard_auth_refresh";
const AUTH_PROBE_TIMEOUT_MS = 5000;

// authEnabled is null until initAuth() has probed the backend.
let authEnabled: boolean | null = null;

// isAuthEnabled reports whether the backend runs OIDC auth. It is false until
// the capability probe succeeds, so a failed probe degrades to the external-SSO
// path rather than locking the user out.
export function isAuthEnabled(): boolean {
  return authEnabled === true;
}

// initAuth probes the public capability endpoint and must complete before the
// first business request (see main.ts). The probe is always anonymous: it runs
// before a token can possibly exist.
export async function initAuth(): Promise<boolean> {
  try {
    const res = await fetch("/api/auth/status", {
      signal: AbortSignal.timeout(AUTH_PROBE_TIMEOUT_MS),
    });
    if (res.ok) {
      const body = (await res.json()) as { enabled?: boolean };
      authEnabled = body.enabled === true;
    }
  } catch {
    // Probe failed (timeout, offline, older backend): keep authEnabled null so
    // the app falls back to the existing behaviour.
  }
  return authEnabled === true;
}

export function getStoredTokens(): StoredTokens | null {
  try {
    const raw = localStorage.getItem(AUTH_STORAGE_KEY);
    return raw ? (JSON.parse(raw) as StoredTokens) : null;
  } catch {
    return null;
  }
}

export function setStoredTokens(tokens: StoredTokens): void {
  try {
    localStorage.setItem(AUTH_STORAGE_KEY, JSON.stringify(tokens));
  } catch {
    // localStorage can be unavailable (private mode); nothing to persist then.
  }
}

// logout drops the local session and returns to the app. It does not perform
// provider-side logout.
export function logout(): void {
  try {
    localStorage.removeItem(AUTH_STORAGE_KEY);
  } catch {
    /* nothing to clear */
  }
  window.location.href = "/";
}

type RefreshResult = "ok" | "expired" | "unavailable";

// refreshTokens exchanges the stored refresh token for a fresh session via the
// backend, which holds the client secret.
async function refreshTokens(): Promise<RefreshResult> {
  const tokens = getStoredTokens();
  if (!tokens?.refresh_token) return "expired";

  let response: Response;
  try {
    response = await fetch("/auth/refresh", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ refresh_token: tokens.refresh_token }),
    });
  } catch {
    // Network failure: the tokens may still be valid, so keep the session.
    return "unavailable";
  }

  if (response.ok) {
    setStoredTokens((await response.json()) as StoredTokens);
    return "ok";
  }
  if (response.status === 400 || response.status === 401) {
    return "expired";
  }
  // Provider outage (5xx/429): do not log the user out.
  return "unavailable";
}

// refreshing deduplicates refreshes within this tab for browsers without the
// Web Locks API (old Safari, WebViews, insecure origins).
let refreshing: Promise<RefreshResult> | null = null;

// refreshOnce deduplicates refreshes across every tab. Rotation invalidates the
// previous refresh token, so two tabs refreshing in parallel would make the
// second attempt look like a replay attack and log the user out.
function refreshOnce(): Promise<RefreshResult> {
  const used = getStoredTokens()?.refresh_token;

  const run = async (): Promise<RefreshResult> => {
    // Another tab may have refreshed while we waited for the lock.
    const current = getStoredTokens()?.refresh_token;
    if (used && current && current !== used) return "ok";
    return refreshTokens();
  };

  const locks = (navigator as Navigator & { locks?: LockManager }).locks;
  if (locks) {
    return locks.request(REFRESH_LOCK, run);
  }

  refreshing ??= run().finally(() => {
    refreshing = null;
  });
  return refreshing;
}

function redirectToLogin(): void {
  const current = new URL(window.location.href);
  window.location.href = `/auth/login?redirect=${encodeURIComponent(current.pathname + current.search)}`;
}

// redirectToDenied goes to the denial page for a session that is authenticated
// but not authorized. Logging in again cannot help, so it must not loop through
// the OAuth flow.
function redirectToDenied(): void {
  window.location.href = "/auth/denied";
}

// reloadForExternalSSO is the historical fallback: reloading lets a reverse
// proxy sitting in front of Asgard re-authenticate the user.
function reloadForExternalSSO(): void {
  const url = new URL(window.location.href);
  url.searchParams.set("_auth_refresh", Date.now().toString());
  window.location.href = url.toString();
}

// recoverSession is used by channels that cannot go through apiFetch (the SSE
// stream). It attempts a single refresh and tells the caller what to do next.
export async function recoverSession(): Promise<"recovered" | "login" | "retry"> {
  const result = await refreshOnce();
  if (result === "ok") return "recovered";
  if (result === "expired") {
    redirectToLogin();
    return "login";
  }
  return "retry";
}

// appendAccessToken attaches the bearer token to a URL for channels that cannot
// set headers: EventSource, WebSocket upgrades and resource loads such as
// <img>/<iframe>/<a href>. It is a no-op while auth is disabled so those URLs
// stay untouched in external-SSO deployments.
export function appendAccessToken(url: string): string {
  if (!isAuthEnabled()) return url;
  const token = getStoredTokens()?.access_token;
  if (!token) return url;
  const separator = url.includes("?") ? "&" : "?";
  return `${url}${separator}access_token=${encodeURIComponent(token)}`;
}

// Centralized fetch wrapper.
//
// With auth disabled it keeps the original behaviour (a 401 triggers the
// external-SSO reload). With auth enabled it injects the bearer token, silently
// refreshes once on 401 and redirects to the denial page on 403.
export async function apiFetch(input: RequestInfo | URL, init?: RequestInit): Promise<Response> {
  // applyAuth returns init unchanged (including undefined) when there is nothing
  // to add, so callers observe the same fetch() arguments as before auth existed.
  const applyAuth = (options?: RequestInit): RequestInit | undefined => {
    if (!isAuthEnabled()) return options;
    const token = getStoredTokens()?.access_token;
    if (!token) return options;
    const headers = new Headers(options?.headers);
    headers.set("Authorization", `Bearer ${token}`);
    return { ...options, headers };
  };

  let response = await fetch(input, applyAuth(init));

  if (!isAuthEnabled()) {
    if (response.status === 401) {
      console.log("apiFetch: 401 received, redirecting to refresh session via SSO...");
      reloadForExternalSSO();
    }
    return response;
  }

  if (response.status === 403) {
    redirectToDenied();
    // Return an unresolved promise so callers neither retry nor start an
    // endless fetch loop while the browser navigates away.
    return new Promise<Response>(() => {});
  }

  if (response.status === 401) {
    const result = await refreshOnce();
    if (result === "ok") {
      response = await fetch(input, applyAuth(init));
    } else if (result === "expired") {
      redirectToLogin();
      return new Promise<Response>(() => {});
    }
    // "unavailable": keep the session and surface the response to the caller.
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

// Fetch command-output compression statistics (returns null on non-ok or network failure)
export async function getCompressionMetrics(): Promise<CompressionMetrics | null> {
  try {
    const res = await apiFetch("/api/compression-metrics");
    if (!res.ok) return null;
    return await res.json();
  } catch (err) {
    console.error("getCompressionMetrics error:", err);
    return null;
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

// Reload standalone proxy config via /api/manage/proxy/reload
export async function reloadProxyConfig(): Promise<{ success: boolean; error?: string }> {
  try {
    const res = await apiFetch("/api/manage/proxy/reload", { method: "POST" });
    if (res.ok) {
      return { success: true };
    }
    const body = await res.json().catch(() => null);
    return {
      success: false,
      error: body?.error || `Proxy reload failed with status ${res.status}`,
    };
  } catch (err: any) {
    console.error("reloadProxyConfig error:", err);
    return {
      success: false,
      error: err?.message || "Failed to reload proxy config",
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

// Fetch keybindings from /api/keybindings (public GET)
export async function getKeybindings(): Promise<KeybindingsApiResponse | null> {
  try {
    const res = await apiFetch("/api/keybindings");
    if (res.status === 200) {
      return await res.json();
    }
    if (res.status === 500) {
      const body = await res.json().catch(() => null);
      return {
        overrides: {},
        error: body?.error || "Failed to load keybindings (corrupted file)",
      };
    }
    const body = await res.json().catch(() => null);
    return {
      overrides: {},
      error: body?.error || `Failed to fetch keybindings (${res.status})`,
    };
  } catch (err: any) {
    console.error("getKeybindings error:", err);
    return null;
  }
}

// Save keybindings to /api/manage/keybindings (protected PUT)
export async function saveKeybindings(
  overrides: KeybindingsOverrides,
): Promise<{ success: boolean; error?: string }> {
  try {
    const res = await apiFetch("/api/manage/keybindings", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ overrides }),
    });
    const body = await res.json().catch(() => null);
    if (res.ok) {
      return { success: true };
    }
    return {
      success: false,
      error: body?.error || `Failed to save keybindings (${res.status})`,
    };
  } catch (err: any) {
    console.error("saveKeybindings error:", err);
    return {
      success: false,
      error: err?.message || "Failed to save keybindings",
    };
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
): Promise<ChatSession | null> {
  try {
    const res = await apiFetch("/api/sessions", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ currentAgent, runDir, allowCrossSession }),
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
  default_ui_lang?: string;
  proxy_envs?: string[];
}> {
  try {
    const res = await apiFetch("/api/config");
    if (res.ok) return await res.json();
  } catch (err) {
    console.error("getBackendConfig error:", err);
  }
  return {};
}

export async function uploadAttachment(sessionId: string, file: File): Promise<Attachment> {
  const formData = new FormData();
  formData.append("file", file);

  const res = await apiFetch(`/api/sessions/${encodeURIComponent(sessionId)}/attachments`, {
    method: "POST",
    body: formData,
  });

  if (!res.ok) {
    const body = await res.json().catch(() => null);
    throw new Error(body?.error || `Failed to upload attachment (${res.status})`);
  }

  const data: Attachment[] = await res.json();
  if (!Array.isArray(data) || data.length === 0) {
    throw new Error("Invalid attachment upload response");
  }
  return data[0];
}

export function getAttachmentUrl(sessionId: string, filename: string): string {
  return appendAccessToken(
    `/api/sessions/${encodeURIComponent(sessionId)}/attachments/${encodeURIComponent(filename)}`,
  );
}

export async function triggerAgentMessage(
  agentId: string,
  params: TriggerAgentMessageParams,
): Promise<{
  status: string;
  chatId: string;
  conflict?: boolean;
  queued?: boolean;
  messageId?: string;
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

export async function getQueuedMessages(sessionId: string): Promise<QueuedMessage[]> {
  if (!sessionId) return [];
  try {
    const res = await apiFetch(`/api/sessions/${encodeURIComponent(sessionId)}/queue`);
    if (res.ok) {
      const data = await res.json();
      return Array.isArray(data) ? data : data?.queue || [];
    }
  } catch (err) {
    console.error("getQueuedMessages error:", err);
  }
  return [];
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
  scope?: FileScope,
): Promise<WorkspaceFileContent | null> {
  if (!sessionId || !path) return null;
  try {
    const scopeParam = scope ? `&scope=${encodeURIComponent(scope)}` : "";
    const res = await apiFetch(
      `/api/files/content?session_id=${encodeURIComponent(sessionId)}&path=${encodeURIComponent(path)}${scopeParam}`,
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

export function getRawFileContentUrl(sessionId: string, path: string): string {
  return appendAccessToken(
    `/api/files/content?session_id=${encodeURIComponent(sessionId)}&path=${encodeURIComponent(path)}&raw=1`,
  );
}

// getRawWorkspaceFileUrl returns a URL usable directly as an <img>/<iframe> src,
// so the bearer token travels in the query string (headers are not available on
// resource loads). Note the token is a snapshot: a refresh will not update an
// URL that is already rendered.
export function getRawWorkspaceFileUrl(sessionId: string, path: string): string {
  return appendAccessToken(
    `/api/v1/workspace/file?session_id=${encodeURIComponent(sessionId)}&path=${encodeURIComponent(path)}&raw=1`,
  );
}

export async function getVoiceToken(): Promise<VoiceTokenResponse> {
  const res = await apiFetch("/api/voice/token", { method: "POST" });
  if (!res.ok) {
    const body = await res.json().catch(() => null);
    throw new Error(body?.error || `Failed to fetch voice token: ${res.status}`);
  }
  return await res.json();
}
