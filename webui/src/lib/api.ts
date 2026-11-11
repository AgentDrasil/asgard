import type {
  AgentInfo,
  Attachment,
  ChatSession,
  CompressionMetrics,
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
  SendNowResult,
  SendNowReason,
  RunnersResponse,
  ManageRunnersResponse,
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
const AUTH_CHANNEL_NAME = "asgard_auth_channel";
// Cross-tab refresh lock kept in localStorage for browsers/site origins where
// the Web Locks API is unavailable (it is restricted to secure contexts). See
// acquireTabLock.
const TAB_LOCK_KEY = "asgard_auth_refresh_lock";
// A tab that holds the lock is assumed to have crashed once it is older than
// this, so a crashed holder cannot deadlock every other tab forever. It must
// comfortably exceed the time a single refresh takes.
const TAB_LOCK_TTL_MS = 10_000;
// How long a contender waits for the current holder before refreshing itself.
const TAB_LOCK_WAIT_MS = 10_000;
// Settle time before a contender confirms it won the lock; concurrent writers
// surface their write within this window.
const TAB_LOCK_SETTLE_MS = 50;
// A refresh token is single-use, so when two tabs exchange the same token at
// once one of them is rejected even though the session is healthy. This bounds
// how long the loser waits for the winner to publish its tokens before it
// treats the session as gone.
const SESSION_HANDOFF_TIMEOUT_MS = 3000;
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

function getBroadcastChannelCtor(): typeof BroadcastChannel | undefined {
  if (typeof BroadcastChannel !== "undefined") {
    return BroadcastChannel;
  }
  return (globalThis as unknown as { BroadcastChannel?: typeof BroadcastChannel }).BroadcastChannel;
}

function broadcastSessionUpdate(tokens: StoredTokens): void {
  try {
    const Channel = getBroadcastChannelCtor();
    if (Channel) {
      const channel = new Channel(AUTH_CHANNEL_NAME);
      channel.postMessage({ type: "session_updated", tokens });
      channel.close();
    }
  } catch {
    /* BroadcastChannel not available or failed */
  }
}

export function setStoredTokens(tokens: StoredTokens): void {
  try {
    localStorage.setItem(AUTH_STORAGE_KEY, JSON.stringify(tokens));
    broadcastSessionUpdate(tokens);
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

interface SessionStamp {
  access: string | undefined;
  refresh: string | undefined;
}

function sessionStamp(): SessionStamp {
  const tokens = getStoredTokens();
  return { access: tokens?.access_token, refresh: tokens?.refresh_token };
}

// sessionRefreshedElsewhere reports whether a different, non-empty session is
// now stored than the one captured in before. The provider rotates the
// single-use refresh token, so a tab that loses a cross-tab refresh race must
// adopt the winner's session instead of forcing the user back through login.
// The winner writes around the time our own rejection arrives, so wait for the
// cross-tab BroadcastChannel or storage event (bounded) before giving up.
function sessionRefreshedElsewhere(before: SessionStamp): Promise<boolean> {
  const changed = (): boolean => {
    const now = sessionStamp();
    return !!now.access && (now.access !== before.access || now.refresh !== before.refresh);
  };

  if (changed()) {
    return Promise.resolve(true);
  }
  if (!before.refresh) {
    // Without a stored refresh token there is nothing another tab could have
    // handed off, so there is no point waiting.
    return Promise.resolve(false);
  }

  return new Promise((resolve) => {
    let settled = false;
    let channel: BroadcastChannel | null = null;
    const finish = (value: boolean): void => {
      if (settled) return;
      settled = true;
      window.removeEventListener("storage", onStorage);
      if (channel) {
        channel.close();
      }
      clearTimeout(timer);
      resolve(value);
    };

    const onStorage = (event: StorageEvent): void => {
      if (event.key === AUTH_STORAGE_KEY && changed()) {
        finish(true);
      }
    };

    try {
      const Channel = getBroadcastChannelCtor();
      if (Channel) {
        channel = new Channel(AUTH_CHANNEL_NAME);
        channel.onmessage = (event: MessageEvent) => {
          if (event.data?.type === "session_updated" && changed()) {
            finish(true);
          }
        };
      }
    } catch {
      /* BroadcastChannel not available */
    }

    const timer = setTimeout(() => finish(false), SESSION_HANDOFF_TIMEOUT_MS);
    window.addEventListener("storage", onStorage);
    // The other tab may have written between the check above and here.
    if (changed()) {
      finish(true);
    }
  });
}

// isTabHidden reports whether the current document is in a background/hidden state.
function isTabHidden(): boolean {
  if (typeof document === "undefined") return false;
  return document.visibilityState === "hidden";
}

// backgroundTabWaitingRedirect indicates whether a visibilitychange listener
// is currently pending to re-evaluate auth status upon tab activation.
let backgroundTabWaitingRedirect = false;

function listenForTabActivationRedirect(): void {
  if (backgroundTabWaitingRedirect) return;
  if (typeof document === "undefined" || typeof document.addEventListener !== "function") return;

  backgroundTabWaitingRedirect = true;
  const onVisible = (): void => {
    if (document.visibilityState === "visible") {
      backgroundTabWaitingRedirect = false;
      document.removeEventListener("visibilitychange", onVisible);
      if (!getStoredTokens()?.access_token) {
        redirectToLogin();
      }
    }
  };
  document.addEventListener("visibilitychange", onVisible);
}

// refreshing deduplicates refreshes within this tab for browsers without the
// Web Locks API (old Safari, WebViews, insecure origins).
let refreshing: Promise<RefreshResult> | null = null;

interface TabLock {
  id: string;
  ts: number;
}

function randomId(): string {
  return Math.random().toString(36).slice(2) + Date.now().toString(36);
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

function readTabLock(): TabLock | null {
  try {
    const raw = localStorage.getItem(TAB_LOCK_KEY);
    if (!raw) return null;
    const parsed = JSON.parse(raw) as TabLock;
    return typeof parsed?.id === "string" && typeof parsed?.ts === "number" ? parsed : null;
  } catch {
    // Malformed value or blocked storage: treat it as unlocked.
    return null;
  }
}

function writeTabLock(lock: TabLock): void {
  try {
    localStorage.setItem(TAB_LOCK_KEY, JSON.stringify(lock));
  } catch {
    /* storage unavailable (e.g. private mode): fall back to the in-page flight */
  }
}

function removeTabLock(): void {
  try {
    localStorage.removeItem(TAB_LOCK_KEY);
  } catch {
    /* ignore */
  }
}

function isStorageUsable(): boolean {
  const probe = `${TAB_LOCK_KEY}:probe:${randomId()}`;
  try {
    localStorage.setItem(probe, "1");
  } catch {
    return false;
  }
  try {
    localStorage.removeItem(probe);
  } catch {
    /* ignore remove failure in degraded storage */
  }
  return true;
}

// acquireTabLock serialises refreshes across tabs when Web Locks is unavailable.
// Every contender publishes its own lock, waits for concurrent writes to
// surface, then reads back: only the tab whose id survives is the holder. Others
// back off and retry, so two tabs never exchange the same single-use refresh
// token and one of them is not mistaken for a replay. Returns null when the wait
// budget is exhausted, in which case the caller may still refresh (the caller
// re-checks whether another tab already handed off a session).
// If localStorage is unavailable (e.g. private browsing or storage disabled),
// acquireTabLock returns a no-op release callback immediately so that callers
// do not spin and exhaust the 10-second timeout.
async function acquireTabLock(): Promise<(() => void) | null> {
  if (!isStorageUsable()) {
    return () => {};
  }

  const me: TabLock = { id: randomId(), ts: Date.now() };
  const deadline = Date.now() + TAB_LOCK_WAIT_MS;

  while (Date.now() < deadline) {
    const current = readTabLock();
    const heldByOther =
      !!current && current.id !== me.id && Date.now() - current.ts < TAB_LOCK_TTL_MS;
    if (!heldByOther) {
      me.ts = Date.now();
      writeTabLock(me);
      await sleep(TAB_LOCK_SETTLE_MS);
      if (readTabLock()?.id === me.id) {
        return () => {
          if (readTabLock()?.id === me.id) {
            removeTabLock();
          }
        };
      }
    }
    await sleep(TAB_LOCK_SETTLE_MS);
  }
  return null;
}

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

  // No Web Locks (insecure context): deduplicate within this page and take the
  // localStorage lock so every other tab queues behind us.
  refreshing ??= (async () => {
    const release = await acquireTabLock();
    try {
      return await run();
    } finally {
      release?.();
    }
  })().finally(() => {
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
  const before = sessionStamp();
  const result = await refreshOnce();
  if (result === "ok") return "recovered";
  if (result === "expired") {
    if (await sessionRefreshedElsewhere(before)) {
      return "recovered";
    }
    if (isTabHidden()) {
      listenForTabActivationRedirect();
      return "retry";
    }
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
    let isRoleForbidden = false;
    try {
      const resClone = typeof response.clone === "function" ? response.clone() : response;
      const data = await resClone.json();
      if (data && typeof data === "object" && data.error === "insufficient role") {
        isRoleForbidden = true;
      }
    } catch {
      // not JSON: treat as non-role forbidden
    }

    if (isRoleForbidden) {
      redirectToDenied();
      // Return an unresolved promise so callers neither retry nor start an
      // endless fetch loop while the browser navigates away.
      return new Promise<Response>(() => {});
    }

    return response;
  }

  if (response.status === 401) {
    const before = sessionStamp();
    const result = await refreshOnce();
    if (result === "ok") {
      response = await fetch(input, applyAuth(init));
    } else if (result === "expired") {
      if (await sessionRefreshedElsewhere(before)) {
        // Another tab exchanged the single-use refresh token first; reuse the
        // session it published instead of forcing a re-login.
        response = await fetch(input, applyAuth(init));
      } else {
        // When running in a background tab (e.g. background polling), do not
        // bounce the page to /auth/login. An inactive tab cannot complete the
        // OAuth flow and would cause concurrent callback code exchanges or
        // state invalidations with active tabs. Wait until the user activates
        // the tab instead.
        if (isTabHidden()) {
          // Listen for tab activation to re-evaluate the session: another tab
          // may have logged in or refreshed by the time the user returns. If
          // still expired, trigger the login redirect.
          listenForTabActivationRedirect();
          return new Promise<Response>(() => {});
        }
        redirectToLogin();
        return new Promise<Response>(() => {});
      }
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

// Clear the backend diagnostic logs and active error/warning snapshot (admin only).
export async function clearSystemLogs(): Promise<boolean> {
  try {
    const res = await apiFetch("/api/system/logs", { method: "DELETE" });
    return res.ok;
  } catch (err) {
    console.error("clearSystemLogs error:", err);
    return false;
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

// withRunner appends the ?runner= query parameter the backend uses to scope a
// request that names no chat (agents list, subdirs, sidebar terminal) to one
// runner. An empty runner (legacy mode) leaves the URL untouched.
export function withRunner(url: string, runner?: string): string {
  if (!runner) return url;
  return `${url}${url.includes("?") ? "&" : "?"}runner=${encodeURIComponent(runner)}`;
}

// RunnerRequestError is a runner binding failure on a chat-creating request:
// 409 (chat bound to another runner) or 503 (runner offline).
export class RunnerRequestError extends Error {
  status: number;
  constructor(status: number, message: string) {
    super(message);
    this.name = "RunnerRequestError";
    this.status = status;
  }
}

// Fetch the runners known to the backend. Returns null on failure.
// Admin only: all runners with their assigned users, plus known users.
export async function getManageRunners(): Promise<ManageRunnersResponse | null> {
  try {
    const res = await apiFetch("/api/manage/runners");
    if (!res.ok) return null;
    const data = await res.json();
    return {
      runners: Array.isArray(data?.runners) ? data.runners : [],
      users: Array.isArray(data?.users) ? data.users : [],
    };
  } catch (err) {
    console.error("getManageRunners error:", err);
    return null;
  }
}

// Admin only: replace the set of users assigned to a runner.
export async function setRunnerUsers(nodeId: string, users: string[]): Promise<string | null> {
  try {
    const res = await apiFetch(`/api/manage/runners/${encodeURIComponent(nodeId)}/users`, {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ users }),
    });
    if (res.ok) return null;
    const data = await res.json().catch(() => null);
    return data?.error || `HTTP ${res.status}`;
  } catch (err) {
    return String(err);
  }
}

export async function getRunners(): Promise<RunnersResponse | null> {
  try {
    const res = await apiFetch("/api/runners");
    if (!res.ok) return null;
    const data = await res.json();
    return {
      backend_version: data?.backend_version || "",
      runners: Array.isArray(data?.runners) ? data.runners : [],
      runner_mode: data?.runner_mode,
    };
  } catch (err) {
    console.error("getRunners error:", err);
    return null;
  }
}

// Fetch loaded agents from backend
export async function getAgents(runner?: string): Promise<AgentInfo[]> {
  try {
    const res = await apiFetch(withRunner("/api/agents", runner));
    if (!res.ok) throw new Error("Failed to fetch agents");
    return await res.json();
  } catch (err) {
    console.error("getAgents error:", err);
    return [];
  }
}

export interface UserProxyKeyMeta {
  key_name: string;
  updated_at: string;
}

export async function getUserProxyKeys(): Promise<UserProxyKeyMeta[]> {
  const res = await apiFetch("/api/user/proxy-keys");
  if (!res.ok) {
    const data = await res.json().catch(() => ({}));
    throw new Error(data?.error || `Failed to fetch user proxy keys: ${res.statusText}`);
  }
  return res.json();
}

export async function saveUserProxyKey(key_name: string, value: string): Promise<void> {
  const res = await apiFetch("/api/user/proxy-keys", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ key_name, value }),
  });
  if (!res.ok) {
    const data = await res.json().catch(() => ({}));
    throw new Error(data?.error || `Failed to save user proxy key: ${res.statusText}`);
  }
}

export async function deleteUserProxyKey(key_name: string): Promise<void> {
  const res = await apiFetch(`/api/user/proxy-keys/${encodeURIComponent(key_name)}`, {
    method: "DELETE",
  });
  if (!res.ok) {
    const data = await res.json().catch(() => ({}));
    throw new Error(data?.error || `Failed to delete user proxy key: ${res.statusText}`);
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

export async function getDirInfo(dir: string, runner?: string): Promise<DirInfo> {
  if (!dir) return { subdirs: [], gitRoot: "" };
  try {
    const res = await apiFetch(withRunner(`/api/subdirs?dir=${encodeURIComponent(dir)}`, runner));
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

export async function getSubdirs(dir: string, runner?: string): Promise<string[]> {
  const info = await getDirInfo(dir, runner);
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
    if (!res.ok) {
      const data = await res.json().catch(() => null);
      throw new Error(data?.error || `Failed to load git diff (${res.status})`);
    }
    const data = await res.json();
    return data.files || [];
  } catch (err) {
    console.error("getGitDiff error:", err);
    throw err;
  }
}

export async function getGitLog(sessionId: string, limit = 10): Promise<GitLogResponse | null> {
  if (!sessionId) return null;
  try {
    const res = await apiFetch(
      `/api/git/log?session_id=${encodeURIComponent(sessionId)}&limit=${limit}`,
    );
    if (!res.ok) {
      const data = await res.json().catch(() => null);
      throw new Error(data?.error || `Failed to load git log (${res.status})`);
    }
    return await res.json();
  } catch (err) {
    console.error("getGitLog error:", err);
    throw err;
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
