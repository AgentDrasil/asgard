import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import {
  getAgents,
  reloadAgents,
  getConfigFile,
  saveConfigFile,
  getKeybindings,
  saveKeybindings,
  restartServer,
  getFileTree,
  getFileContent,
  searchFiles,
  getSystemStatus,
  getSystemLogs,
  getRawFileContentUrl,
  getRawWorkspaceFileUrl,
  uploadAttachment,
  getAttachmentUrl,
  triggerAgentMessage,
  getSessions,
  archiveSession,
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

  describe("getSystemLogs", () => {
    it("returns logs on 200 OK without filter", async () => {
      const mockLogs = [
        {
          id: 1,
          timestamp: "2026-08-30T10:00:00Z",
          level: "warn" as const,
          source: "config",
          message: "config warning",
        },
      ];
      vi.spyOn(globalThis, "fetch").mockResolvedValue({
        ok: true,
        status: 200,
        json: async () => ({ logs: mockLogs }),
      } as Response);

      const res = await getSystemLogs();
      expect(res).toEqual(mockLogs);
      expect(globalThis.fetch).toHaveBeenCalledWith("/api/system/logs", undefined);
    });

    it("includes level filter query param when provided", async () => {
      vi.spyOn(globalThis, "fetch").mockResolvedValue({
        ok: true,
        status: 200,
        json: async () => ({ logs: [] }),
      } as Response);

      await getSystemLogs("error");
      expect(globalThis.fetch).toHaveBeenCalledWith("/api/system/logs?level=error", undefined);
    });

    it("returns empty array on non-ok response without throwing", async () => {
      vi.spyOn(globalThis, "fetch").mockResolvedValue({
        ok: false,
        status: 500,
      } as Response);

      const res = await getSystemLogs();
      expect(res).toEqual([]);
    });

    it("handles fetch rejection and logs error", async () => {
      vi.spyOn(globalThis, "fetch").mockRejectedValue(new Error("Network disconnect"));

      const res = await getSystemLogs();
      expect(res).toEqual([]);
      expect(consoleErrorSpy).toHaveBeenCalledWith("getSystemLogs error:", expect.any(Error));
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

    it("fetches file content successfully with scope parameter", async () => {
      const mockContent = {
        path: "/tmp/output.log",
        name: "output.log",
        ext: "log",
        size: 120,
        content: "log data",
        isBinary: false,
        updatedAt: "2026-08-19T00:00:00Z",
      };
      vi.spyOn(globalThis, "fetch").mockResolvedValue({
        ok: true,
        status: 200,
        json: async () => mockContent,
      } as Response);

      const res = await getFileContent("sess-123", "/tmp/output.log", "tmp");
      expect(res).toEqual(mockContent);
      expect(globalThis.fetch).toHaveBeenCalledWith(
        "/api/files/content?session_id=sess-123&path=%2Ftmp%2Foutput.log&scope=tmp",
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

  describe("getKeybindings", () => {
    it("fetches keybindings successfully on 200 OK", async () => {
      const mockResp = {
        overrides: {
          linux: { toggle_sidebar: "Ctrl+Alt+B" },
        },
        exists: true,
      };
      vi.spyOn(globalThis, "fetch").mockResolvedValue({
        ok: true,
        status: 200,
        json: async () => mockResp,
      } as Response);

      const res = await getKeybindings();
      expect(res).toEqual(mockResp);
      expect(globalThis.fetch).toHaveBeenCalledWith("/api/keybindings", undefined);
    });

    it("returns error on 500 corrupted file response", async () => {
      vi.spyOn(globalThis, "fetch").mockResolvedValue({
        ok: false,
        status: 500,
        json: async () => ({ error: "yaml parse error" }),
      } as Response);

      const res = await getKeybindings();
      expect(res).toEqual({
        overrides: {},
        error: "yaml parse error",
      });
    });

    it("returns null on network failure and logs error", async () => {
      vi.spyOn(globalThis, "fetch").mockRejectedValue(new Error("Network error"));

      const res = await getKeybindings();
      expect(res).toBeNull();
      expect(consoleErrorSpy).toHaveBeenCalledWith("getKeybindings error:", expect.any(Error));
    });
  });

  describe("saveKeybindings", () => {
    it("saves keybindings successfully on 200 OK", async () => {
      vi.spyOn(globalThis, "fetch").mockResolvedValue({
        ok: true,
        status: 200,
        json: async () => ({ success: true }),
      } as Response);

      const overrides = { linux: { toggle_sidebar: "Ctrl+Alt+B" } };
      const res = await saveKeybindings(overrides);
      expect(res).toEqual({ success: true });
      expect(globalThis.fetch).toHaveBeenCalledWith("/api/manage/keybindings", {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ overrides }),
      });
    });

    it("returns error on non-ok response", async () => {
      vi.spyOn(globalThis, "fetch").mockResolvedValue({
        ok: false,
        status: 400,
        json: async () => ({ error: "invalid key sequence" }),
      } as Response);

      const res = await saveKeybindings({ linux: { toggle_sidebar: "invalid" } });
      expect(res).toEqual({ success: false, error: "invalid key sequence" });
    });

    it("handles network reject gracefully", async () => {
      vi.spyOn(globalThis, "fetch").mockRejectedValue(new Error("Network disconnect"));

      const res = await saveKeybindings({});
      expect(res).toEqual({ success: false, error: "Network disconnect" });
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

  describe("raw media URL helpers", () => {
    it("getRawFileContentUrl constructs encoded URL with raw=1", () => {
      const url = getRawFileContentUrl("session-123", "/src/assets/logo with spaces.png");
      expect(url).toBe(
        "/api/files/content?session_id=session-123&path=%2Fsrc%2Fassets%2Flogo%20with%20spaces.png&raw=1",
      );
    });

    it("getRawWorkspaceFileUrl constructs encoded URL with raw=1", () => {
      const url = getRawWorkspaceFileUrl("session-123", "/workspace/dir/test & special#name.pdf");
      expect(url).toBe(
        "/api/v1/workspace/file?session_id=session-123&path=%2Fworkspace%2Fdir%2Ftest%20%26%20special%23name.pdf&raw=1",
      );
    });
  });

  describe("attachments API", () => {
    it("uploadAttachment uploads file and returns the first Attachment object", async () => {
      const mockAttachment = {
        name: "test.png",
        path: ".attachments/test.png",
        size: 1024,
        mimeType: "image/png",
      };
      vi.spyOn(globalThis, "fetch").mockResolvedValue({
        ok: true,
        status: 200,
        json: async () => [mockAttachment],
      } as Response);

      const dummyFile = new File(["dummy content"], "test.png", { type: "image/png" });
      const res = await uploadAttachment("sess-123", dummyFile);

      expect(res).toEqual(mockAttachment);
      expect(globalThis.fetch).toHaveBeenCalledWith(
        "/api/sessions/sess-123/attachments",
        expect.objectContaining({
          method: "POST",
          body: expect.any(FormData),
        }),
      );
    });

    it("uploadAttachment throws error on non-ok status with backend error message", async () => {
      vi.spyOn(globalThis, "fetch").mockResolvedValue({
        ok: false,
        status: 413,
        json: async () => ({ error: "file size exceeds 20MB limit" }),
      } as Response);

      const dummyFile = new File(["huge content"], "huge.zip", { type: "application/zip" });
      await expect(uploadAttachment("sess-123", dummyFile)).rejects.toThrow(
        "file size exceeds 20MB limit",
      );
    });

    it("uploadAttachment throws error if response array is empty", async () => {
      vi.spyOn(globalThis, "fetch").mockResolvedValue({
        ok: true,
        status: 200,
        json: async () => [],
      } as Response);

      const dummyFile = new File(["data"], "empty.txt", { type: "text/plain" });
      await expect(uploadAttachment("sess-123", dummyFile)).rejects.toThrow(
        "Invalid attachment upload response",
      );
    });

    it("getAttachmentUrl constructs properly encoded URL", () => {
      const url = getAttachmentUrl("sess-123", "sub dir/test image.png");
      expect(url).toBe("/api/sessions/sess-123/attachments/sub%20dir%2Ftest%20image.png");
    });
  });

  describe("triggerAgentMessage", () => {
    it("sends prompt and attachments in request body", async () => {
      const mockResult = { status: "accepted", chatId: "sess-123" };
      vi.spyOn(globalThis, "fetch").mockResolvedValue({
        ok: true,
        status: 200,
        json: async () => mockResult,
      } as Response);

      const attachments = [
        {
          name: "data.csv",
          path: ".attachments/data.csv",
          size: 2048,
          mimeType: "text/csv",
        },
      ];

      const res = await triggerAgentMessage("coder", {
        prompt: "Analyze this dataset",
        chatId: "sess-123",
        runDir: "/workspace",
        model: "gemini-2.5",
        metadata: { message_id: "user-123" },
        attachments,
      });

      expect(res).toEqual(mockResult);
      expect(globalThis.fetch).toHaveBeenCalledWith("/api/agents/coder/message", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          prompt: "Analyze this dataset",
          chatId: "sess-123",
          runDir: "/workspace",
          model: "gemini-2.5",
          metadata: { message_id: "user-123" },
          attachments,
        }),
      });
    });
  });

  describe("getSessions", () => {
    it("fetches active sessions when archived is false or omitted", async () => {
      const mockSessions = [
        { chatID: "sess-1", title: "Active 1", currentAgent: "coder", runDir: "/tmp" },
      ];
      vi.spyOn(globalThis, "fetch").mockResolvedValue({
        ok: true,
        status: 200,
        json: async () => mockSessions,
      } as Response);

      const res = await getSessions();
      expect(res).toEqual(mockSessions);
      expect(globalThis.fetch).toHaveBeenCalledWith("/api/sessions", undefined);
    });

    it("fetches archived sessions when archived is true", async () => {
      const mockArchived = [
        {
          chatID: "sess-2",
          title: "Archived 1",
          currentAgent: "coder",
          runDir: "/tmp",
          isArchived: true,
        },
      ];
      vi.spyOn(globalThis, "fetch").mockResolvedValue({
        ok: true,
        status: 200,
        json: async () => mockArchived,
      } as Response);

      const res = await getSessions(true);
      expect(res).toEqual(mockArchived);
      expect(globalThis.fetch).toHaveBeenCalledWith("/api/sessions?archived=true", undefined);
    });

    it("returns empty array on network failure", async () => {
      vi.spyOn(globalThis, "fetch").mockRejectedValue(new Error("Network failed"));

      const res = await getSessions();
      expect(res).toEqual([]);
      expect(consoleErrorSpy).toHaveBeenCalledWith(
        "Failed to fetch sessions from backend:",
        expect.any(Error),
      );
    });
  });

  describe("archiveSession", () => {
    it("returns false if chatID is empty", async () => {
      const fetchSpy = vi.spyOn(globalThis, "fetch");
      const res = await archiveSession("");
      expect(res).toBe(false);
      expect(fetchSpy).not.toHaveBeenCalled();
    });

    it("returns true on successful 200 OK archive response", async () => {
      vi.spyOn(globalThis, "fetch").mockResolvedValue({
        ok: true,
        status: 200,
      } as Response);

      const res = await archiveSession("sess-123");
      expect(res).toBe(true);
      expect(globalThis.fetch).toHaveBeenCalledWith("/api/sessions/sess-123/archive", {
        method: "POST",
      });
    });

    it("returns false on non-ok HTTP status", async () => {
      vi.spyOn(globalThis, "fetch").mockResolvedValue({
        ok: false,
        status: 404,
      } as Response);

      const res = await archiveSession("sess-nonexistent");
      expect(res).toBe(false);
    });

    it("returns false on network error and logs error", async () => {
      vi.spyOn(globalThis, "fetch").mockRejectedValue(new Error("Connection reset"));

      const res = await archiveSession("sess-123");
      expect(res).toBe(false);
      expect(consoleErrorSpy).toHaveBeenCalledWith("Failed to archive session:", expect.any(Error));
    });
  });
});
