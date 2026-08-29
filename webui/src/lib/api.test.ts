import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import {
  getAgents,
  reloadAgents,
  getConfigFile,
  saveConfigFile,
  restartServer,
  getFileTree,
  getFileContent,
  searchFiles,
  getSystemStatus,
} from "./api";

describe("API Library", () => {
  let consoleErrorSpy: ReturnType<typeof vi.spyOn>;

  beforeEach(() => {
    consoleErrorSpy = vi.spyOn(console, "error").mockImplementation(() => {});
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  describe("getSystemStatus", () => {
    it("returns null on 404 response without throwing", async () => {
      vi.spyOn(globalThis, "fetch").mockResolvedValue({
        ok: false,
        status: 404,
      } as Response);

      const res = await getSystemStatus();
      expect(res).toBeNull();
    });

    it("returns diagnostic status on 200 OK", async () => {
      const mockStatus = {
        status: "degraded",
        errors: ["Missing token"],
        warnings: [],
      };
      vi.spyOn(globalThis, "fetch").mockResolvedValue({
        ok: true,
        status: 200,
        json: async () => mockStatus,
      } as Response);

      const res = await getSystemStatus();
      expect(res).toEqual(mockStatus);
    });
  });

  it("should return empty array and log error on fetch error for getAgents", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockRejectedValue(new Error("Network error"));

    const agents = await getAgents();
    expect(agents).toEqual([]);
    expect(consoleErrorSpy).toHaveBeenCalledWith("getAgents error:", expect.any(Error));

    fetchMock.mockRestore();
  });

  describe("getFileTree", () => {
    it("returns empty array when sessionId is empty", async () => {
      const fetchSpy = vi.spyOn(globalThis, "fetch");
      const res = await getFileTree("");
      expect(res).toEqual([]);
      expect(fetchSpy).not.toHaveBeenCalled();
    });

    it("fetches root tree when no subPath is provided", async () => {
      const mockEntries = [
        { name: "src", path: "src", isDir: true },
        { name: "README.md", path: "README.md", isDir: false, size: 120, ext: "md" },
      ];
      vi.spyOn(globalThis, "fetch").mockResolvedValue({
        ok: true,
        status: 200,
        json: async () => ({ entries: mockEntries }),
      } as Response);

      const res = await getFileTree("sess-123");
      expect(res).toEqual(mockEntries);
      expect(globalThis.fetch).toHaveBeenCalledWith(
        "/api/files/tree?session_id=sess-123",
        undefined,
      );
    });

    it("encodes subPath parameter when provided", async () => {
      vi.spyOn(globalThis, "fetch").mockResolvedValue({
        ok: true,
        status: 200,
        json: async () => ({ entries: [] }),
      } as Response);

      await getFileTree("sess-123", "nested/dir path");
      expect(globalThis.fetch).toHaveBeenCalledWith(
        "/api/files/tree?session_id=sess-123&path=nested%2Fdir%20path",
        undefined,
      );
    });

    it("throws error with backend error message on non-ok response", async () => {
      vi.spyOn(globalThis, "fetch").mockResolvedValue({
        ok: false,
        status: 404,
        json: async () => ({ error: "Directory not found" }),
      } as Response);

      await expect(getFileTree("sess-123")).rejects.toThrow("Directory not found");
    });
  });

  describe("getFileContent", () => {
    it("returns null when sessionId or path is missing", async () => {
      const fetchSpy = vi.spyOn(globalThis, "fetch");
      expect(await getFileContent("", "main.go")).toBeNull();
      expect(await getFileContent("sess-123", "")).toBeNull();
      expect(fetchSpy).not.toHaveBeenCalled();
    });

    it("fetches file content successfully", async () => {
      const mockContent = {
        path: "src/main.go",
        name: "main.go",
        ext: "go",
        size: 250,
        content: "package main\n\nfunc main() {}\n",
        isBinary: false,
        updatedAt: "2026-08-19T00:00:00Z",
      };
      vi.spyOn(globalThis, "fetch").mockResolvedValue({
        ok: true,
        status: 200,
        json: async () => mockContent,
      } as Response);

      const res = await getFileContent("sess-123", "src/main.go");
      expect(res).toEqual(mockContent);
      expect(globalThis.fetch).toHaveBeenCalledWith(
        "/api/files/content?session_id=sess-123&path=src%2Fmain.go",
        undefined,
      );
    });

    it("throws error with backend error message on non-ok response", async () => {
      vi.spyOn(globalThis, "fetch").mockResolvedValue({
        ok: false,
        status: 400,
        json: async () => ({ error: "file size exceeds maximum allowed limit (5MB)" }),
      } as Response);

      await expect(getFileContent("sess-123", "bad.txt")).rejects.toThrow(
        "file size exceeds maximum allowed limit (5MB)",
      );
    });
  });

  describe("searchFiles", () => {
    it("returns empty array when sessionId is empty", async () => {
      const fetchSpy = vi.spyOn(globalThis, "fetch");
      const res = await searchFiles("", "test");
      expect(res).toEqual([]);
      expect(fetchSpy).not.toHaveBeenCalled();
    });

    it("searches files with query and limit", async () => {
      const mockFiles = [{ path: "src/search.ts", name: "search.ts", ext: "ts", size: 500 }];
      vi.spyOn(globalThis, "fetch").mockResolvedValue({
        ok: true,
        status: 200,
        json: async () => ({ files: mockFiles }),
      } as Response);

      const res = await searchFiles("sess-123", "search", 20);
      expect(res).toEqual(mockFiles);
      expect(globalThis.fetch).toHaveBeenCalledWith(
        "/api/files/search?session_id=sess-123&query=search&limit=20",
        { signal: undefined },
      );
    });

    it("ignores AbortError without logging console error", async () => {
      const abortErr = new Error("The operation was aborted");
      abortErr.name = "AbortError";
      vi.spyOn(globalThis, "fetch").mockRejectedValue(abortErr);

      const res = await searchFiles("sess-123", "aborted-query");
      expect(res).toEqual([]);
      expect(consoleErrorSpy).not.toHaveBeenCalled();
    });
  });

  describe("reloadAgents", () => {
    it("returns success: true when /api/manage/reload succeeds", async () => {
      vi.spyOn(globalThis, "fetch").mockResolvedValue({
        ok: true,
        status: 200,
        json: async () => ({ status: "success", message: "agents reloaded" }),
      } as Response);

      const res = await reloadAgents();
      expect(res).toEqual({ success: true });
      expect(globalThis.fetch).toHaveBeenCalledWith("/api/manage/reload", { method: "POST" });
    });

    it("returns extracted error message when /api/manage/reload fails with 500 JSON error", async () => {
      vi.spyOn(globalThis, "fetch").mockResolvedValue({
        ok: false,
        status: 500,
        json: async () => ({ error: "agent_father is required but missing" }),
      } as Response);

      const res = await reloadAgents();
      expect(res).toEqual({
        success: false,
        error: "agent_father is required but missing",
      });
    });

    it("handles network reject gracefully", async () => {
      vi.spyOn(globalThis, "fetch").mockRejectedValue(new Error("Connection refused"));

      const res = await reloadAgents();
      expect(res).toEqual({
        success: false,
        error: "Connection refused",
      });
    });
  });

  describe("getConfigFile", () => {
    it("fetches config file successfully", async () => {
      const mockResp = {
        path: "/etc/asgard/config.yaml",
        content: "port: 8080\n",
        exists: true,
      };
      vi.spyOn(globalThis, "fetch").mockResolvedValue({
        ok: true,
        status: 200,
        json: async () => mockResp,
      } as Response);

      const res = await getConfigFile();
      expect(res).toEqual(mockResp);
      expect(globalThis.fetch).toHaveBeenCalledWith("/api/manage/config", undefined);
    });

    it("returns null on network failure", async () => {
      vi.spyOn(globalThis, "fetch").mockRejectedValue(new Error("Failed to fetch"));

      const res = await getConfigFile();
      expect(res).toBeNull();
      expect(consoleErrorSpy).toHaveBeenCalledWith("getConfigFile error:", expect.any(Error));
    });
  });

  describe("saveConfigFile", () => {
    it("saves configuration successfully", async () => {
      vi.spyOn(globalThis, "fetch").mockResolvedValue({
        ok: true,
        status: 200,
        json: async () => ({ status: "success", message: "config saved" }),
      } as Response);

      const res = await saveConfigFile("port: 9000\n");
      expect(res).toEqual({ status: "success", message: "config saved" });
      expect(globalThis.fetch).toHaveBeenCalledWith("/api/manage/config", {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ content: "port: 9000\n" }),
      });
    });

    it("returns error from response when validation fails (400)", async () => {
      vi.spyOn(globalThis, "fetch").mockResolvedValue({
        ok: false,
        status: 400,
        json: async () => ({ error: "invalid configuration: yaml parse error" }),
      } as Response);

      const res = await saveConfigFile("invalid: yaml: :");
      expect(res).toEqual({ error: "invalid configuration: yaml parse error" });
    });

    it("handles network reject gracefully", async () => {
      vi.spyOn(globalThis, "fetch").mockRejectedValue(new Error("Network offline"));

      const res = await saveConfigFile("port: 8080");
      expect(res).toEqual({ error: "Network offline" });
    });
  });

  describe("restartServer", () => {
    it("returns true on 200 response", async () => {
      vi.spyOn(globalThis, "fetch").mockResolvedValue({
        ok: true,
        status: 200,
        json: async () => ({ status: "success", message: "server restart initiated" }),
      } as Response);

      const res = await restartServer();
      expect(res).toBe(true);
      expect(globalThis.fetch).toHaveBeenCalledWith("/api/manage/restart", { method: "POST" });
    });

    it("returns false on non-ok response (e.g. 403 / 500)", async () => {
      vi.spyOn(globalThis, "fetch").mockResolvedValue({
        ok: false,
        status: 403,
      } as Response);

      const res = await restartServer();
      expect(res).toBe(false);
    });

    it("returns true even if fetch throws network error (due to process exit / reset)", async () => {
      vi.spyOn(globalThis, "fetch").mockRejectedValue(new TypeError("Failed to fetch"));

      const res = await restartServer();
      expect(res).toBe(true);
    });
  });
});
