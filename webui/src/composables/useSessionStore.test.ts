import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { ref } from "vue";
import { useSessionStore } from "./useSessionStore";
import * as api from "../lib/api";
import type { ChatSession, AgentInfo, SessionEvent } from "../types";

class MockEventSource {
  static instances: MockEventSource[] = [];
  url: string;
  listeners: Record<string, ((e: any) => void)[]> = {};
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

  removeEventListener() {}

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

describe("useSessionStore", () => {
  beforeEach(() => {
    MockEventSource.instances = [];
    (globalThis as any).EventSource = MockEventSource;
  });

  afterEach(() => {
    delete (globalThis as any).EventSource;
    vi.restoreAllMocks();
  });

  it("should open session, fetch snapshot, and handle SSE events", async () => {
    const mockSession: ChatSession = {
      chatID: "session-1",
      title: "Test Chat",
      currentAgent: "agent-1",
      runDir: "/workspace",
      isRunning: true,
      messages: [
        {
          id: "user-1",
          role: "user",
          content: "Hello",
          timestamp: 1000,
        },
      ],
      artifacts: ["file1.go"],
    };

    const agents = ref<AgentInfo[]>([
      { id: "agent-1", name: "Coder", description: "", run_dirs: [] },
    ]);

    vi.spyOn(api, "getSession").mockResolvedValue(mockSession);

    const store = useSessionStore({ agents });
    await store.openSession("session-1");

    expect(store.activeSessionId.value).toBe("session-1");
    expect(store.activeSession.value?.title).toBe("Test Chat");
    expect(store.activeAgent.value?.name).toBe("Coder");
    expect(store.messages.value.length).toBe(1);
    expect(store.messages.value[0].content).toBe("Hello");
    expect(store.isRunning.value).toBe(true);
    expect(store.artifacts.value).toEqual(["file1.go"]);

    const es = MockEventSource.instances[0];
    expect(es).toBeDefined();

    // 1. Receive status event with agent id -> resolved to Coder
    const statusEvent: SessionEvent = {
      eventId: 2,
      chatId: "session-1",
      type: "status",
      payload: { agent: "agent-1", isRunning: true },
      timestamp: 1500,
    };
    es.emit("status", statusEvent);
    expect(store.workingAgentLabel.value).toBe("Coder");

    // 2. Receive message event
    const msgEvent: SessionEvent = {
      eventId: 3,
      chatId: "session-1",
      type: "message",
      message: {
        id: "asst-1",
        role: "assistant",
        content: "Hi there!",
        timestamp: 2000,
      },
      timestamp: 2000,
    };
    es.emit("message", msgEvent);
    expect(store.messages.value.length).toBe(2);
    expect(store.messages.value[1].content).toBe("Hi there!");

    // 3. Receive event for another session (should be ignored)
    const otherMsgEvent: SessionEvent = {
      eventId: 4,
      chatId: "session-other",
      type: "message",
      message: {
        id: "asst-other",
        role: "assistant",
        content: "Should not appear",
        timestamp: 2500,
      },
      timestamp: 2500,
    };
    es.emit("message", otherMsgEvent);
    expect(store.messages.value.length).toBe(2);

    // 4. Receive artifact event
    const artEvent: SessionEvent = {
      eventId: 5,
      chatId: "session-1",
      type: "artifact",
      payload: { artifacts: ["file2.go"] },
      timestamp: 3000,
    };
    es.emit("artifact", artEvent);
    expect(store.artifacts.value).toEqual(["file1.go", "file2.go"]);

    // 5. Receive title event
    const titleEvent: SessionEvent = {
      eventId: 6,
      chatId: "session-1",
      type: "title",
      payload: { title: "Updated Title" },
      timestamp: 4000,
    };
    es.emit("title", titleEvent);
    expect(store.activeSession.value?.title).toBe("Updated Title");

    // 6. Receive done event
    const doneEvent: SessionEvent = {
      eventId: 7,
      chatId: "session-1",
      type: "done",
      timestamp: 5000,
    };
    es.emit("done", doneEvent);
    expect(store.isRunning.value).toBe(false);
    expect(store.isInputBusy.value).toBe(false);
    expect(store.workingAgentLabel.value).toBeNull();
  });

  it("should cleanly switch sessions without leaking messages or isRunning state", async () => {
    const sessionA: ChatSession = {
      chatID: "session-A",
      title: "Chat A",
      currentAgent: "agent-1",
      runDir: "/workspace",
      isRunning: true,
      messages: [
        { id: "msg-a1", role: "user", content: "A1", timestamp: 1000 },
        { id: "msg-a2", role: "assistant", content: "A2", timestamp: 2000 },
      ],
      artifacts: ["art-a.go"],
    };

    const sessionB: ChatSession = {
      chatID: "session-B",
      title: "Chat B",
      currentAgent: "agent-1",
      runDir: "/workspace",
      isRunning: false,
      messages: [{ id: "msg-b1", role: "user", content: "B1", timestamp: 3000 }],
      artifacts: [],
    };

    const getSessionSpy = vi.spyOn(api, "getSession").mockImplementation(async (id: string) => {
      if (id === "session-A") return sessionA;
      if (id === "session-B") return sessionB;
      return null;
    });

    const store = useSessionStore();

    // 1. Open running session A
    await store.openSession("session-A");
    expect(store.activeSessionId.value).toBe("session-A");
    expect(store.messages.value.length).toBe(2);
    expect(store.isRunning.value).toBe(true);
    expect(store.isInputBusy.value).toBe(true);
    expect(store.artifacts.value).toEqual(["art-a.go"]);

    // 2. Switch to idle session B
    await store.openSession("session-B");
    expect(store.activeSessionId.value).toBe("session-B");
    // Messages must contain ONLY session B messages (no leakage from A)
    expect(store.messages.value.length).toBe(1);
    expect(store.messages.value[0].content).toBe("B1");
    // isRunning and isInputBusy must be false
    expect(store.isRunning.value).toBe(false);
    expect(store.isInputBusy.value).toBe(false);
    expect(store.artifacts.value).toEqual([]);

    expect(getSessionSpy).toHaveBeenCalledTimes(2);
  });

  it("should send message optimistically and trigger backend execution", async () => {
    const mockSession: ChatSession = {
      chatID: "session-2",
      title: "Chat 2",
      currentAgent: "agent-1",
      runDir: "/workspace",
      messages: [],
    };

    const agents = ref<AgentInfo[]>([
      { id: "agent-1", name: "Coder", description: "", run_dirs: [] },
    ]);

    vi.spyOn(api, "getSession").mockResolvedValue(mockSession);
    const triggerSpy = vi.spyOn(api, "triggerAgentMessage").mockResolvedValue({
      status: "accepted",
      chatId: "session-2",
    });

    const store = useSessionStore({ agents });
    await store.openSession("session-2");

    await store.sendMessage("Do something", {
      selectedAgentId: "agent-1",
      selectedDir: "/workspace",
    });

    expect(store.messages.value.length).toBe(1);
    expect(store.messages.value[0].role).toBe("user");
    expect(store.messages.value[0].content).toBe("Do something");
    expect(store.isRunning.value).toBe(true);

    expect(triggerSpy).toHaveBeenCalledWith("agent-1", {
      prompt: "Do something",
      chatId: "session-2",
      runDir: "/workspace",
      model: undefined,
      metadata: expect.objectContaining({
        message_id: expect.stringMatching(/^user-/),
      }),
    });
  });

  it("should handle 409 conflict when session is already running", async () => {
    const mockSession: ChatSession = {
      chatID: "session-409",
      title: "Chat 409",
      currentAgent: "agent-1",
      runDir: "/workspace",
      isRunning: false,
      messages: [],
    };

    vi.spyOn(api, "getSession").mockResolvedValue(mockSession);
    vi.spyOn(api, "triggerAgentMessage").mockResolvedValue({
      status: "conflict",
      chatId: "session-409",
      conflict: true,
    });

    const store = useSessionStore();
    await store.openSession("session-409");

    await store.sendMessage("Concurrent prompt");

    // Optimistic user message rolled back and error card pushed
    expect(store.messages.value.some((m) => m.role === "error")).toBe(true);
    expect(store.messages.value.some((m) => m.content === "Concurrent prompt")).toBe(false);
    expect(store.loading.value).toBe(false);
    expect(store.isRunning.value).toBe(false);
  });

  it("should preserve pending optimistic user messages on resync", async () => {
    const mockSession: ChatSession = {
      chatID: "session-resync",
      title: "Chat Resync",
      currentAgent: "agent-1",
      runDir: "/workspace",
      isRunning: false,
      messages: [{ id: "msg-1", role: "assistant", content: "Snapshot msg", timestamp: 1000 }],
    };

    vi.spyOn(api, "getSession").mockResolvedValue(mockSession);
    vi.spyOn(api, "triggerAgentMessage").mockResolvedValue({
      status: "accepted",
      chatId: "session-resync",
    });

    const store = useSessionStore();
    await store.openSession("session-resync");

    // Send an optimistic message that has not yet landed in snapshot
    await store.sendMessage("Optimistic in flight");
    expect(store.messages.value.some((m) => m.content === "Optimistic in flight")).toBe(true);

    // Emit resync event
    const es = MockEventSource.instances[0];
    es.emit("resync", {
      eventId: 99,
      chatId: "session-resync",
      type: "resync",
      timestamp: 3000,
    });

    // Wait for async resync
    await vi.waitFor(() => {
      expect(store.messages.value.some((m) => m.content === "Snapshot msg")).toBe(true);
      expect(store.messages.value.some((m) => m.content === "Optimistic in flight")).toBe(true);
    });
  });

  it("should update message reply state", async () => {
    const mockSession: ChatSession = {
      chatID: "session-3",
      title: "Chat 3",
      currentAgent: "agent-1",
      runDir: "/workspace",
      messages: [
        {
          id: "ask-1",
          role: "ask_user",
          content: "Which file?",
          timestamp: 1000,
        },
      ],
    };

    vi.spyOn(api, "getSession").mockResolvedValue(mockSession);

    const store = useSessionStore();
    await store.openSession("session-3");

    expect(store.messages.value[0].replied).toBeUndefined();

    store.updateMessageReply("ask-1", "main.go");

    expect(store.messages.value[0].replied).toBe(true);
    expect(store.messages.value[0].replyText).toBe("main.go");
  });
});
