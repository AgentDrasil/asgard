import { describe, it, expect } from "vitest";
import {
  mapExtToLang,
  getFileIcon,
  escapeHtml,
  extractHighlightedLines,
  isAncestorDir,
  isImageFile,
  isVideoFile,
  isAudioFile,
  isPdfFile,
  isCsvFile,
  getMediaCategory,
  resolveViewerCategory,
  isSessionTmpDir,
  isTmpScopePath,
  isSessionScopePath,
} from "./fileUtils";

describe("fileUtils", () => {
  describe("mapExtToLang", () => {
    it("maps common file extensions to Shiki language identifiers", () => {
      expect(mapExtToLang("go")).toBe("go");
      expect(mapExtToLang("ts")).toBe("typescript");
      expect(mapExtToLang("tsx")).toBe("typescript");
      expect(mapExtToLang("js")).toBe("javascript");
      expect(mapExtToLang("jsx")).toBe("javascript");
      expect(mapExtToLang("vue")).toBe("vue");
      expect(mapExtToLang("json")).toBe("json");
      expect(mapExtToLang("html")).toBe("html");
      expect(mapExtToLang("css")).toBe("css");
      expect(mapExtToLang("yaml")).toBe("yaml");
      expect(mapExtToLang("yml")).toBe("yaml");
      expect(mapExtToLang("md")).toBe("markdown");
      expect(mapExtToLang("csv")).toBe("csv");
      expect(mapExtToLang("tsv")).toBe("csv");
      expect(mapExtToLang("py")).toBe("python");
      expect(mapExtToLang("sh")).toBe("bash");
      expect(mapExtToLang("sql")).toBe("sql");
      expect(mapExtToLang("c")).toBe("c");
      expect(mapExtToLang("cpp")).toBe("cpp");
      expect(mapExtToLang("dockerfile")).toBe("dockerfile");
      expect(mapExtToLang("diff")).toBe("diff");
    });

    it("returns 'text' for unknown extensions or undefined", () => {
      expect(mapExtToLang("unknown")).toBe("text");
      expect(mapExtToLang("")).toBe("text");
      expect(mapExtToLang(undefined)).toBe("text");
    });
  });

  describe("getFileIcon", () => {
    it("resolves specific icons for known extensions and fallback icon for unknown", () => {
      expect(getFileIcon("go")).toBe("vscode-icons:file-type-go");
      expect(getFileIcon("ts")).toBe("vscode-icons:file-type-typescript");
      expect(getFileIcon("md")).toBe("octicon:markdown-24");
      expect(getFileIcon("csv")).toBe("vscode-icons:file-type-excel");
      expect(getFileIcon("tsv")).toBe("vscode-icons:file-type-excel");
      expect(getFileIcon("png")).toBe("vscode-icons:file-type-image");
      expect(getFileIcon("mp4")).toBe("vscode-icons:file-type-video");
      expect(getFileIcon("mp3")).toBe("vscode-icons:file-type-audio");
      expect(getFileIcon("pdf")).toBe("vscode-icons:file-type-pdf");
      expect(getFileIcon(undefined, "src/components/App.vue")).toBe("vscode-icons:file-type-vue");
      expect(getFileIcon(undefined, "some/path/photo.jpg")).toBe("vscode-icons:file-type-image");
      expect(getFileIcon(undefined, "some/path/movie.webm")).toBe("vscode-icons:file-type-video");
      expect(getFileIcon(undefined, "some/path/audio.wav")).toBe("vscode-icons:file-type-audio");
      expect(getFileIcon(undefined, "some/path/doc.pdf")).toBe("vscode-icons:file-type-pdf");
      expect(getFileIcon(undefined, "some/path/data.csv")).toBe("vscode-icons:file-type-excel");
      expect(getFileIcon(undefined, "some/path/file.xyz")).toBe("octicon:file-code-24");
    });
  });

  describe("media type checkers", () => {
    it("detects image files correctly", () => {
      expect(isImageFile("png")).toBe(true);
      expect(isImageFile(".jpg")).toBe(true);
      expect(isImageFile("jpeg")).toBe(true);
      expect(isImageFile("svg")).toBe(true);
      expect(isImageFile("webp")).toBe(true);
      expect(isImageFile("gif")).toBe(true);
      expect(isImageFile(undefined, "path/to/icon.ico")).toBe(true);
      expect(isImageFile("go")).toBe(false);
      expect(isImageFile("ts")).toBe(false);
      expect(isImageFile(undefined, "app.ts")).toBe(false);
    });

    it("detects video files correctly", () => {
      expect(isVideoFile("mp4")).toBe(true);
      expect(isVideoFile(".webm")).toBe(true);
      expect(isVideoFile("mov")).toBe(true);
      expect(isVideoFile(undefined, "test.ogv")).toBe(true);
      expect(isVideoFile("mp3")).toBe(false);
      expect(isVideoFile("png")).toBe(false);
    });

    it("detects audio files correctly", () => {
      expect(isAudioFile("ogg")).toBe(true);
      expect(isAudioFile("mp3")).toBe(true);
      expect(isAudioFile(".wav")).toBe(true);
      expect(isAudioFile("flac")).toBe(true);
      expect(isAudioFile("aac")).toBe(true);
      expect(isAudioFile(undefined, "sound.m4a")).toBe(true);
      expect(isAudioFile("mp4")).toBe(false);
      expect(isAudioFile("pdf")).toBe(false);
    });

    it("detects pdf files correctly", () => {
      expect(isPdfFile("pdf")).toBe(true);
      expect(isPdfFile(".pdf")).toBe(true);
      expect(isPdfFile(undefined, "doc.pdf")).toBe(true);
      expect(isPdfFile("png")).toBe(false);
      expect(isPdfFile("txt")).toBe(false);
    });

    it("detects csv files correctly", () => {
      expect(isCsvFile("csv")).toBe(true);
      expect(isCsvFile(".tsv")).toBe(true);
      expect(isCsvFile(undefined, "data.csv")).toBe(true);
      expect(isCsvFile("txt")).toBe(false);
    });

    it("detects getMediaCategory properly", () => {
      expect(getMediaCategory("png")).toBe("image");
      expect(getMediaCategory("mp4")).toBe("video");
      expect(getMediaCategory("mp3")).toBe("audio");
      expect(getMediaCategory("ogg")).toBe("audio");
      expect(getMediaCategory("pdf")).toBe("pdf");
      expect(getMediaCategory("csv")).toBe("csv");
      expect(getMediaCategory("tsv")).toBe("csv");
      expect(getMediaCategory("md")).toBe("markdown");
      expect(getMediaCategory("markdown")).toBe("markdown");
      expect(getMediaCategory("go")).toBe("code");
      expect(getMediaCategory("ts")).toBe("code");
      expect(getMediaCategory("txt")).toBe("code");
      expect(getMediaCategory("java")).toBe("code");
      expect(getMediaCategory(undefined, "lib.rs")).toBe("code");
      expect(getMediaCategory(undefined, "dir.d/README")).toBe("code");
      expect(getMediaCategory(undefined, "dir.d/file")).toBe("code");
      expect(getMediaCategory(undefined, "config.toml")).toBe("code");
      expect(getMediaCategory(undefined, "pom.xml")).toBe("code");
      expect(getMediaCategory("")).toBe("code");
      expect(getMediaCategory("wasm")).toBe("binary");
      expect(getMediaCategory("bin")).toBe("binary");
      expect(getMediaCategory("zip")).toBe("binary");
      expect(getMediaCategory("tar")).toBe("binary");
    });
  });

  describe("resolveViewerCategory", () => {
    it("returns code for null or undefined", () => {
      expect(resolveViewerCategory(null)).toBe("code");
      expect(resolveViewerCategory(undefined)).toBe("code");
    });

    it("returns media category from extension", () => {
      expect(resolveViewerCategory({ ext: "png", path: "/test.png" })).toBe("image");
      expect(resolveViewerCategory({ ext: "mp4", path: "/test.mp4" })).toBe("video");
      expect(resolveViewerCategory({ ext: "mp3", path: "/test.mp3" })).toBe("audio");
      expect(resolveViewerCategory({ ext: "pdf", path: "/test.pdf" })).toBe("pdf");
      expect(resolveViewerCategory({ ext: "csv", path: "/test.csv" })).toBe("csv");
      expect(resolveViewerCategory({ ext: "md", path: "/test.md" })).toBe("markdown");
      expect(resolveViewerCategory({ ext: "go", path: "/main.go" })).toBe("code");
    });

    it("returns binary when isBinary is true even for unknown/code extensions", () => {
      expect(resolveViewerCategory({ ext: "dat", path: "/data.dat", isBinary: true })).toBe(
        "binary",
      );
      expect(resolveViewerCategory({ ext: "unknown", path: "/file", isBinary: true })).toBe(
        "binary",
      );
      expect(resolveViewerCategory({ ext: "go", path: "/main.go", isBinary: false })).toBe("code");
    });
  });

  describe("escapeHtml", () => {
    it("escapes special HTML characters properly", () => {
      expect(escapeHtml(`<div>"Hello" & 'World'</div>`)).toBe(
        `&lt;div&gt;&quot;Hello&quot; &amp; &#039;World&#039;&lt;/div&gt;`,
      );
    });
  });

  describe("extractHighlightedLines", () => {
    it("extracts multi-token line spans without truncating nested tokens", () => {
      const shikiHtml = `<pre class="shiki github-dark" style="background-color:#24292e;color:#e1e4e8"><code><span class="line"><span style="color:#89DDFF">const</span><span style="color:#A6ACCD"> x </span><span style="color:#89DDFF">=</span><span style="color:#F78C6C"> 1</span></span>
<span class="line"><span style="color:#A6ACCD">console</span><span style="color:#89DDFF">.</span><span style="color:#82AAFF">log</span><span style="color:#89DDFF">(</span><span style="color:#F78C6C">x</span><span style="color:#89DDFF">)</span></span></code></pre>`;

      const lines = extractHighlightedLines(shikiHtml, 2);
      expect(lines).toHaveLength(2);
      expect(lines[0]).toBe(
        `<span style="color:#89DDFF">const</span><span style="color:#A6ACCD"> x </span><span style="color:#89DDFF">=</span><span style="color:#F78C6C"> 1</span>`,
      );
      expect(lines[1]).toBe(
        `<span style="color:#A6ACCD">console</span><span style="color:#89DDFF">.</span><span style="color:#82AAFF">log</span><span style="color:#89DDFF">(</span><span style="color:#F78C6C">x</span><span style="color:#89DDFF">)</span>`,
      );
    });

    it("handles empty lines within Shiki output", () => {
      const shikiHtml = `<pre class="shiki github-dark"><code><span class="line"><span style="color:#89DDFF">let</span><span style="color:#A6ACCD"> a</span></span>
<span class="line"></span>
<span class="line"><span style="color:#A6ACCD">a = 2</span></span></code></pre>`;

      const lines = extractHighlightedLines(shikiHtml, 3);
      expect(lines).toHaveLength(3);
      expect(lines[0]).toBe(
        `<span style="color:#89DDFF">let</span><span style="color:#A6ACCD"> a</span>`,
      );
      expect(lines[1]).toBe("");
      expect(lines[2]).toBe(`<span style="color:#A6ACCD">a = 2</span>`);
    });

    it("returns empty array if expected line count does not match", () => {
      const shikiHtml = `<pre class="shiki"><code><span class="line"><span>line 1</span></span></code></pre>`;
      expect(extractHighlightedLines(shikiHtml, 5)).toEqual([]);
    });

    it("returns empty array for empty string", () => {
      expect(extractHighlightedLines("")).toEqual([]);
    });
  });

  describe("isAncestorDir", () => {
    it("identifies direct and nested parent directories accurately", () => {
      expect(isAncestorDir("src", "src/main.ts")).toBe(true);
      expect(isAncestorDir("src", "src/components/file/FileView.vue")).toBe(true);
      expect(isAncestorDir("src/components", "src/components/file/FileView.vue")).toBe(true);
      expect(isAncestorDir("src/components/", "src/components/file/FileView.vue")).toBe(true);
      expect(isAncestorDir("/home/user/src", "/home/user/src/components/File.vue")).toBe(true);
    });

    it("distinguishes same-prefix sibling directories correctly (e.g. /src/app vs /src/app-2)", () => {
      expect(isAncestorDir("/src/app", "/src/app-2/main.ts")).toBe(false);
      expect(isAncestorDir("src/app", "src/app-2/main.ts")).toBe(false);
      expect(isAncestorDir("src/app", "src/app/main.ts")).toBe(true);
      expect(isAncestorDir("components", "components-extra/test.ts")).toBe(false);
    });

    it("handles empty paths, trailing slashes, and identical paths safely", () => {
      expect(isAncestorDir("", "src/main.ts")).toBe(false);
      expect(isAncestorDir("src", "")).toBe(false);
      expect(isAncestorDir("", "")).toBe(false);
      expect(isAncestorDir("src/main.ts", "src/main.ts")).toBe(false);
      expect(isAncestorDir("src", "src")).toBe(false);
      expect(isAncestorDir("src/", "src/")).toBe(false);
    });
  });

  describe("isSessionTmpDir", () => {
    it("returns true for empty, /tmp, /tmp/session-id, and session-specific tmp directories", () => {
      expect(isSessionTmpDir("")).toBe(true);
      expect(isSessionTmpDir(null)).toBe(true);
      expect(isSessionTmpDir(undefined)).toBe(true);
      expect(isSessionTmpDir(".")).toBe(true);
      expect(isSessionTmpDir("/tmp")).toBe(true);
      expect(isSessionTmpDir("/tmp/")).toBe(true);
      expect(isSessionTmpDir(".tmp")).toBe(true);
      expect(isSessionTmpDir(".tmp/")).toBe(true);
      expect(isSessionTmpDir("/tmp/session-id")).toBe(true);
      expect(isSessionTmpDir(".tmp/session-id")).toBe(true);
      expect(isSessionTmpDir("/tmp/${session_id}")).toBe(true);
      expect(isSessionTmpDir(".tmp/${session_id}")).toBe(true);
      expect(isSessionTmpDir("/tmp/session-id/sub")).toBe(true);
      expect(isSessionTmpDir(".tmp/session-id/sub")).toBe(true);
      expect(isSessionTmpDir("/tmp/${session_id}/sub")).toBe(true);
      expect(isSessionTmpDir(".tmp/${session_id}/sub")).toBe(true);
      expect(isSessionTmpDir("/tmp/sess-123", "sess-123")).toBe(true);
      expect(isSessionTmpDir(".tmp/sess-123", "sess-123")).toBe(true);
      expect(isSessionTmpDir("/tmp/sess-123/sub", "sess-123")).toBe(true);
      expect(isSessionTmpDir(".tmp/sess-123/sub", "sess-123")).toBe(true);
    });

    it("returns true for relative tmp forms and subpaths", () => {
      expect(isSessionTmpDir("tmp")).toBe(true);
      expect(isSessionTmpDir("tmp/")).toBe(true);
      expect(isSessionTmpDir("tmp/session-id")).toBe(true);
      expect(isSessionTmpDir("tmp/session-id/sub")).toBe(true);
      expect(isSessionTmpDir("tmp/${session_id}")).toBe(true);
      expect(isSessionTmpDir("tmp/sess-123", "sess-123")).toBe(true);
      expect(isSessionTmpDir("tmp/sess-123/sub", "sess-123")).toBe(true);
      expect(isSessionTmpDir("tmp//sess-123", "sess-123")).toBe(true);
      expect(isSessionTmpDir("./tmp/sess-123", "sess-123")).toBe(true);
      expect(isSessionTmpDir("./tmp/session-id", "sess-123")).toBe(true);
    });

    it("returns true for host absolute tmp paths with exact segment matching", () => {
      expect(isSessionTmpDir("/home/user/tmp/sess-123", "sess-123")).toBe(true);
      expect(isSessionTmpDir("/home/user/tmp/sess-123/sub", "sess-123")).toBe(true);
      expect(isSessionTmpDir("/home/alice/tmp/sess-123", "sess-123")).toBe(true);
      expect(isSessionTmpDir("/root/tmp/sess-123", "sess-123")).toBe(true);
      expect(isSessionTmpDir("/root/tmp/sess-123/sub", "sess-123")).toBe(true);
    });

    it("returns false for regular project directories and arbitrary nested paths", () => {
      expect(isSessionTmpDir("/home/user/project")).toBe(false);
      expect(isSessionTmpDir("src/my-project")).toBe(false);
      expect(isSessionTmpDir("/var/www/html")).toBe(false);
      expect(isSessionTmpDir("/home/user/other/tmp/sess-123", "sess-123")).toBe(false);
      expect(isSessionTmpDir("/tmp/other-project", "sess-123")).toBe(false);
      expect(isSessionTmpDir("tmpother")).toBe(false);
      expect(isSessionTmpDir("tmpother/file.txt")).toBe(false);
      expect(isSessionTmpDir("src/tmp")).toBe(false);
    });
  });

  describe("isTmpScopePath", () => {
    it("matches /tmp namespace paths", () => {
      expect(isTmpScopePath("/tmp")).toBe(true);
      expect(isTmpScopePath("tmp")).toBe(true);
      expect(isTmpScopePath(".tmp")).toBe(true);
      expect(isTmpScopePath("/tmp/file.txt")).toBe(true);
      expect(isTmpScopePath("tmp/file.txt")).toBe(true);
      expect(isTmpScopePath(".tmp/file.txt")).toBe(true);
      expect(isTmpScopePath("\\tmp\\file.txt")).toBe(true);
    });

    it("rejects non-tmp paths", () => {
      expect(isTmpScopePath(null)).toBe(false);
      expect(isTmpScopePath(undefined)).toBe(false);
      expect(isTmpScopePath("")).toBe(false);
      expect(isTmpScopePath("/session/file.txt")).toBe(false);
      expect(isTmpScopePath("tmpother/file.txt")).toBe(false);
      expect(isTmpScopePath("src/main.go")).toBe(false);
    });
  });

  describe("isSessionScopePath", () => {
    it("matches /session namespace paths", () => {
      expect(isSessionScopePath("/session")).toBe(true);
      expect(isSessionScopePath("session")).toBe(true);
      expect(isSessionScopePath(".session")).toBe(true);
      expect(isSessionScopePath("/session/file.txt")).toBe(true);
      expect(isSessionScopePath("session/file.txt")).toBe(true);
      expect(isSessionScopePath(".session/file.txt")).toBe(true);
      expect(isSessionScopePath("\\session\\file.txt")).toBe(true);
    });

    it("rejects non-session paths", () => {
      expect(isSessionScopePath(null)).toBe(false);
      expect(isSessionScopePath(undefined)).toBe(false);
      expect(isSessionScopePath("")).toBe(false);
      expect(isSessionScopePath("/tmp/file.txt")).toBe(false);
      expect(isSessionScopePath("sessionother/file.txt")).toBe(false);
      expect(isSessionScopePath("src/main.go")).toBe(false);
    });
  });
});
