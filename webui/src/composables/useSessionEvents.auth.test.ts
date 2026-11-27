// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { useSessionEvents } from "./useSessionEvents";
import { initAuth, setStoredTokens } from "../lib/api";
import type { StoredTokens } from "../types";

const TOKENS: StoredTokens = {
  access_token: "access-1",
  refresh_token: "refresh-1",
  id_token: "id-1",
  expires_in: 300,
  token_type: "Bearer",
};

class MockEventSource {
  static instances: MockEventSource[] = [];
  static readonly CONNECTING = 0;
  static readonly OPEN = 1;
  static readonly CLOSED = 2;

  url: string;
  readyState = MockEventSource.OPEN;
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

  removeEventListener(event: string, handler: (e: any) => void) {
    if (this.listeners[event]) {
      this.listeners[event] = this.listeners[event].filter((h) => h !== handler);
    }
  }

  close() {
    this.closed = true;
    this.readyState = MockEventSource.CLOSED;
  }

  // fail simulates the terminal error EventSource reports when the server
  // answers with a non-2xx status (e.g. 401 from the auth middleware).
  fail() {
    this.readyState = MockEventSource.CLOSED;
    this.onerror?.(new Event("error"));
  }
}

let originalLocalStorage: Storage | undefined;
let originalLocation: Location | undefined;
let navigations: string[];
let fetchMock: ReturnType<typeof vi.spyOn>;

function jsonResponse(status: number, body?: unknown): Response {
  return {
    ok: status >= 200 && status < 300,
    status,
    json: async () => body,
  } as Response;
}

function installStorage() {
  let store: Record<string, string> = {};
  const mock = {
    getItem: (k: string) => (k in store ? store[k] : null),
    setItem: (k: string, v: string) => {
      store[k] = String(v);
    },
    removeItem: (k: string) => {
      delete store[k];
    },
    clear: () => {
      store = {};
    },
    key: (i: number) => Object.keys(store)[i] ?? null,
    get length() {
      return Object.keys(store).length;
    },
  } as unknown as Storage;

  originalLocalStorage = window.localStorage;
  Object.defineProperty(window, "localStorage", {
    value: mock,
    configurable: true,
    writable: true,
  });
}

function installLocation(href = "http://localhost/dashboard") {
  navigations = [];
  const parsed = new URL(href);
  const mock = {
    pathname: parsed.pathname,
    search: parsed.search,
    origin: parsed.origin,
    get href() {
      return href;
    },
    set href(value: string) {
      navigations.push(value);
    },
    replace: (value: string) => {
      navigations.push(value);
    },
  };
  originalLocation = window.location;
  Object.defineProperty(window, "location", { value: mock, configurable: true, writable: true });
}

async function setAuthEnabled(enabled: boolean) {
  fetchMock.mockResolvedValueOnce(jsonResponse(200, { enabled }));
  await initAuth();
}

// Composables bind document/window wake listeners; disconnect them after each
// test so they cannot react to later tests' events.
const liveDisconnects: (() => void)[] = [];
function trackedEvents(...args: Parameters<typeof useSessionEvents>) {
  const r = useSessionEvents(...args);
  liveDisconnects.push(r.disconnect);
  return r;
}

describe("useSessionEvents auth handling", () => {
  beforeEach(() => {
    MockEventSource.instances = [];
    (globalThis as any).EventSource = MockEventSource;
    installStorage();
    installLocation();
    fetchMock = vi.spyOn(globalThis, "fetch").mockResolvedValue(jsonResponse(200, {}));
  });

  afterEach(() => {
    for (const d of liveDisconnects.splice(0)) d();
    vi.useRealTimers();
    vi.restoreAllMocks();
    delete (globalThis as any).EventSource;
    if (originalLocalStorage) {
      Object.defineProperty(window, "localStorage", {
        value: originalLocalStorage,
        configurable: true,
        writable: true,
      });
    }
    if (originalLocation) {
      Object.defineProperty(window, "location", {
        value: originalLocation,
        configurable: true,
        writable: true,
      });
    }
  });

  it("omits the token while auth is disabled", async () => {
    await setAuthEnabled(false);

    const { connect } = trackedEvents({});
    connect("chat-1");

    expect(MockEventSource.instances).toHaveLength(1);
    expect(MockEventSource.instances[0].url).toBe("/api/sessions/chat-1/events");
  });

  it("carries the token in the query string while auth is enabled", async () => {
    await setAuthEnabled(true);
    setStoredTokens(TOKENS);

    const { connect } = trackedEvents({});
    connect("chat-1");

    expect(MockEventSource.instances).toHaveLength(1);
    expect(MockEventSource.instances[0].url).toBe(
      "/api/sessions/chat-1/events?access_token=access-1",
    );
  });

  it("retries a closed stream with backoff while auth is disabled", async () => {
    vi.useFakeTimers();
    vi.spyOn(Math, "random").mockReturnValue(1);
    await setAuthEnabled(false);

    const { connect } = trackedEvents({});
    connect("chat-1");
    MockEventSource.instances[0].fail();

    await vi.advanceTimersByTimeAsync(999);
    expect(MockEventSource.instances).toHaveLength(1);
    await vi.advanceTimersByTimeAsync(1);
    expect(MockEventSource.instances).toHaveLength(2);
    expect(navigations).toHaveLength(0);
  });

  it("refreshes the session and reconnects with the new token", async () => {
    await setAuthEnabled(true);
    setStoredTokens(TOKENS);

    const fresh: StoredTokens = { ...TOKENS, access_token: "access-2", refresh_token: "refresh-2" };
    fetchMock.mockImplementation(async (input: RequestInfo | URL) => {
      if (String(input) === "/auth/refresh") return jsonResponse(200, fresh);
      return jsonResponse(404);
    });

    const { connect } = trackedEvents({});
    connect("chat-1");
    MockEventSource.instances[0].fail();

    await vi.waitFor(() => expect(MockEventSource.instances).toHaveLength(2));
    expect(MockEventSource.instances[1].url).toBe(
      "/api/sessions/chat-1/events?access_token=access-2",
    );
  });

  it("does not refresh on every CLOSED when the failure is not auth related", async () => {
    vi.useFakeTimers();
    vi.spyOn(Math, "random").mockReturnValue(1);
    await setAuthEnabled(true);
    setStoredTokens(TOKENS);

    let refreshes = 0;
    fetchMock.mockImplementation(async (input: RequestInfo | URL) => {
      if (String(input) === "/auth/refresh") {
        refreshes++;
        return jsonResponse(200, { ...TOKENS, access_token: "access-2", refresh_token: "r-2" });
      }
      return jsonResponse(404);
    });

    const { connect } = trackedEvents({});
    connect("chat-1");
    MockEventSource.instances[0].fail();
    // The refresh waits out the cross-tab lock settle time, then reconnects at once.
    await vi.advanceTimersByTimeAsync(1000);
    expect(MockEventSource.instances).toHaveLength(2);
    expect(refreshes).toBe(1);

    // 502 from a proxy: the fresh token is fine, back off without refreshing.
    MockEventSource.instances[1].fail();
    await vi.advanceTimersByTimeAsync(1999);
    expect(MockEventSource.instances).toHaveLength(2);
    await vi.advanceTimersByTimeAsync(1);
    expect(MockEventSource.instances).toHaveLength(3);

    MockEventSource.instances[2].fail();
    await vi.advanceTimersByTimeAsync(3999);
    expect(MockEventSource.instances).toHaveLength(3);
    await vi.advanceTimersByTimeAsync(1);
    expect(MockEventSource.instances).toHaveLength(4);
    expect(refreshes).toBe(1);
  });

  it("sends the user to the provider when the session cannot be refreshed", async () => {
    await setAuthEnabled(true);
    setStoredTokens(TOKENS);

    fetchMock.mockImplementation(async (input: RequestInfo | URL) => {
      if (String(input) === "/auth/refresh") return jsonResponse(401, { error: "refresh failed" });
      return jsonResponse(404);
    });

    const { connect } = trackedEvents({});
    connect("chat-1");
    MockEventSource.instances[0].fail();

    await vi.waitFor(() => expect(navigations).toHaveLength(1), { timeout: 5000 });
    expect(navigations[0]).toBe("/auth/login?redirect=%2Fdashboard");
    expect(MockEventSource.instances).toHaveLength(1);
  });

  it("retries after a backoff when the provider is unreachable", async () => {
    vi.useFakeTimers();
    await setAuthEnabled(true);
    setStoredTokens(TOKENS);

    vi.spyOn(Math, "random").mockReturnValue(1);
    fetchMock.mockImplementation(async (input: RequestInfo | URL) => {
      if (String(input) === "/auth/refresh")
        return jsonResponse(503, { error: "auth unavailable" });
      return jsonResponse(404);
    });

    const { connect } = trackedEvents({});
    connect("chat-1");
    MockEventSource.instances[0].fail();

    await vi.advanceTimersByTimeAsync(100);
    expect(MockEventSource.instances).toHaveLength(1);
    expect(navigations).toHaveLength(0);

    await vi.advanceTimersByTimeAsync(1000);
    expect(MockEventSource.instances).toHaveLength(2);
  });

  it("does not reconnect a superseded session", async () => {
    await setAuthEnabled(true);
    setStoredTokens(TOKENS);

    const fresh: StoredTokens = { ...TOKENS, access_token: "access-2" };
    fetchMock.mockImplementation(async (input: RequestInfo | URL) => {
      if (String(input) === "/auth/refresh") return jsonResponse(200, fresh);
      return jsonResponse(404);
    });

    const { connect } = trackedEvents({});
    connect("chat-1");
    const first = MockEventSource.instances[0];

    // The user switches sessions while the recovery is in flight.
    connect("chat-2");
    first.fail();

    await vi.waitFor(() => expect(MockEventSource.instances.length).toBeGreaterThanOrEqual(2));
    // No third stream is opened for the abandoned session.
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(MockEventSource.instances).toHaveLength(2);
    expect(MockEventSource.instances[1].url).toContain("/api/sessions/chat-2/events");
  });

  it("tries a refresh again after a transient refresh failure", async () => {
    vi.useFakeTimers();
    vi.spyOn(Math, "random").mockReturnValue(1);
    await setAuthEnabled(true);
    setStoredTokens(TOKENS);

    let refreshes = 0;
    fetchMock.mockImplementation(async (input: RequestInfo | URL) => {
      if (String(input) === "/auth/refresh") {
        refreshes++;
        return refreshes === 1
          ? jsonResponse(503, { error: "auth unavailable" })
          : jsonResponse(200, { ...TOKENS, access_token: "access-2", refresh_token: "r-2" });
      }
      return jsonResponse(404);
    });

    const { connect } = trackedEvents({});
    connect("chat-1");
    MockEventSource.instances[0].fail();
    await vi.advanceTimersByTimeAsync(3000);
    expect(MockEventSource.instances).toHaveLength(2);
    expect(refreshes).toBe(1);

    MockEventSource.instances[1].fail();
    await vi.advanceTimersByTimeAsync(3000);
    expect(refreshes).toBe(2);
    expect(MockEventSource.instances[MockEventSource.instances.length - 1].url).toContain(
      "access_token=access-2",
    );
  });

  it("resets recovery state when switching sessions", async () => {
    vi.useFakeTimers();
    vi.spyOn(Math, "random").mockReturnValue(1);
    await setAuthEnabled(true);
    setStoredTokens(TOKENS);

    let refreshes = 0;
    fetchMock.mockImplementation(async (input: RequestInfo | URL) => {
      if (String(input) === "/auth/refresh") {
        refreshes++;
        return jsonResponse(503, { error: "auth unavailable" });
      }
      return jsonResponse(404);
    });

    const { connect } = trackedEvents({});
    connect("chat-1");
    MockEventSource.instances[0].fail();
    await vi.advanceTimersByTimeAsync(100);
    expect(refreshes).toBe(1);

    connect("chat-2");
    MockEventSource.instances[MockEventSource.instances.length - 1].fail();
    await vi.advanceTimersByTimeAsync(100);
    expect(refreshes).toBe(2);
  });

  it("rebuilds at once when another tab already rotated the token", async () => {
    vi.useFakeTimers();
    vi.spyOn(Math, "random").mockReturnValue(1);
    await setAuthEnabled(true);
    setStoredTokens(TOKENS);

    const { connect } = trackedEvents({});
    connect("chat-1");
    setStoredTokens({ ...TOKENS, access_token: "access-9" });
    MockEventSource.instances[0].fail();
    await vi.advanceTimersByTimeAsync(0);
    expect(MockEventSource.instances).toHaveLength(2);
    expect(MockEventSource.instances[1].url).toContain("access_token=access-9");
  });

  // driveToFailed exhausts the reconnect budget with a non-auth failure
  // (the token is fresh, so no refresh is attempted along the way).
  async function driveToFailed(connectFn: (id: string) => void, state: { value: string }) {
    connectFn("chat-1");
    for (let i = 0; i < 40 && state.value !== "failed"; i++) {
      const es = MockEventSource.instances[MockEventSource.instances.length - 1];
      es.fail();
      await vi.advanceTimersByTimeAsync(60_000);
    }
    expect(state.value).toBe("failed");
  }

  it("rebuilds with the refreshed token on a wake probe after failed", async () => {
    vi.useFakeTimers();
    vi.spyOn(Math, "random").mockReturnValue(1);
    await setAuthEnabled(true);
    setStoredTokens(TOKENS);

    let refreshes = 0;
    fetchMock.mockImplementation(async (input: RequestInfo | URL) => {
      if (String(input) === "/auth/refresh") {
        refreshes++;
        return jsonResponse(200, { ...TOKENS, access_token: "access-2", refresh_token: "r-2" });
      }
      return jsonResponse(404);
    });

    const { connect, state } = trackedEvents({});
    // The first CLOSED refreshes once; mark that as already spent by driving
    // to failed, then count refreshes from here.
    await driveToFailed(connect, state);
    refreshes = 0;
    const before = MockEventSource.instances.length;

    // Token goes stale, then the tab wakes up: the probe is rejected, the
    // refresh succeeds and a stream with the new token is built despite the cap.
    document.dispatchEvent(new Event("visibilitychange"));
    expect(MockEventSource.instances).toHaveLength(before + 1);
    MockEventSource.instances[before].fail();
    await vi.advanceTimersByTimeAsync(2000);
    expect(refreshes).toBe(1);
    expect(MockEventSource.instances).toHaveLength(before + 2);
    expect(MockEventSource.instances[before + 1].url).toContain("access_token=access-2");

    // Bounded: a further CLOSED goes back to failed without another refresh.
    MockEventSource.instances[before + 1].fail();
    await vi.advanceTimersByTimeAsync(60_000);
    expect(state.value).toBe("failed");
    expect(refreshes).toBe(1);
    expect(MockEventSource.instances).toHaveLength(before + 2);
  });

  it("rebuilds at the cap when another tab rotated the token", async () => {
    vi.useFakeTimers();
    vi.spyOn(Math, "random").mockReturnValue(1);
    await setAuthEnabled(true);
    setStoredTokens(TOKENS);

    const { connect, state } = trackedEvents({});
    await driveToFailed(connect, state);
    const before = MockEventSource.instances.length;

    document.dispatchEvent(new Event("visibilitychange"));
    expect(MockEventSource.instances).toHaveLength(before + 1);
    setStoredTokens({ ...TOKENS, access_token: "access-9" });
    MockEventSource.instances[before].fail();
    await vi.advanceTimersByTimeAsync(0);
    expect(MockEventSource.instances).toHaveLength(before + 2);
    expect(MockEventSource.instances[before + 1].url).toContain("access_token=access-9");

    MockEventSource.instances[before + 1].fail();
    await vi.advanceTimersByTimeAsync(60_000);
    expect(state.value).toBe("failed");
    expect(MockEventSource.instances).toHaveLength(before + 2);
  });
});
