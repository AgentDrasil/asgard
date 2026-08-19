import { describe, it, expect } from "vitest";
import { commentKey, formatCommentBlock, rebuildChatInputFromComments } from "./commentUtils";
import type { CommentEntry } from "../types";

describe("commentUtils", () => {
  describe("commentKey", () => {
    it("generates key without side", () => {
      expect(commentKey("src/main.ts", 42)).toBe("src/main.ts:42");
    });

    it("generates key with side", () => {
      expect(commentKey("src/main.ts", 42, "new")).toBe("src/main.ts:new:42");
      expect(commentKey("src/main.ts", 10, "old")).toBe("src/main.ts:old:10");
    });
  });

  describe("formatCommentBlock", () => {
    it("formats CommentEntry into comment block", () => {
      const entry: CommentEntry = {
        filePath: "pkg/service/server.go",
        lineNumber: 105,
        lineContent: "func StartServer() error {",
        comment: "Please check error handling here.",
      };

      const expected =
        "pkg/service/server.go line 105\nfunc StartServer() error {\n---\n\nuser comment:\n\nPlease check error handling here.\n---";

      expect(formatCommentBlock(entry)).toBe(expected);
    });
  });

  describe("rebuildChatInputFromComments", () => {
    it("returns empty string when comments map or array is empty", () => {
      expect(rebuildChatInputFromComments([])).toBe("");
      expect(rebuildChatInputFromComments(new Map())).toBe("");
    });

    it("joins multiple comments from array with double newline", () => {
      const entry1: CommentEntry = {
        filePath: "a.ts",
        lineNumber: 1,
        lineContent: "const a = 1;",
        comment: "First comment",
      };
      const entry2: CommentEntry = {
        filePath: "b.ts",
        lineNumber: 5,
        lineContent: "const b = 2;",
        comment: "Second comment",
      };

      const result = rebuildChatInputFromComments([entry1, entry2]);
      const expected = `${formatCommentBlock(entry1)}\n\n${formatCommentBlock(entry2)}`;
      expect(result).toBe(expected);
    });

    it("joins multiple comments from Map", () => {
      const entry1: CommentEntry = {
        filePath: "a.ts",
        lineNumber: 1,
        lineContent: "const a = 1;",
        comment: "First comment",
      };
      const map = new Map<string, CommentEntry>();
      map.set("key1", entry1);

      expect(rebuildChatInputFromComments(map)).toBe(formatCommentBlock(entry1));
    });
  });
});
