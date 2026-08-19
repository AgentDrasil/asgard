import { describe, it, expect } from "vitest";
import { mapExtToLang, getFileIcon, escapeHtml, extractHighlightedLines } from "./fileUtils";

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
      expect(getFileIcon(undefined, "src/components/App.vue")).toBe("vscode-icons:file-type-vue");
      expect(getFileIcon(undefined, "some/path/file.xyz")).toBe("octicon:file-code-24");
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
});
