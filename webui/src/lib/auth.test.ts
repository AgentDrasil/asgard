// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import {
  apiFetch,
  appendAccessToken,
  getAttachmentUrl,
  getRawFileContentUrl,
  getRawWorkspaceFileUrl,
  getStoredTokens,
  initAuth,
  isAuthEnabled,
  logout,
  setStoredTokens,
} from "./api";
import type { StoredTokens } from "../types";

const TOKENS: StoredTokens = {
  access_token: "access-1",
  refresh_token: "refresh-1",
  id_token: "id-1",
  expires_in: 300,
  token_type: "Bearer",
};

let originalLocalStorage: Storage | undefined;
let originalLocation: Location | undefined;
let navigations: string[];

// installStorage swaps in an in-memory Storage so tests never touch the real one.
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

// installLocation replaces window.location with a recorder so assignments are
// observable instead of triggering a jsdom navigation error.
function installLocation(href = "http://localhost/dashboard?x=1") {
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
  Object.defineProperty(window, "location", {
    value: mock,
    configurable: true,
    writable: true,
  });
}

function jsonResponse(status: number, body?: unknown): Response {
  return {
    ok: status >= 200 && status < 300,
    status,
    json: async () => body,
  } as Response;
}

// enableAuth drives initAuth against a stubbed capability probe reporting that
// the backend runs OIDC auth.
async function enableAuth(fetchMock: ReturnType<typeof vi.spyOn>) {
  fetchMock.mockResolvedValueOnce(jsonResponse(200, { enabled: true }));
  await initAuth();
  expect(isAuthEnabled()).toBe(true);
}

// disableAuth establishes the default "auth is off" module state.
async function disableAuth(fetchMock: ReturnType<typeof vi.spyOn>) {
  fetchMock.mockResolvedValueOnce(jsonResponse(200, { enabled: false }));
  await initAuth();
  expect(isAuthEnabled()).toBe(false);
}

describe("auth capability discovery", () => {
  let fetchMock: ReturnType<typeof vi.spyOn>;

  beforeEach(() => {
    installStorage();
    installLocation();
    fetchMock = vi
      .spyOn(globalThis, "fetch")
      .mockResolvedValue(jsonResponse(200, { enabled: false }));
  });

  afterEach(() => {
    vi.restoreAllMocks();
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

  it("probes the public capability endpoint and reports enabled", async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(200, { enabled: true }));
    await expect(initAuth()).resolves.toBe(true);
    expect(fetchMock).toHaveBeenCalledWith("/api/auth/status", expect.anything());
    expect(isAuthEnabled()).toBe(true);
  });

  it("reports disabled when the backend has no auth", async () => {
    await expect(initAuth()).resolves.toBe(false);
    expect(isAuthEnabled()).toBe(false);
  });

  it("degrades to disabled when the probe fails", async () => {
    fetchMock.mockRejectedValueOnce(new Error("offline"));
    await expect(initAuth()).resolves.toBe(false);
    expect(isAuthEnabled()).toBe(false);
  });

  describe("apiFetch without auth", () => {
    beforeEach(async () => {
      await disableAuth(fetchMock);
      fetchMock.mockReset();
    });

    it("passes the request through untouched", async () => {
      fetchMock.mockResolvedValueOnce(jsonResponse(200, { agents: [] }));
      const res = await apiFetch("/api/agents");
      expect(res.status).toBe(200);
      expect(fetchMock).toHaveBeenCalledWith("/api/agents", undefined);
    });

    it("keeps the external-SSO reload on 401", async () => {
      fetchMock.mockResolvedValueOnce(jsonResponse(401));
      await apiFetch("/api/agents");
      expect(navigations).toHaveLength(1);
      expect(navigations[0]).toContain("_auth_refresh=");
    });
  });

  describe("apiFetch with auth", () => {
    beforeEach(async () => {
      await enableAuth(fetchMock);
      fetchMock.mockReset();
      setStoredTokens(TOKENS);
    });

    it("attaches the bearer token", async () => {
      fetchMock.mockResolvedValueOnce(jsonResponse(200, {}));
      await apiFetch("/api/agents");

      const init = fetchMock.mock.calls[0][1] as RequestInit;
      const headers = new Headers(init.headers);
      expect(headers.get("Authorization")).toBe("Bearer access-1");
    });

    it("refreshes once and retries on 401", async () => {
      const fresh: StoredTokens = {
        ...TOKENS,
        access_token: "access-2",
        refresh_token: "refresh-2",
      };
      fetchMock.mockImplementation(async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        if (url === "/auth/refresh") return jsonResponse(200, fresh);
        const auth = new Headers(init?.headers).get("Authorization");
        return auth === "Bearer access-2" ? jsonResponse(200, { ok: true }) : jsonResponse(401);
      });

      const res = await apiFetch("/api/agents");
      expect(res.status).toBe(200);
      expect(getStoredTokens()?.access_token).toBe("access-2");

      const refreshes = fetchMock.mock.calls.filter((call: unknown[]) => String(call[0]) === "/auth/refresh");
      expect(refreshes).toHaveLength(1);
      expect(navigations).toHaveLength(0);
    });

    it("redirects to the provider when the refresh token is gone", async () => {
      fetchMock.mockImplementation(async (input: RequestInfo | URL) => {
        const url = String(input);
        if (url === "/auth/refresh") return jsonResponse(401, { error: "refresh failed" });
        return jsonResponse(401);
      });

      // The promise never settles: the browser is navigating to the provider.
      void apiFetch("/api/agents");

      await vi.waitFor(() => expect(navigations).toHaveLength(1));
      expect(navigations[0]).toBe("/auth/login?redirect=%2Fdashboard%3Fx%3D1");
    });

    it("keeps the session when the provider is unavailable during refresh", async () => {
      fetchMock.mockImplementation(async (input: RequestInfo | URL) => {
        const url = String(input);
        if (url === "/auth/refresh") return jsonResponse(503, { error: "auth unavailable" });
        return jsonResponse(401);
      });

      const res = await apiFetch("/api/agents");
      expect(res.status).toBe(401);
      expect(navigations).toHaveLength(0);
      // The refresh token is kept so a later attempt can still succeed.
      expect(getStoredTokens()?.refresh_token).toBe("refresh-1");
    });

    it("redirects to the denial page on 403 without retrying", async () => {
      fetchMock.mockResolvedValue(jsonResponse(403));

      void apiFetch("/api/agents");

      await vi.waitFor(() => expect(navigations).toHaveLength(1));
      expect(navigations[0]).toBe("/auth/denied");
      const refreshes = fetchMock.mock.calls.filter((call: unknown[]) => String(call[0]) === "/auth/refresh");
      expect(refreshes).toHaveLength(0);
    });

    it("collapses concurrent refreshes into a single request", async () => {
      const fresh: StoredTokens = {
        ...TOKENS,
        access_token: "access-2",
        refresh_token: "refresh-2",
      };
      fetchMock.mockImplementation(async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        if (url === "/auth/refresh") return jsonResponse(200, fresh);
        const auth = new Headers(init?.headers).get("Authorization");
        return auth === "Bearer access-2" ? jsonResponse(200, { ok: true }) : jsonResponse(401);
      });

      const [a, b] = await Promise.all([apiFetch("/api/a"), apiFetch("/api/b")]);
      expect(a.status).toBe(200);
      expect(b.status).toBe(200);

      const refreshes = fetchMock.mock.calls.filter((call: unknown[]) => String(call[0]) === "/auth/refresh");
      expect(refreshes).toHaveLength(1);
    });

    it("appends the token to native-channel URLs", () => {
      expect(appendAccessToken("/api/x")).toBe("/api/x?access_token=access-1");
      expect(appendAccessToken("/api/x?a=1")).toBe("/api/x?a=1&access_token=access-1");
      expect(getRawFileContentUrl("s1", "a.md")).toContain("access_token=access-1");
      expect(getRawWorkspaceFileUrl("s1", "a.md")).toContain("access_token=access-1");
      expect(getAttachmentUrl("s1", "a.png")).toContain("access_token=access-1");
    });
  });

  describe("appendAccessToken without auth", () => {
    beforeEach(async () => {
      await disableAuth(fetchMock);
      fetchMock.mockReset();
      setStoredTokens(TOKENS);
    });

    it("leaves URLs untouched", () => {
      expect(appendAccessToken("/api/x")).toBe("/api/x");
      expect(getRawFileContentUrl("s1", "a.md")).not.toContain("access_token");
      expect(getAttachmentUrl("s1", "a.png")).not.toContain("access_token");
    });
  });

  describe("token storage", () => {
    it("round trips tokens and clears them on logout", () => {
      setStoredTokens(TOKENS);
      expect(getStoredTokens()).toEqual(TOKENS);

      logout();
      expect(getStoredTokens()).toBeNull();
      expect(navigations).toContain("/");
    });

    it("treats corrupt storage as no session", () => {
      localStorage.setItem("asgard_auth", "{not json");
      expect(getStoredTokens()).toBeNull();
    });

    it("appends nothing when no token is stored", async () => {
      fetchMock.mockResolvedValueOnce(jsonResponse(200, { enabled: true }));
      await initAuth();
      expect(appendAccessToken("/api/x")).toBe("/api/x");
    });
  });
});
