import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { useSessionEvents } from "./useSessionEvents";
import type { SessionEvent } from "../types";

class MockEventSource {
  static instances: MockEventSource[] = [];
  url: string;
  listeners: Record<string, ((e: any) => void)[]> = {};
  onopen: (() => void) | null = null;
  onerror: ((err: any) => void) | null = null;
  closed = false;

  constructor(url: string) {
    this.url = url;
    MockEventSource.instances.push(this);
  }

  addEventListener(event: string, handler: (e: any) => void) {
    if (!this.listeners[event]) {
      this.listeners[event] = [];
    }
    this.listeners[event].push(handler);
  }

  removeEventListener(event: string, handler: (e: any) => void) {
    if (this.listeners[event]) {
      this.listeners[event] = this.listeners[event].filter((h) => h !== handler);
    }
  }

  emit(event: string, data: any) {
    const handlers = this.listeners[event] || [];
    for (const h of handlers) {
      h({ data: JSON.stringify(data) });
    }
  }

  close() {
    this.closed = true;
  }
}

describe("useSessionEvents", () => {
  beforeEach(() => {
    MockEventSource.instances = [];
    (globalThis as any).EventSource = MockEventSource;
  });

  afterEach(() => {
    delete (globalThis as any).EventSource;
  });

  it("should connect to SSE endpoint and parse events", () => {
    const onMessage = vi.fn<(ev: SessionEvent) => void>();
    const onStatus = vi.fn<(ev: SessionEvent) => void>();
    const onTitle = vi.fn<(ev: SessionEvent) => void>();
    const onArtifact = vi.fn<(ev: SessionEvent) => void>();
    const onDone = vi.fn<(ev: SessionEvent) => void>();
    const onResync = vi.fn<(ev: SessionEvent) => void>();

    const { connect, disconnect, getCurrentSessionId } = useSessionEvents({
      onMessage,
      onStatus,
      onTitle,
      onArtifact,
      onDone,
      onResync,
    });

    connect("chat-123");
    expect(getCurrentSessionId()).toBe("chat-123");
    expect(MockEventSource.instances.length).toBe(1);
    expect(MockEventSource.instances[0].url).toBe("/api/sessions/chat-123/events");

    const es = MockEventSource.instances[0];

    const messageEvent: SessionEvent = {
      eventId: 1,
      chatId: "chat-123",
      type: "message",
      message: {
        id: "msg-1",
        role: "assistant",
        content: "Hello world",
      },
      timestamp: Date.now(),
    };
    es.emit("message", messageEvent);
    expect(onMessage).toHaveBeenCalledWith(messageEvent);

    const statusEvent: SessionEvent = {
      eventId: 2,
      chatId: "chat-123",
      type: "status",
      payload: { isRunning: true },
      timestamp: Date.now(),
    };
    es.emit("status", statusEvent);
    expect(onStatus).toHaveBeenCalledWith(statusEvent);

    const titleEvent: SessionEvent = {
      eventId: 3,
      chatId: "chat-123",
      type: "title",
      payload: { title: "New Title" },
      timestamp: Date.now(),
    };
    es.emit("title", titleEvent);
    expect(onTitle).toHaveBeenCalledWith(titleEvent);

    const artifactEvent: SessionEvent = {
      eventId: 4,
      chatId: "chat-123",
      type: "artifact",
      payload: { artifacts: ["main.go"] },
      timestamp: Date.now(),
    };
    es.emit("artifact", artifactEvent);
    expect(onArtifact).toHaveBeenCalledWith(artifactEvent);

    const doneEvent: SessionEvent = {
      eventId: 5,
      chatId: "chat-123",
      type: "done",
      timestamp: Date.now(),
    };
    es.emit("done", doneEvent);
    expect(onDone).toHaveBeenCalledWith(doneEvent);

    const resyncEvent: SessionEvent = {
      eventId: 6,
      chatId: "chat-123",
      type: "resync",
      timestamp: Date.now(),
    };
    es.emit("resync", resyncEvent);
    expect(onResync).toHaveBeenCalledWith(resyncEvent);

    disconnect();
    expect(es.closed).toBe(true);
    expect(getCurrentSessionId()).toBeNull();
  });

  it("should trigger onQueue callback when queue event is received", () => {
    const onQueue = vi.fn<(ev: SessionEvent) => void>();

    const { connect, disconnect } = useSessionEvents({
      onQueue,
    });

    connect("chat-queue-test");
    const es = MockEventSource.instances[0];

    const queueEvent: SessionEvent = {
      eventId: 7,
      chatId: "chat-queue-test",
      type: "queue",
      payload: {
        queue: [
          {
            id: "qmsg-1",
            chatId: "chat-queue-test",
            prompt: "Queued prompt 1",
            createdAt: "2026-09-03T20:00:00Z",
            updatedAt: "2026-09-03T20:00:00Z",
          },
        ],
      },
      timestamp: Date.now(),
    };

    es.emit("queue", queueEvent);
    expect(onQueue).toHaveBeenCalledWith(queueEvent);

    disconnect();
    expect(es.closed).toBe(true);
  });
});
