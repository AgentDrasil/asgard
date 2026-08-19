/**
 * File utility functions for icon resolution, syntax language detection,
 * HTML escaping, syntax-highlighted HTML line extraction, and directory tree relationships.
 */

export function isAncestorDir(dirPath: string, targetPath: string): boolean {
  if (!dirPath || !targetPath) return false;
  const normalizedDir = dirPath.endsWith("/") ? dirPath : dirPath + "/";
  const normalizedTarget = targetPath.endsWith("/") ? targetPath.slice(0, -1) : targetPath;
  const strippedDir = normalizedDir.slice(0, -1);
  if (strippedDir === normalizedTarget) return false;
  return targetPath.startsWith(normalizedDir);
}

export function mapExtToLang(ext?: string): string {
  const e = (ext || "").toLowerCase();
  switch (e) {
    case "go":
      return "go";
    case "ts":
    case "tsx":
      return "typescript";
    case "js":
    case "jsx":
    case "mjs":
    case "cjs":
      return "javascript";
    case "vue":
      return "vue";
    case "json":
    case "jsonc":
    case "json5":
      return "json";
    case "html":
    case "htm":
      return "html";
    case "css":
    case "scss":
    case "sass":
    case "less":
      return "css";
    case "yaml":
    case "yml":
      return "yaml";
    case "md":
    case "markdown":
      return "markdown";
    case "py":
      return "python";
    case "sh":
    case "bash":
    case "zsh":
      return "bash";
    case "sql":
      return "sql";
    case "c":
    case "h":
      return "c";
    case "cpp":
    case "cc":
    case "cxx":
    case "hpp":
      return "cpp";
    case "dockerfile":
      return "dockerfile";
    case "diff":
    case "patch":
      return "diff";
    default:
      return "text";
  }
}

export function getFileIcon(ext?: string, path?: string): string {
  const fileExt = (ext || path?.split(".").pop() || "").toLowerCase();
  switch (fileExt) {
    case "md":
    case "markdown":
      return "octicon:markdown-24";
    case "go":
      return "vscode-icons:file-type-go";
    case "ts":
    case "tsx":
      return "vscode-icons:file-type-typescript";
    case "js":
    case "jsx":
      return "vscode-icons:file-type-js";
    case "vue":
      return "vscode-icons:file-type-vue";
    case "json":
      return "vscode-icons:file-type-json";
    case "css":
    case "scss":
    case "sass":
    case "less":
      return "vscode-icons:file-type-css";
    case "html":
    case "htm":
      return "vscode-icons:file-type-html";
    case "yaml":
    case "yml":
      return "file-icons:yaml";
    case "sh":
    case "bash":
    case "zsh":
      return "vscode-icons:file-type-shell";
    case "py":
      return "vscode-icons:file-type-python";
    case "rs":
      return "vscode-icons:file-type-rust";
    case "c":
    case "h":
      return "vscode-icons:file-type-c";
    case "cpp":
    case "cc":
    case "cxx":
    case "hpp":
      return "vscode-icons:file-type-cpp";
    case "sql":
      return "vscode-icons:file-type-sql";
    default:
      return "octicon:file-code-24";
  }
}

export function escapeHtml(str: string): string {
  return str
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;")
    .replace(/'/g, "&#039;");
}

/**
 * Extract per-line inner HTML from Shiki-highlighted HTML output.
 * Preserves nested token spans within each line.
 */
export function extractHighlightedLines(html: string, expectedCount?: number): string[] {
  if (!html) return [];

  // 1. Try DOMParser if available (e.g. browser environment)
  if (typeof DOMParser !== "undefined") {
    try {
      const doc = new DOMParser().parseFromString(html, "text/html");
      const lineEls = Array.from(
        doc.querySelectorAll("code > span.line, pre > span.line, span.line"),
      );
      if (lineEls.length > 0 && (expectedCount === undefined || lineEls.length === expectedCount)) {
        return lineEls.map((el) => el.innerHTML);
      }
    } catch {
      // Fallback to pure string scanner below
    }
  }

  // 2. Pure balanced-span scanner (Node.js / Vitest / SSR / browser fallback)
  const lines: string[] = [];
  let searchIdx = 0;

  while (searchIdx < html.length) {
    const lineStart = html.indexOf('<span class="line"', searchIdx);
    const lineStartAlt = html.indexOf("<span class='line'", searchIdx);
    let startIdx = -1;
    if (lineStart !== -1 && lineStartAlt !== -1) {
      startIdx = Math.min(lineStart, lineStartAlt);
    } else {
      startIdx = lineStart !== -1 ? lineStart : lineStartAlt;
    }

    if (startIdx === -1) break;

    const openTagEnd = html.indexOf(">", startIdx);
    if (openTagEnd === -1) break;

    const contentStart = openTagEnd + 1;
    let depth = 1;
    let curr = contentStart;

    while (curr < html.length && depth > 0) {
      if (html.startsWith("<span", curr)) {
        const nextChar = html[curr + 5];
        if (nextChar === " " || nextChar === ">" || nextChar === "\t" || nextChar === "\n") {
          depth++;
        }
        curr += 5;
      } else if (html.startsWith("</span>", curr)) {
        depth--;
        if (depth === 0) {
          lines.push(html.slice(contentStart, curr));
          curr += 7;
          searchIdx = curr;
          break;
        }
        curr += 7;
      } else {
        curr++;
      }
    }

    if (depth > 0) {
      break;
    }
  }

  if (lines.length > 0 && (expectedCount === undefined || lines.length === expectedCount)) {
    return lines;
  }

  return [];
}
