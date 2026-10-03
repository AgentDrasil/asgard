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

describe("useSessionEvents auth handling", () => {
  beforeEach(() => {
    MockEventSource.instances = [];
    (globalThis as any).EventSource = MockEventSource;
    installStorage();
    installLocation();
    fetchMock = vi.spyOn(globalThis, "fetch").mockResolvedValue(jsonResponse(200, {}));
  });

  afterEach(() => {
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

    const { connect } = useSessionEvents({});
    connect("chat-1");

    expect(MockEventSource.instances).toHaveLength(1);
    expect(MockEventSource.instances[0].url).toBe("/api/sessions/chat-1/events");
  });

  it("carries the token in the query string while auth is enabled", async () => {
    await setAuthEnabled(true);
    setStoredTokens(TOKENS);

    const { connect } = useSessionEvents({});
    connect("chat-1");

    expect(MockEventSource.instances).toHaveLength(1);
    expect(MockEventSource.instances[0].url).toBe(
      "/api/sessions/chat-1/events?access_token=access-1",
    );
  });

  it("leaves a closed stream alone while auth is disabled", async () => {
    await setAuthEnabled(false);

    const { connect } = useSessionEvents({});
    connect("chat-1");
    MockEventSource.instances[0].fail();

    // External-SSO deployments recover through the next apiFetch 401 instead.
    await vi.waitFor(() => expect(MockEventSource.instances[0].closed).toBe(true));
    expect(MockEventSource.instances).toHaveLength(1);
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

    const { connect } = useSessionEvents({});
    connect("chat-1");
    MockEventSource.instances[0].fail();

    await vi.waitFor(() => expect(MockEventSource.instances).toHaveLength(2));
    expect(MockEventSource.instances[1].url).toBe(
      "/api/sessions/chat-1/events?access_token=access-2",
    );
  });

  it("sends the user to the provider when the session cannot be refreshed", async () => {
    await setAuthEnabled(true);
    setStoredTokens(TOKENS);

    fetchMock.mockImplementation(async (input: RequestInfo | URL) => {
      if (String(input) === "/auth/refresh") return jsonResponse(401, { error: "refresh failed" });
      return jsonResponse(404);
    });

    const { connect } = useSessionEvents({});
    connect("chat-1");
    MockEventSource.instances[0].fail();

    await vi.waitFor(() => expect(navigations).toHaveLength(1));
    expect(navigations[0]).toBe("/auth/login?redirect=%2Fdashboard");
    expect(MockEventSource.instances).toHaveLength(1);
  });

  it("retries after a backoff when the provider is unreachable", async () => {
    vi.useFakeTimers();
    await setAuthEnabled(true);
    setStoredTokens(TOKENS);

    fetchMock.mockImplementation(async (input: RequestInfo | URL) => {
      if (String(input) === "/auth/refresh")
        return jsonResponse(503, { error: "auth unavailable" });
      return jsonResponse(404);
    });

    const { connect } = useSessionEvents({});
    connect("chat-1");
    MockEventSource.instances[0].fail();

    await vi.advanceTimersByTimeAsync(0);
    expect(MockEventSource.instances).toHaveLength(1);
    expect(navigations).toHaveLength(0);

    await vi.advanceTimersByTimeAsync(5000);
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

    const { connect } = useSessionEvents({});
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
});
