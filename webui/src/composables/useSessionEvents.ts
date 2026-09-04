import type { SessionEvent } from "../types";

export interface SessionEventsCallbacks {
  onMessage?: (ev: SessionEvent) => void;
  onStatus?: (ev: SessionEvent) => void;
  onTitle?: (ev: SessionEvent) => void;
  onArtifact?: (ev: SessionEvent) => void;
  onDone?: (ev: SessionEvent) => void;
  onResync?: (ev: SessionEvent) => void;
  onQueue?: (ev: SessionEvent) => void;
  onAuthExpired?: (ev: SessionEvent) => void;
  onError?: (err: Event) => void;
  onOpen?: () => void;
}

export function useSessionEvents(callbacks: SessionEventsCallbacks = {}) {
  let eventSource: EventSource | null = null;
  let currentSessionId: string | null = null;

  const disconnect = () => {
    if (eventSource) {
      eventSource.close();
      eventSource = null;
    }
    currentSessionId = null;
  };

  const connect = (sessionId: string) => {
    if (!sessionId) return;
    if (eventSource && currentSessionId === sessionId) {
      return;
    }
    disconnect();
    currentSessionId = sessionId;

    const url = `/api/sessions/${encodeURIComponent(sessionId)}/events`;
    const es = new EventSource(url);
    eventSource = es;

    es.onopen = () => {
      callbacks.onOpen?.();
    };

    const parseAndDispatch = (handler?: (ev: SessionEvent) => void) => (e: MessageEvent) => {
      try {
        const data: SessionEvent = JSON.parse(e.data);
        handler?.(data);
      } catch (err) {
        console.error("Failed to parse SSE data:", err, e.data);
      }
    };

    es.addEventListener("message", parseAndDispatch(callbacks.onMessage));
    es.addEventListener("status", parseAndDispatch(callbacks.onStatus));
    es.addEventListener("title", parseAndDispatch(callbacks.onTitle));
    es.addEventListener("artifact", parseAndDispatch(callbacks.onArtifact));
    es.addEventListener("done", parseAndDispatch(callbacks.onDone));
    es.addEventListener("resync", parseAndDispatch(callbacks.onResync));
    es.addEventListener("queue", parseAndDispatch(callbacks.onQueue));
    es.addEventListener("auth_expired", (e: MessageEvent) => {
      parseAndDispatch(callbacks.onAuthExpired)(e);
      if (typeof window !== "undefined") {
        const url = new URL(window.location.href);
        url.searchParams.set("_auth_refresh", Date.now().toString());
        window.location.href = url.toString();
      }
    });

    es.onerror = (err) => {
      callbacks.onError?.(err);
      if (es.readyState === EventSource.CLOSED) {
        disconnect();
      }
    };
  };

  return {
    connect,
    disconnect,
    getCurrentSessionId: () => currentSessionId,
  };
}
