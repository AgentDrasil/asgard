// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import {
  useSessionEvents as useSessionEventsRaw,
  reconnectDelay,
  MAX_RECONNECT_ATTEMPTS,
  WAKE_RETRY_MIN_INTERVAL_MS,
} from "./useSessionEvents";

class MockEventSource {
  static instances: MockEventSource[] = [];
  static readonly CONNECTING = 0;
  static readonly OPEN = 1;
  static readonly CLOSED = 2;

  url: string;
  readyState = MockEventSource.CONNECTING;
  closed = false;
  listeners: Record<string, ((e: any) => void)[]> = {};
  onopen: (() => void) | null = null;
  onerror: ((err: any) => void) | null = null;

  constructor(url: string) {
    this.url = url;
    MockEventSource.instances.push(this);
  }
  addEventListener(event: string, handler: (e: any) => void) {
    (this.listeners[event] ??= []).push(handler);
  }
  close() {
    this.closed = true;
    this.readyState = MockEventSource.CLOSED;
  }
  open() {
    this.readyState = MockEventSource.OPEN;
    this.onopen?.();
  }
  emit(event: string, data: any) {
    for (const h of this.listeners[event] ?? []) h({ data: JSON.stringify(data) });
  }
  // fail simulates a terminal error (non-2xx response, e.g. a proxy 502).
  fail() {
    this.readyState = MockEventSource.CLOSED;
    this.onerror?.(new Event("error"));
  }
}

const owners: { disconnect: () => void }[] = [];
function useSessionEvents(...args: Parameters<typeof useSessionEventsRaw>) {
  const o = useSessionEventsRaw(...args);
  owners.push(o);
  return o;
}

const last = () => MockEventSource.instances[MockEventSource.instances.length - 1];

function setVisibility(value: "visible" | "hidden") {
  Object.defineProperty(document, "visibilityState", { value, configurable: true });
  document.dispatchEvent(new Event("visibilitychange"));
}

describe("useSessionEvents lifecycle", () => {
  beforeEach(() => {
    MockEventSource.instances = [];
    (globalThis as any).EventSource = MockEventSource;
    vi.useFakeTimers();
    vi.spyOn(Math, "random").mockReturnValue(1);
  });

  afterEach(() => {
    owners.splice(0).forEach((o) => o.disconnect());
    vi.useRealTimers();
    vi.restoreAllMocks();
    delete (globalThis as any).EventSource;
    Object.defineProperty(document, "visibilityState", { value: "visible", configurable: true });
  });

  it("caps the jittered backoff", () => {
    vi.spyOn(Math, "random").mockReturnValue(0);
    expect(reconnectDelay(0)).toBe(500);
    expect(reconnectDelay(20)).toBe(15000);
    vi.spyOn(Math, "random").mockReturnValue(1);
    expect(reconnectDelay(1)).toBe(2000);
    expect(reconnectDelay(20)).toBe(30000);
  });

  it("reconnects after CLOSED with growing backoff and resets after open", async () => {
    const { connect } = useSessionEvents({});
    connect("chat-1");
    last().fail();

    await vi.advanceTimersByTimeAsync(999);
    expect(MockEventSource.instances).toHaveLength(1);
    await vi.advanceTimersByTimeAsync(1);
    expect(MockEventSource.instances).toHaveLength(2);

    last().fail();
    await vi.advanceTimersByTimeAsync(1999);
    expect(MockEventSource.instances).toHaveLength(2);
    await vi.advanceTimersByTimeAsync(1);
    expect(MockEventSource.instances).toHaveLength(3);

    last().open();
    last().fail();
    await vi.advanceTimersByTimeAsync(1000);
    expect(MockEventSource.instances).toHaveLength(4);
  });

  it("carries lastEventId across reconnects", async () => {
    const { connect, getLastEventId } = useSessionEvents({});
    connect("chat-1");
    expect(last().url).toBe("/api/sessions/chat-1/events");
    last().open();
    last().emit("message", { eventId: 4, chatId: "chat-1" });
    last().emit("done", { eventId: 7, chatId: "chat-1" });
    last().emit("status", { eventId: 5, chatId: "chat-1" }); // never goes backwards
    expect(getLastEventId()).toBe(7);

    last().fail();
    await vi.advanceTimersByTimeAsync(1000);
    expect(last().url).toBe("/api/sessions/chat-1/events?lastEventId=7");

    last().open();
    last().emit("message", { eventId: 9, chatId: "chat-1" });
    last().fail();
    await vi.advanceTimersByTimeAsync(1000);
    expect(last().url).toBe("/api/sessions/chat-1/events?lastEventId=9");
  });

  it("starts from scratch when switching sessions", () => {
    const { connect } = useSessionEvents({});
    connect("chat-1");
    last().emit("message", { eventId: 4, chatId: "chat-1" });
    connect("chat-2");
    expect(last().url).toBe("/api/sessions/chat-2/events");
  });

  it("forwards resync and advances the resume point", () => {
    const onResync = vi.fn<() => void>();
    const { connect, getLastEventId } = useSessionEvents({ onResync });
    connect("chat-1");
    last().emit("resync", { eventId: 12, chatId: "chat-1", type: "resync" });
    expect(onResync).toHaveBeenCalledTimes(1);
    expect(getLastEventId()).toBe(12);
  });

  it("adopts a lower resync event id as the resume point", () => {
    const { connect, getLastEventId } = useSessionEvents({});
    connect("chat-1");
    last().emit("message", { eventId: 500, chatId: "chat-1" });
    last().emit("resync", { eventId: 120, chatId: "chat-1", type: "resync" });
    expect(getLastEventId()).toBe(120);
    last().emit("resync", { eventId: 0, chatId: "chat-1", type: "resync" });
    expect(getLastEventId()).toBe(0);
  });

  it("gives up after repeated failures and retries on connect()", async () => {
    const { connect, state } = useSessionEvents({});
    connect("chat-1");
    for (let i = 0; i < MAX_RECONNECT_ATTEMPTS; i++) {
      last().fail();
      await vi.advanceTimersByTimeAsync(30000);
    }
    expect(MockEventSource.instances).toHaveLength(MAX_RECONNECT_ATTEMPTS + 1);
    last().fail();
    expect(state.value).toBe("failed");
    await vi.advanceTimersByTimeAsync(120000);
    expect(MockEventSource.instances).toHaveLength(MAX_RECONNECT_ATTEMPTS + 1);

    connect("chat-1");
    expect(MockEventSource.instances).toHaveLength(MAX_RECONNECT_ATTEMPTS + 2);
    expect(state.value).toBe("reconnecting");
  });

  async function failOut(connect: (id: string) => void) {
    connect("chat-1");
    for (let i = 0; i < MAX_RECONNECT_ATTEMPTS; i++) {
      last().fail();
      await vi.advanceTimersByTimeAsync(30000);
    }
    last().fail();
  }

  it("probes once from failed when the tab becomes visible, then stays failed", async () => {
    const { connect, state } = useSessionEvents({});
    await failOut(connect);
    expect(state.value).toBe("failed");
    const n = MockEventSource.instances.length;

    setVisibility("hidden");
    setVisibility("visible");
    expect(MockEventSource.instances).toHaveLength(n + 1);
    last().fail();
    expect(state.value).toBe("failed");
    await vi.advanceTimersByTimeAsync(120000);
    expect(MockEventSource.instances).toHaveLength(n + 1);

    // A successful probe recovers and resets the budget.
    await vi.advanceTimersByTimeAsync(WAKE_RETRY_MIN_INTERVAL_MS);
    setVisibility("visible");
    expect(MockEventSource.instances).toHaveLength(n + 2);
    last().open();
    expect(state.value).toBe("open");
  });

  it("probes once from failed when the network comes back", async () => {
    const { connect, state, disconnect } = useSessionEvents({});
    await failOut(connect);
    const n = MockEventSource.instances.length;
    window.dispatchEvent(new Event("online"));
    expect(MockEventSource.instances).toHaveLength(n + 1);
    last().fail();
    expect(state.value).toBe("failed");

    disconnect();
    window.dispatchEvent(new Event("online"));
    expect(MockEventSource.instances).toHaveLength(n + 1);
  });

  it("rate-limits immediate wake-up retries so flipping tabs cannot burn the budget", async () => {
    const { connect } = useSessionEvents({});
    connect("chat-1");
    last().fail(); // timer pending
    setVisibility("visible");
    expect(MockEventSource.instances).toHaveLength(2);
    last().fail();
    const n = MockEventSource.instances.length;
    for (let i = 0; i < 5; i++) setVisibility("visible");
    expect(MockEventSource.instances).toHaveLength(n);
    await vi.advanceTimersByTimeAsync(WAKE_RETRY_MIN_INTERVAL_MS);
    setVisibility("visible");
    expect(MockEventSource.instances.length).toBeGreaterThan(n);
  });

  it("keeps the backoff when the tab is flipped repeatedly", async () => {
    const { connect } = useSessionEvents({});
    connect("chat-1");
    last().fail();
    await vi.advanceTimersByTimeAsync(1000);
    last().fail(); // attempt 2: 2s backoff pending
    setVisibility("hidden");
    setVisibility("visible"); // timer pending: retries now, backoff not reset
    expect(MockEventSource.instances).toHaveLength(3);
    last().fail();
    await vi.advanceTimersByTimeAsync(3999);
    expect(MockEventSource.instances).toHaveLength(3);
    await vi.advanceTimersByTimeAsync(1);
    expect(MockEventSource.instances).toHaveLength(4);

    // A healthy-but-connecting stream is not rebuilt by a visibility flip.
    setVisibility("hidden");
    setVisibility("visible");
    expect(MockEventSource.instances).toHaveLength(4);
  });

  it("reports whether a reconnect could be resumed", async () => {
    const onOpen = vi.fn<() => void>();
    const { connect } = useSessionEvents({ onOpen });
    connect("chat-1");
    last().open();
    expect(onOpen).toHaveBeenLastCalledWith({ reconnected: false, resumed: false });

    // Lost before any event was seen: the gap cannot be replayed.
    last().fail();
    await vi.advanceTimersByTimeAsync(1000);
    last().open();
    expect(onOpen).toHaveBeenLastCalledWith({ reconnected: true, resumed: false });

    last().emit("message", { eventId: 3, chatId: "chat-1" });
    last().fail();
    await vi.advanceTimersByTimeAsync(1000);
    last().open();
    expect(onOpen).toHaveBeenLastCalledWith({ reconnected: true, resumed: true });
  });

  it("walks through the connection states", async () => {
    const { connect, disconnect, state } = useSessionEvents({});
    expect(state.value).toBe("closed");
    connect("chat-1");
    expect(state.value).toBe("connecting");
    last().open();
    expect(state.value).toBe("open");
    last().fail();
    expect(state.value).toBe("reconnecting");
    await vi.advanceTimersByTimeAsync(1000);
    expect(state.value).toBe("reconnecting");
    last().open();
    expect(state.value).toBe("open");
    disconnect();
    expect(state.value).toBe("closed");
  });

  it("shows reconnecting while the browser retries on its own", () => {
    const { connect, state } = useSessionEvents({});
    connect("chat-1");
    last().open();
    last().readyState = MockEventSource.CONNECTING;
    last().onerror?.(new Event("error"));
    expect(state.value).toBe("reconnecting");
    expect(MockEventSource.instances).toHaveLength(1);
    last().open();
    expect(state.value).toBe("open");
  });

  it("does not retry after disconnect", async () => {
    const { connect, disconnect } = useSessionEvents({});
    connect("chat-1");
    last().fail();
    disconnect();
    await vi.advanceTimersByTimeAsync(60000);
    expect(MockEventSource.instances).toHaveLength(1);
  });

  it("reconnects immediately when the tab becomes visible again", async () => {
    const { connect, state } = useSessionEvents({});
    connect("chat-1");
    last().open();
    last().emit("message", { eventId: 2, chatId: "chat-1" });
    last().fail();
    setVisibility("hidden");
    expect(MockEventSource.instances).toHaveLength(1);

    setVisibility("visible");
    expect(MockEventSource.instances).toHaveLength(2);
    expect(last().url).toBe("/api/sessions/chat-1/events?lastEventId=2");
    expect(state.value).toBe("reconnecting");

    // The pending backoff timer was superseded: no extra stream.
    await vi.advanceTimersByTimeAsync(5000);
    expect(MockEventSource.instances).toHaveLength(2);
  });

  it("ignores visibility changes while the stream is open or after disconnect", () => {
    const { connect, disconnect } = useSessionEvents({});
    connect("chat-1");
    last().open();
    setVisibility("hidden");
    setVisibility("visible");
    expect(MockEventSource.instances).toHaveLength(1);

    last().fail();
    disconnect();
    setVisibility("visible");
    expect(MockEventSource.instances).toHaveLength(1);
  });
});
