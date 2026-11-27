import type { StoredTokens } from "../../types";

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

export function sessionStamp(): SessionStamp {
  const tokens = getStoredTokens();
  return { access: tokens?.access_token, refresh: tokens?.refresh_token };
}

// sessionRefreshedElsewhere reports whether a different, non-empty session is
// now stored than the one captured in before. The provider rotates the
// single-use refresh token, so a tab that loses a cross-tab refresh race must
// adopt the winner's session instead of forcing the user back through login.
// The winner writes around the time our own rejection arrives, so wait for the
// cross-tab BroadcastChannel or storage event (bounded) before giving up.
export function sessionRefreshedElsewhere(before: SessionStamp): Promise<boolean> {
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
export function isTabHidden(): boolean {
  if (typeof document === "undefined") return false;
  return document.visibilityState === "hidden";
}

// backgroundTabWaitingRedirect indicates whether a visibilitychange listener
// is currently pending to re-evaluate auth status upon tab activation.
let backgroundTabWaitingRedirect = false;

export function listenForTabActivationRedirect(): void {
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
export function refreshOnce(): Promise<RefreshResult> {
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

export function redirectToLogin(): void {
  const current = new URL(window.location.href);
  window.location.href = `/auth/login?redirect=${encodeURIComponent(current.pathname + current.search)}`;
}

// redirectToDenied goes to the denial page for a session that is authenticated
// but not authorized. Logging in again cannot help, so it must not loop through
// the OAuth flow.
export function redirectToDenied(): void {
  window.location.href = "/auth/denied";
}

// reloadForExternalSSO is the historical fallback: reloading lets a reverse
// proxy sitting in front of Asgard re-authenticate the user.
export function reloadForExternalSSO(): void {
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
