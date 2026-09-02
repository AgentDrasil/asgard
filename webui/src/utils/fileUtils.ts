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
    case "csv":
    case "tsv":
      return "csv";
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

export const IMAGE_EXTENSIONS = new Set([
  "png",
  "jpg",
  "jpeg",
  "gif",
  "webp",
  "svg",
  "ico",
  "bmp",
  "avif",
]);

export const VIDEO_EXTENSIONS = new Set(["mp4", "webm", "ogv", "mov"]);

export const AUDIO_EXTENSIONS = new Set(["ogg", "mp3", "wav", "oga", "aac", "m4a", "flac"]);

export const PDF_EXTENSIONS = new Set(["pdf"]);

export const CSV_EXTENSIONS = new Set(["csv", "tsv"]);

export const BINARY_EXTENSIONS = new Set([
  "zip",
  "tar",
  "gz",
  "tgz",
  "bz2",
  "xz",
  "7z",
  "rar",
  "exe",
  "dll",
  "so",
  "dylib",
  "o",
  "a",
  "obj",
  "lib",
  "bin",
  "dat",
  "wasm",
  "class",
  "jar",
  "iso",
  "img",
  "db",
  "sqlite",
  "sqlite3",
  "pyc",
  "woff",
  "woff2",
  "ttf",
  "otf",
  "eot",
]);

export function extractExt(ext?: string, path?: string): string {
  if (ext) {
    return ext.toLowerCase().replace(/^\./, "");
  }
  if (path) {
    const base = path.split("/").pop() ?? path;
    const parts = base.split(".");
    if (parts.length > 1) {
      return (parts.pop() ?? "").toLowerCase();
    }
  }
  return "";
}

export function isImageFile(ext?: string, path?: string): boolean {
  const e = extractExt(ext, path);
  return IMAGE_EXTENSIONS.has(e);
}

export function isVideoFile(ext?: string, path?: string): boolean {
  const e = extractExt(ext, path);
  return VIDEO_EXTENSIONS.has(e);
}

export function isAudioFile(ext?: string, path?: string): boolean {
  const e = extractExt(ext, path);
  return AUDIO_EXTENSIONS.has(e);
}

export function isPdfFile(ext?: string, path?: string): boolean {
  const e = extractExt(ext, path);
  return PDF_EXTENSIONS.has(e);
}

export function isCsvFile(ext?: string, path?: string): boolean {
  const e = extractExt(ext, path);
  return CSV_EXTENSIONS.has(e);
}

export type MediaCategory =
  | "image"
  | "video"
  | "audio"
  | "pdf"
  | "csv"
  | "markdown"
  | "code"
  | "binary";

export function getMediaCategory(ext?: string, path?: string): MediaCategory {
  const e = extractExt(ext, path);
  if (isImageFile(e)) return "image";
  if (isVideoFile(e)) return "video";
  if (isAudioFile(e)) return "audio";
  if (isPdfFile(e)) return "pdf";
  if (isCsvFile(e)) return "csv";
  if (e === "md" || e === "markdown") return "markdown";
  if (!e) return "code"; // extensionless → treat as text
  if (BINARY_EXTENSIONS.has(e)) return "binary";
  return "code"; // unknown ext → text-safe default for a coding workspace
}

export function resolveViewerCategory(
  file?: {
    ext?: string;
    path?: string;
    isBinary?: boolean;
  } | null,
): MediaCategory {
  if (!file) return "code";
  const cat = getMediaCategory(file.ext, file.path);
  if (cat === "code" && file.isBinary) {
    return "binary";
  }
  return cat;
}

export function getFileIcon(ext?: string, path?: string): string {
  const fileExt = extractExt(ext, path);
  const baseName = (path || "").split("/").pop()?.toLowerCase();
  if (
    baseName === "ui_manifest.json" ||
    baseName === "ui-manifest.json" ||
    (baseName && baseName.endsWith(".a2ui.json"))
  ) {
    return "material-symbols:dashboard-customize-outline";
  }

  if (isImageFile(fileExt)) {
    return "vscode-icons:file-type-image";
  }
  if (isVideoFile(fileExt)) {
    return "vscode-icons:file-type-video";
  }
  if (isAudioFile(fileExt)) {
    return "vscode-icons:file-type-audio";
  }
  if (isPdfFile(fileExt)) {
    return "vscode-icons:file-type-pdf";
  }
  if (isCsvFile(fileExt)) {
    return "vscode-icons:file-type-excel";
  }

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

/**
 * Checks whether a given path belongs to the session /tmp namespace
 * (/tmp, tmp, .tmp and their subpaths).
 */
export function isTmpScopePath(path?: string | null): boolean {
  if (!path) return false;
  const clean = path.replace(/\\/g, "/").trim();
  return (
    clean === "/tmp" ||
    clean === "tmp" ||
    clean === ".tmp" ||
    clean.startsWith("/tmp/") ||
    clean.startsWith("tmp/") ||
    clean.startsWith(".tmp/")
  );
}

/**
 * Checks whether a given path belongs to the session /session namespace
 * (/session, session, .session and their subpaths).
 */
export function isSessionScopePath(path?: string | null): boolean {
  if (!path) return false;
  const clean = path.replace(/\\/g, "/").trim();
  return (
    clean === "/session" ||
    clean === "session" ||
    clean === ".session" ||
    clean.startsWith("/session/") ||
    clean.startsWith("session/") ||
    clean.startsWith(".session/")
  );
}

/**
 * Checks whether a given runDir corresponds to the session temporary directory (/tmp, tmp, /tmp/session-id, tmp/session-id, /home/<user>/tmp/<sessionId>, etc.).
 */
export function isSessionTmpDir(runDir?: string | null, sessionId?: string | null): boolean {
  if (!runDir || runDir === "." || runDir === "") return true;
  let normalized = runDir.replace(/\\/g, "/").trim().replace(/\/+/g, "/");
  normalized = normalized.replace(/^\.\//, "").replace(/\/+$/, "");

  // 1. Explicit bare directory or placeholder names (and subpaths)
  const exactBareDirs = ["tmp", "/tmp", ".tmp"];
  if (exactBareDirs.includes(normalized)) {
    return true;
  }

  const placeholderPrefixes = [
    "tmp/session-id",
    "/tmp/session-id",
    ".tmp/session-id",
    "tmp/${session_id}",
    "/tmp/${session_id}",
    ".tmp/${session_id}",
  ];

  if (
    placeholderPrefixes.some(
      (prefix) => normalized === prefix || normalized.startsWith(`${prefix}/`),
    )
  ) {
    return true;
  }

  // 2. Matching with actual sessionId
  if (sessionId) {
    const sessionPrefixes = [`tmp/${sessionId}`, `/tmp/${sessionId}`, `.tmp/${sessionId}`];
    if (
      sessionPrefixes.some((prefix) => normalized === prefix || normalized.startsWith(`${prefix}/`))
    ) {
      return true;
    }

    // 3. Host absolute path segments matching: /home/<user>/tmp/<sessionId> or /root/tmp/<sessionId> (and subpaths)
    // Structure must strictly be /home/<user>/tmp/<sessionId>[/...] or /root/tmp/<sessionId>[/...]
    const parts = normalized.split("/").filter(Boolean);
    // For /home/<user>/tmp/<sessionId>, parts: ["home", "<user>", "tmp", sessionId, ...] (length >= 4)
    if (parts.length >= 4 && parts[0] === "home" && parts[2] === "tmp" && parts[3] === sessionId) {
      return true;
    }
    // For /root/tmp/<sessionId>, parts: ["root", "tmp", sessionId, ...] (length >= 3)
    if (parts.length >= 3 && parts[0] === "root" && parts[1] === "tmp" && parts[2] === sessionId) {
      return true;
    }
  }

  return false;
}
