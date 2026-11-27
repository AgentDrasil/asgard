import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { useSessionStore } from "./useSessionStore";
import * as api from "../lib/api";
import type { ChatMessage, ChatSession } from "../types";

class MockEventSource {
  static instances: MockEventSource[] = [];
  listeners: Record<string, ((e: any) => void)[]> = {};
  onopen: (() => void) | null = null;
  onerror: ((err: any) => void) | null = null;
  readyState = 0;
  url: string;
  constructor(url: string) {
    this.url = url;
    MockEventSource.instances.push(this);
  }
  addEventListener(event: string, handler: (e: any) => void) {
    (this.listeners[event] ||= []).push(handler);
  }
  removeEventListener() {}
  emit(event: string, data: any) {
    for (const h of this.listeners[event] || []) h({ data: JSON.stringify(data) });
  }
  close() {}
}

// Builds a transcript of tool call/result pairs interleaved with assistant text.
function buildTranscript(n: number): ChatMessage[] {
  const out: ChatMessage[] = [];
  let t = 0;
  while (out.length < n) {
    out.push({
      id: `a-${t}`,
      role: "assistant",
      content: `text ${t}`,
      timestamp: t,
      agentName: "x",
    });
    for (let k = 0; k < 8 && out.length < n; k++) {
      out.push({
        id: `c-${t}-${k}`,
        role: "tool_call",
        content: `call ${k}`,
        timestamp: t,
        agentName: "x",
        stepIndex: k,
      });
      out.push({
        id: `r-${t}-${k}`,
        role: "tool_result",
        content: `result ${k}`,
        timestamp: t,
        agentName: "x",
        stepIndex: k,
      });
    }
    t++;
  }
  return out.slice(0, n);
}

describe("useSessionStore long-transcript performance", () => {
  beforeEach(() => {
    MockEventSource.instances = [];
    (globalThis as any).EventSource = MockEventSource;
  });
  afterEach(() => {
    delete (globalThis as any).EventSource;
    vi.restoreAllMocks();
  });

  it("streamed message events stay cheap on a 5000 message transcript", async () => {
    const session: ChatSession = {
      chatID: "s1",
      title: "t",
      currentAgent: "a",
      runDir: "/w",
      isRunning: true,
      messages: buildTranscript(5000),
      artifacts: [],
    };
    vi.spyOn(api, "getSession").mockResolvedValue(session);
    const store = useSessionStore();
    await store.openSession("s1");
    const es = MockEventSource.instances[0];
    expect(store.messages.value.length).toBeGreaterThan(0);

    const N = 600;
    const emit = (m: ChatMessage, id: number) =>
      es.emit("message", { eventId: id, chatId: "s1", type: "message", message: m, timestamp: id });

    const start = performance.now();
    for (let i = 0; i < N; i++) {
      // tool_call, then its result, then an update to the call (streaming), then text.
      const call: ChatMessage = {
        id: `nc-${i}`,
        role: "tool_call",
        content: "c",
        timestamp: i,
        agentName: "x",
        stepIndex: 1,
      };
      emit(call, i * 4);
      void store.messages.value;
      emit(
        {
          id: `nr-${i}`,
          role: "tool_result",
          content: "r",
          timestamp: i,
          agentName: "x",
          stepIndex: 1,
        },
        i * 4 + 1,
      );
      void store.messages.value;
      emit({ ...call, content: "c2" }, i * 4 + 2);
      void store.messages.value;
      emit(
        { id: `nt-${i}`, role: "assistant", content: "t", timestamp: i, agentName: "x" },
        i * 4 + 3,
      );
      void store.messages.value;
    }
    const perEvent = (performance.now() - start) / (N * 4);
    console.log(`perf: ${perEvent.toFixed(4)} ms/event over ${N * 4} events`);
    // Generous bound; the old O(n) path costs far more on a 5000 message transcript.
    expect(perEvent).toBeLessThan(0.5);
  });
});
