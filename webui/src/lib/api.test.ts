import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { getAgents, getFileTree, getFileContent, searchFiles } from "./api";

describe("API Library", () => {
  let consoleErrorSpy: ReturnType<typeof vi.spyOn>;

  beforeEach(() => {
    consoleErrorSpy = vi.spyOn(console, "error").mockImplementation(() => {});
  });

  afterEach(() => {
    vi.restoreAllMocks();
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

    it("returns empty array on non-ok response or error", async () => {
      vi.spyOn(globalThis, "fetch").mockResolvedValue({
        ok: false,
        status: 404,
      } as Response);

      const res = await getFileTree("sess-123");
      expect(res).toEqual([]);
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

    it("returns null on 500 error", async () => {
      vi.spyOn(globalThis, "fetch").mockResolvedValue({
        ok: false,
        status: 500,
      } as Response);

      const res = await getFileContent("sess-123", "bad.txt");
      expect(res).toBeNull();
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
});
