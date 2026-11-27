import { ref, type Ref } from "vue";
import type { SessionEvent, SessionStatusPayload } from "../types";
import { appendAccessToken, getStoredTokens, isAuthEnabled, recoverSession } from "../lib/api";

export type { SessionStatusPayload };

// Reconnect backoff: exponential from BASE to CAP with jitter, so a restarting
// backend is not hit by every tab at the same instant.
const RECONNECT_BASE_DELAY_MS = 1000;
const RECONNECT_MAX_DELAY_MS = 30000;
// Consecutive attempts without a successful open before giving up: a permanent
// error (404 deleted session, 403) must not retry forever.
export const MAX_RECONNECT_ATTEMPTS = 10;
// Minimum spacing between immediate retries triggered by the tab becoming
// visible / the network coming back, so flipping tabs cannot burn the attempt
// budget or hammer the server.
export const WAKE_RETRY_MIN_INTERVAL_MS = 5000;

// "failed" is left by connect() (the user retries) or by a single probe when
// the tab becomes visible or the network comes back.
export type ConnectionState = "connecting" | "open" | "reconnecting" | "closed" | "failed";

export interface SessionEventsOpenInfo {
  // The stream was open before and was lost since (error, CLOSED, hidden tab).
  reconnected: boolean;
  // The server could replay the gap from a known last event id. When false the
  // gap is unknown and the caller must refetch its snapshot. (A `resync` event
  // can still follow when the server no longer holds that id.)
  resumed: boolean;
}

export interface SessionEventsCallbacks {
  onMessage?: (ev: SessionEvent) => void;
  onStatus?: (ev: SessionEvent) => void;
  onTitle?: (ev: SessionEvent) => void;
  onArtifact?: (ev: SessionEvent) => void;
  onDone?: (ev: SessionEvent) => void;
  onResync?: (ev: SessionEvent) => void;
  onQueue?: (ev: SessionEvent) => void;
  onSubagent?: (ev: SessionEvent) => void;
  onSubagentArtifact?: (ev: SessionEvent) => void;
  onError?: (err: Event) => void;
  onOpen?: (info: SessionEventsOpenInfo) => void;
}

// eventsUrl carries the bearer token in the query string: EventSource cannot set
// request headers. It is a no-op while auth is disabled. A fresh EventSource
// does not send Last-Event-ID, so the resume point travels as ?lastEventId=.
function eventsUrl(sessionId: string, lastEventId: number): string {
  let url = `/api/sessions/${encodeURIComponent(sessionId)}/events`;
  if (lastEventId > 0) url += `?lastEventId=${lastEventId}`;
  return appendAccessToken(url);
}

// reconnectDelay returns the jittered backoff for the given attempt (0-based).
export function reconnectDelay(attempt: number): number {
  const exp = Math.min(RECONNECT_MAX_DELAY_MS, RECONNECT_BASE_DELAY_MS * 2 ** attempt);
  return Math.round(exp * (0.5 + Math.random() * 0.5));
}

// useSessionEvents owns the whole lifecycle of one session's event stream:
// last-event-id tracking, resume on reconnect, backoff retry (any auth mode),
// token recovery via lib/api/auth.ts, and recovery when the tab becomes visible again.
export function useSessionEvents(callbacks: SessionEventsCallbacks = {}) {
  const state: Ref<ConnectionState> = ref("closed");
  let eventSource: EventSource | null = null;
  let currentSessionId: string | null = null;
  let reconnectTimer: ReturnType<typeof setTimeout> | null = null;
  let lastEventId = 0;
  let attempts = 0;
  // The stream dropped since the last onopen; the next open is a reconnect.
  let lost = false;
  // Bumped on every (re)build/disconnect so in-flight async recoveries notice
  // they were superseded.
  let gen = 0;
  let wakeBound = false;
  let lastWakeAt = -Infinity;
  // Access token the current EventSource URL was built with, and whether a
  // token recovery was already tried since the stream last opened.
  let builtToken: string | undefined;
  let recoveryTried = false;

  const closeSource = () => {
    if (eventSource) {
      eventSource.close();
      eventSource = null;
    }
  };

  const clearReconnectTimer = () => {
    if (reconnectTimer !== null) {
      clearTimeout(reconnectTimer);
      reconnectTimer = null;
    }
  };

  const onWake = (e?: Event) => {
    if (e?.type === "visibilitychange" && document.visibilityState !== "visible") return;
    if (!currentSessionId) return;
    // Background tabs throttle timers and may have stalled a retry: retry now.
    // Healthy streams and recoveries in flight are left alone. A failed
    // connection gets one probe: attempts is not reset, so a failing probe goes
    // straight back to failed.
    const stalled =
      state.value === "failed" ||
      reconnectTimer !== null ||
      (eventSource !== null && eventSource.readyState === EventSource.CLOSED);
    if (!stalled) return;
    const now = Date.now();
    if (now - lastWakeAt < WAKE_RETRY_MIN_INTERVAL_MS) return;
    lastWakeAt = now;
    lost = true;
    reopen(currentSessionId, false);
  };

  const bindWake = () => {
    if (wakeBound || typeof document === "undefined") return;
    document.addEventListener("visibilitychange", onWake);
    window.addEventListener("online", onWake);
    wakeBound = true;
  };

  const unbindWake = () => {
    if (!wakeBound) return;
    document.removeEventListener("visibilitychange", onWake);
    window.removeEventListener("online", onWake);
    wakeBound = false;
  };

  const disconnect = () => {
    gen++;
    clearReconnectTimer();
    closeSource();
    unbindWake();
    currentSessionId = null;
    lastWakeAt = -Infinity;
    lastEventId = 0;
    attempts = 0;
    lost = false;
    recoveryTried = false;
    builtToken = undefined;
    state.value = "closed";
  };

  const parseAndDispatch = (handler?: (ev: SessionEvent) => void) => (e: MessageEvent) => {
    try {
      const data: SessionEvent = JSON.parse(e.data);
      if (typeof data.eventId === "number" && data.eventId > lastEventId) {
        lastEventId = data.eventId;
      }
      handler?.(data);
    } catch (err) {
      console.error("Failed to parse SSE data:", err, e.data);
    }
  };

  // scheduleReconnect retries with backoff. The delay grows per consecutive
  // failure and resets once a stream opens.
  const scheduleReconnect = (sessionId: string, myGen: number, firstImmediate = false) => {
    clearReconnectTimer();
    if (attempts >= MAX_RECONNECT_ATTEMPTS) {
      state.value = "failed";
      // Let a later probe try a token refresh again.
      recoveryTried = false;
      return;
    }
    const delay = firstImmediate && attempts === 0 ? 0 : reconnectDelay(attempts);
    attempts++;
    reconnectTimer = setTimeout(() => {
      reconnectTimer = null;
      if (gen !== myGen || currentSessionId !== sessionId) return;
      reopen(sessionId, false);
    }, delay);
  };

  // rebuildWithNewToken rebuilds after the stored token changed. Even at the
  // attempt cap this builds one stream: it is bounded because the new stream
  // carries the stored token and recoveryTried is set, so its next CLOSED takes
  // the plain counted path back to failed.
  const rebuildWithNewToken = (sessionId: string, myGen: number) => {
    if (attempts >= MAX_RECONNECT_ATTEMPTS) {
      clearReconnectTimer();
      // The token was just replaced; refreshing again would not help.
      recoveryTried = true;
      reopen(sessionId, false);
      return;
    }
    scheduleReconnect(sessionId, myGen, true);
  };

  // recoverAndReconnect refreshes the session after the stream was rejected
  // (a 401 is indistinguishable from other CLOSED causes) through lib/api/auth.ts's
  // coordinated refresh, then rebuilds the EventSource: the browser's built-in
  // reconnect replays the original URL and therefore the stale token.
  const recoverAndReconnect = async (sessionId: string, myGen: number) => {
    const outcome = await recoverSession();
    // A newer connect()/reopen()/disconnect() superseded us while we waited.
    if (gen !== myGen || currentSessionId !== sessionId) return;

    if (outcome === "recovered") {
      // Still counts as an attempt, so a non-auth failure cannot loop here.
      rebuildWithNewToken(sessionId, myGen);
      return;
    }
    // "retry": the provider is unreachable; keep the session, try again later.
    // "login": recoverSession already navigated away.
    // Nothing rotated, so allow another refresh on the next CLOSED.
    if (outcome === "retry") {
      recoveryTried = false;
      scheduleReconnect(sessionId, myGen);
    }
  };

  const buildSource = (sessionId: string, myGen: number) => {
    builtToken = getStoredTokens()?.access_token;
    const es = new EventSource(eventsUrl(sessionId, lastEventId));

    es.onopen = () => {
      if (eventSource !== es) return;
      const reconnected = lost;
      lost = false;
      attempts = 0;
      recoveryTried = false;
      state.value = "open";
      callbacks.onOpen?.({ reconnected, resumed: lastEventId > 0 });
    };

    es.addEventListener("message", parseAndDispatch(callbacks.onMessage));
    es.addEventListener("status", parseAndDispatch(callbacks.onStatus));
    es.addEventListener("title", parseAndDispatch(callbacks.onTitle));
    es.addEventListener("artifact", parseAndDispatch(callbacks.onArtifact));
    es.addEventListener("done", parseAndDispatch(callbacks.onDone));
    // resync carries the topic's high-water mark: adopt it even when lower, so
    // the next reconnect resumes from the server's numbering.
    es.addEventListener("resync", (e: MessageEvent) => {
      try {
        const data: SessionEvent = JSON.parse(e.data);
        if (typeof data.eventId === "number") lastEventId = data.eventId;
        callbacks.onResync?.(data);
      } catch (err) {
        console.error("Failed to parse SSE data:", err, e.data);
      }
    });
    es.addEventListener("queue", parseAndDispatch(callbacks.onQueue));
    es.addEventListener("subagent", parseAndDispatch(callbacks.onSubagent));
    es.addEventListener("subagent_artifact", parseAndDispatch(callbacks.onSubagentArtifact));

    es.onerror = (err) => {
      if (eventSource !== es) return; // a newer connection already took over
      callbacks.onError?.(err);
      lost = true;
      state.value = "reconnecting";
      // CONNECTING means the browser is retrying on its own (with the
      // Last-Event-ID header); only CLOSED is terminal (non-2xx response).
      if (es.readyState !== EventSource.CLOSED) return;
      closeSource();
      // Recover the token only when it is plausibly stale: not already
      // replaced by another tab, and not tried since the stream last opened.
      // Other CLOSED causes (502, 404, 403) just back off.
      const tokenStale = getStoredTokens()?.access_token === builtToken;
      if (isAuthEnabled() && tokenStale && !recoveryTried) {
        recoveryTried = true;
        void recoverAndReconnect(sessionId, myGen);
      } else if (isAuthEnabled() && !tokenStale) {
        // Another tab already refreshed: just rebuild with the new token.
        rebuildWithNewToken(sessionId, myGen);
      } else {
        scheduleReconnect(sessionId, myGen);
      }
    };

    return es;
  };

  // reopen rebuilds the stream for the current session, resuming from the last
  // seen event id. immediate resets the backoff (explicit user action only;
  // a tab becoming visible or going online keeps it).
  function reopen(sessionId: string, immediate: boolean) {
    clearReconnectTimer();
    closeSource();
    const myGen = ++gen;
    if (immediate) attempts = 0;
    state.value = lost ? "reconnecting" : "connecting";
    eventSource = buildSource(sessionId, myGen);
  }

  const connect = (sessionId: string) => {
    if (!sessionId) return;
    if (currentSessionId === sessionId) {
      // Already streaming; otherwise (CLOSED, waiting out a backoff) retry now.
      if (eventSource && eventSource.readyState !== EventSource.CLOSED) return;
      lost = true;
      reopen(sessionId, true);
      return;
    }
    gen++;
    clearReconnectTimer();
    closeSource();
    currentSessionId = sessionId;
    lastEventId = 0;
    attempts = 0;
    lost = false;
    recoveryTried = false;
    builtToken = undefined;
    lastWakeAt = -Infinity;
    bindWake();
    reopen(sessionId, true);
  };

  return {
    state,
    connect,
    disconnect,
    getCurrentSessionId: () => currentSessionId,
    getLastEventId: () => lastEventId,
  };
}
