import { createRouter, createMemoryHistory } from "vue-router";
import { describe, it, expect } from "vitest";
import {
  parseFilePath,
  parseCommitId,
  encodePathSegments,
  buildChatRoute,
  buildFilesRoute,
  buildVcsRoute,
  shouldResetSessionState,
  resolveViewFromRoute,
} from "./routeUtils";

describe("routeUtils", () => {
  const dummyComponent = { template: "<div />" };
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: "/chat/:id", name: "chat", component: dummyComponent },
      { path: "/chat/:id/files/:filePath(.*)*", name: "chat-files", component: dummyComponent },
      {
        path: "/chat/:id/vcs/:commitId?/:filePath(.*)*",
        name: "chat-vcs",
        component: dummyComponent,
      },
    ],
  });

  describe("parseFilePath", () => {
    it("parses single and nested file paths correctly and filters empty segments", () => {
      expect(parseFilePath(undefined)).toBeNull();
      expect(parseFilePath("")).toBeNull();
      expect(parseFilePath("   ")).toBeNull();
      expect(parseFilePath("/")).toBeNull();
      expect(parseFilePath("///")).toBeNull();

      expect(parseFilePath("src/main.ts")).toBe("src/main.ts");
      expect(parseFilePath("/src/components/App.vue/")).toBe("src/components/App.vue");
      expect(parseFilePath(["src", "components", "App.vue"])).toBe("src/components/App.vue");
      expect(parseFilePath(["src", "", "  ", "nested", "file.ts"])).toBe("src/nested/file.ts");
      expect(parseFilePath([])).toBeNull();
      expect(parseFilePath(["", "  "])).toBeNull();
    });
  });

  describe("parseCommitId", () => {
    it("normalizes commit ids case-insensitively and handles unstash / UNSTASH / Unstash / undefined", () => {
      expect(parseCommitId(undefined)).toBeNull();
      expect(parseCommitId("")).toBeNull();
      expect(parseCommitId("   ")).toBeNull();
      expect(parseCommitId("unstash")).toBeNull();
      expect(parseCommitId("UNSTASH")).toBeNull();
      expect(parseCommitId("Unstash")).toBeNull();
      expect(parseCommitId("uNStAsH")).toBeNull();
      expect(parseCommitId(["unstash"])).toBeNull();
      expect(parseCommitId([])).toBeNull();

      expect(parseCommitId("abc1234")).toBe("abc1234");
      expect(parseCommitId("feat/branch")).toBe("feat/branch");
      expect(parseCommitId("rev%1")).toBe("rev%1");
      expect(parseCommitId(["commitHash123"])).toBe("commitHash123");
    });
  });

  describe("encodePathSegments", () => {
    it("encodes file path segments preserving slashes but encoding special characters (#, ?, %, spaces, unicode)", () => {
      expect(encodePathSegments("")).toBe("");
      expect(encodePathSegments("src/main.ts")).toBe("src/main.ts");
      expect(encodePathSegments("src/foo bar/baz#1?.txt")).toBe("src/foo%20bar/baz%231%3F.txt");
      expect(encodePathSegments("docs/100%_done/中文.md")).toBe(
        "docs/100%25_done/%E4%B8%AD%E6%96%87.md",
      );
    });
  });

  describe("roundtrip consistency", () => {
    it("guarantees build -> vue-router resolve -> parse roundtrip consistency for arbitrary file paths", () => {
      const testCases = [
        "simple.txt",
        "nested/path/to/file.ts",
        "path with spaces/file name.ext",
        "special#characters?in%path/file.vue",
        "docs/中文目录/文件.md",
        "symbols/@test/[id]/+page.svelte",
        "report 100%.vue",
        "a%20b.vue",
      ];

      for (const originalPath of testCases) {
        // Files route roundtrip
        const fileUrl = buildFilesRoute("session-123", originalPath);
        const resolvedFile = router.resolve(fileUrl);
        expect(parseFilePath(resolvedFile.params.filePath as string[])).toBe(originalPath);

        // VCS route with commitId roundtrip
        const vcsUrl = buildVcsRoute("session-123", "commit-456", originalPath);
        const resolvedVcs = router.resolve(vcsUrl);
        expect(parseCommitId(resolvedVcs.params.commitId as string)).toBe("commit-456");
        expect(parseFilePath(resolvedVcs.params.filePath as string[])).toBe(originalPath);

        // VCS route without commitId (unstash) roundtrip
        const vcsUnstashUrl = buildVcsRoute("session-123", null, originalPath);
        const resolvedVcsUnstash = router.resolve(vcsUnstashUrl);
        expect(parseCommitId(resolvedVcsUnstash.params.commitId as string)).toBeNull();
        expect(parseFilePath(resolvedVcsUnstash.params.filePath as string[])).toBe(originalPath);
      }
    });
  });

  describe("route builders", () => {
    it("builds correct route URLs for chat, files, and vcs", () => {
      expect(buildChatRoute("sess 123")).toBe("/chat/sess%20123");

      expect(buildFilesRoute("sess-1")).toBe("/chat/sess-1/files");
      expect(buildFilesRoute("sess-1", null)).toBe("/chat/sess-1/files");
      expect(buildFilesRoute("sess-1", "src/App.vue")).toBe("/chat/sess-1/files/src/App.vue");
      expect(buildFilesRoute("sess-1", "src/foo bar.ts")).toBe(
        "/chat/sess-1/files/src/foo%20bar.ts",
      );

      expect(buildVcsRoute("sess-1")).toBe("/chat/sess-1/vcs/unstash");
      expect(buildVcsRoute("sess-1", null)).toBe("/chat/sess-1/vcs/unstash");
      expect(buildVcsRoute("sess-1", "unstash")).toBe("/chat/sess-1/vcs/unstash");
      expect(buildVcsRoute("sess-1", "UNSTASH")).toBe("/chat/sess-1/vcs/unstash");
      expect(buildVcsRoute("sess-1", "Unstash")).toBe("/chat/sess-1/vcs/unstash");
      expect(buildVcsRoute("sess-1", "a1b2c3d")).toBe("/chat/sess-1/vcs/a1b2c3d");
      expect(buildVcsRoute("sess-1", "a1b2c3d", "src/App.vue")).toBe(
        "/chat/sess-1/vcs/a1b2c3d/src/App.vue",
      );
      expect(buildVcsRoute("sess-1", null, "src/App.vue")).toBe(
        "/chat/sess-1/vcs/unstash/src/App.vue",
      );
      expect(buildVcsRoute("sess-1", "UNSTASH", "src/App.vue")).toBe(
        "/chat/sess-1/vcs/unstash/src/App.vue",
      );
    });
  });

  describe("resolveViewFromRoute", () => {
    it("resolves activeView, filePath, and commitId from route location and parameters", () => {
      // By routeName
      expect(resolveViewFromRoute("/chat/123", { id: "123" }, "chat")).toEqual({
        activeView: "chat",
        filePath: null,
        commitId: null,
      });

      expect(
        resolveViewFromRoute(
          "/chat/123/files/src/App.vue",
          { id: "123", filePath: ["src", "App.vue"] },
          "chat-files",
        ),
      ).toEqual({
        activeView: "file",
        filePath: "src/App.vue",
        commitId: null,
      });

      expect(
        resolveViewFromRoute(
          "/chat/123/vcs/abc1234/src/App.vue",
          { id: "123", commitId: "abc1234", filePath: ["src", "App.vue"] },
          "chat-vcs",
        ),
      ).toEqual({
        activeView: "vcs",
        filePath: "src/App.vue",
        commitId: "abc1234",
      });

      expect(
        resolveViewFromRoute(
          "/chat/123/vcs/unstash/src/App.vue",
          { id: "123", commitId: "unstash", filePath: ["src", "App.vue"] },
          "chat-vcs",
        ),
      ).toEqual({
        activeView: "vcs",
        filePath: "src/App.vue",
        commitId: null,
      });

      // By regex fallback when routeName is null or not matched
      expect(
        resolveViewFromRoute(
          "/chat/123/files/src/main.ts",
          { id: "123", filePath: "src/main.ts" },
          null,
        ),
      ).toEqual({
        activeView: "file",
        filePath: "src/main.ts",
        commitId: null,
      });

      expect(
        resolveViewFromRoute(
          "/chat/123/vcs/commit1/src/main.ts",
          { id: "123", commitId: "commit1", filePath: "src/main.ts" },
          null,
        ),
      ).toEqual({
        activeView: "vcs",
        filePath: "src/main.ts",
        commitId: "commit1",
      });
    });

    it("resolves vcs view correctly when filePath contains a 'files' directory segment", () => {
      expect(
        resolveViewFromRoute(
          "/chat/123/vcs/unstash/files/main.go",
          { id: "123", commitId: "unstash", filePath: ["files", "main.go"] },
          null,
        ),
      ).toEqual({
        activeView: "vcs",
        filePath: "files/main.go",
        commitId: null,
      });

      expect(
        resolveViewFromRoute(
          "/chat/123/vcs/unstash/files/sub/files.ts",
          { id: "123", commitId: "unstash", filePath: ["files", "sub", "files.ts"] },
          "chat-vcs",
        ),
      ).toEqual({
        activeView: "vcs",
        filePath: "files/sub/files.ts",
        commitId: null,
      });
    });
  });

  describe("shouldResetSessionState", () => {
    it("identifies session transitions accurately via shouldResetSessionState", () => {
      expect(shouldResetSessionState(null, "session-1")).toBe(false);
      expect(shouldResetSessionState(undefined, "session-1")).toBe(false);
      expect(shouldResetSessionState("session-1", null)).toBe(false);
      expect(shouldResetSessionState("session-1", undefined)).toBe(false);
      expect(shouldResetSessionState(null, null)).toBe(false);
      expect(shouldResetSessionState(undefined, undefined)).toBe(false);
      expect(shouldResetSessionState(null, undefined)).toBe(false);
      expect(shouldResetSessionState("session-1", "session-1")).toBe(false);
      expect(shouldResetSessionState("session-1", "session-2")).toBe(true);
    });
  });

  describe("route synchronization and state derivation", () => {
    it("restores activeView='chat' from /chat/:id", () => {
      const resolved = router.resolve("/chat/sess-123");
      const state = resolveViewFromRoute(
        resolved.path,
        resolved.params,
        resolved.name ? String(resolved.name) : null,
      );
      expect(state.activeView).toBe("chat");
      expect(state.filePath).toBeNull();
      expect(state.commitId).toBeNull();
    });

    it("restores activeView='file' and selectedFilePath from /chat/:id/files/:filePath", () => {
      const resolved = router.resolve("/chat/sess-123/files/src/main.ts");
      const state = resolveViewFromRoute(
        resolved.path,
        resolved.params,
        resolved.name ? String(resolved.name) : null,
      );
      expect(state.activeView).toBe("file");
      expect(state.filePath).toBe("src/main.ts");
      expect(state.commitId).toBeNull();
    });

    it("restores activeView='vcs', selectedCommit, and filePath from /chat/:id/vcs/:commitId/:filePath", () => {
      const resolved = router.resolve("/chat/sess-123/vcs/a1b2c3d/src/components/App.vue");
      const state = resolveViewFromRoute(
        resolved.path,
        resolved.params,
        resolved.name ? String(resolved.name) : null,
      );
      expect(state.activeView).toBe("vcs");
      expect(state.commitId).toBe("a1b2c3d");
      expect(state.filePath).toBe("src/components/App.vue");
    });

    it("accurately detects when session ID changes vs view changes via shouldResetSessionState", () => {
      expect(shouldResetSessionState("session-A", "session-B")).toBe(true);
      expect(shouldResetSessionState("session-A", "session-A")).toBe(false);
      expect(shouldResetSessionState(null, "session-A")).toBe(false);
      expect(shouldResetSessionState("session-A", null)).toBe(false);
    });

    it("normalizes naked /chat/:id/vcs with undefined commitId to commitId=null and activeView='vcs'", () => {
      const resolved = router.resolve("/chat/sess-123/vcs");
      const state = resolveViewFromRoute(
        resolved.path,
        resolved.params,
        resolved.name ? String(resolved.name) : null,
      );
      expect(state.activeView).toBe("vcs");
      expect(state.commitId).toBeNull();
      expect(state.filePath).toBeNull();
    });
  });
});
