import type { SessionEvent } from "../types";
import { appendAccessToken, isAuthEnabled, recoverSession } from "../lib/api";

// AUTH_RECONNECT_DELAY_MS is the backoff before retrying a stream whose session
// could not be recovered because the provider was unreachable.
const AUTH_RECONNECT_DELAY_MS = 5000;

export interface SessionEventsCallbacks {
  onMessage?: (ev: SessionEvent) => void;
  onStatus?: (ev: SessionEvent) => void;
  onTitle?: (ev: SessionEvent) => void;
  onArtifact?: (ev: SessionEvent) => void;
  onDone?: (ev: SessionEvent) => void;
  onResync?: (ev: SessionEvent) => void;
  onQueue?: (ev: SessionEvent) => void;
  onError?: (err: Event) => void;
  onOpen?: () => void;
}

// eventsUrl carries the bearer token in the query string: EventSource cannot set
// request headers. It is a no-op while auth is disabled.
function eventsUrl(sessionId: string): string {
  return appendAccessToken(`/api/sessions/${encodeURIComponent(sessionId)}/events`);
}

export function useSessionEvents(callbacks: SessionEventsCallbacks = {}) {
  let eventSource: EventSource | null = null;
  let currentSessionId: string | null = null;
  let reconnectTimer: ReturnType<typeof setTimeout> | null = null;

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

  const disconnect = () => {
    clearReconnectTimer();
    closeSource();
    currentSessionId = null;
  };

  const parseAndDispatch = (handler?: (ev: SessionEvent) => void) => (e: MessageEvent) => {
    try {
      const data: SessionEvent = JSON.parse(e.data);
      handler?.(data);
    } catch (err) {
      console.error("Failed to parse SSE data:", err, e.data);
    }
  };

  // recoverAndReconnect refreshes the session after the stream was rejected with
  // 401 and rebuilds the EventSource, because the browser's built-in reconnect
  // replays the original URL and therefore the stale token.
  const recoverAndReconnect = async (sessionId: string) => {
    const outcome = await recoverSession();
    // A newer connect()/disconnect() superseded us while we waited.
    if (currentSessionId !== sessionId) return;

    if (outcome === "recovered") {
      connect(sessionId);
      return;
    }
    if (outcome === "retry") {
      // The provider is unreachable; keep the session and try again later.
      clearReconnectTimer();
      reconnectTimer = setTimeout(() => {
        reconnectTimer = null;
        if (currentSessionId === sessionId) connect(sessionId);
      }, AUTH_RECONNECT_DELAY_MS);
    }
    // "login": recoverSession already navigated away.
  };

  const buildSource = (sessionId: string) => {
    const es = new EventSource(eventsUrl(sessionId));

    es.onopen = () => {
      callbacks.onOpen?.();
    };

    es.addEventListener("message", parseAndDispatch(callbacks.onMessage));
    es.addEventListener("status", parseAndDispatch(callbacks.onStatus));
    es.addEventListener("title", parseAndDispatch(callbacks.onTitle));
    es.addEventListener("artifact", parseAndDispatch(callbacks.onArtifact));
    es.addEventListener("done", parseAndDispatch(callbacks.onDone));
    es.addEventListener("resync", parseAndDispatch(callbacks.onResync));
    es.addEventListener("queue", parseAndDispatch(callbacks.onQueue));

    es.onerror = (err) => {
      callbacks.onError?.(err);
      // CONNECTING means the browser is retrying on its own; only CLOSED is
      // terminal (the server answered with a non-2xx status).
      if (es.readyState !== EventSource.CLOSED) return;
      if (eventSource !== es) return; // a newer connection already took over
      closeSource();
      if (!isAuthEnabled()) {
        // External-SSO deployments recover on the next apiFetch 401, which
        // reloads the page; do not reload straight from a stream failure.
        return;
      }
      void recoverAndReconnect(sessionId);
    };

    return es;
  };

  const connect = (sessionId: string) => {
    if (!sessionId) return;
    if (eventSource && currentSessionId === sessionId) {
      return;
    }
    clearReconnectTimer();
    closeSource();
    currentSessionId = sessionId;
    eventSource = buildSource(sessionId);
  };

  return {
    connect,
    disconnect,
    getCurrentSessionId: () => currentSessionId,
  };
}
