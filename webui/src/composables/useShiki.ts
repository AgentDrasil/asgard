import { onMounted, onUnmounted, readonly, ref, shallowRef } from "vue";
import { createHighlighter, type Highlighter } from "shiki";

// Mapping DaisyUI / App themes to standard Shiki themes
export function getShikiTheme(docTheme: string | null): string {
  switch (docTheme) {
    case "latte":
      return "catppuccin-latte";
    case "frappe":
      return "catppuccin-frappe";
    case "macchiato":
      return "catppuccin-macchiato";
    case "mocha":
      return "catppuccin-mocha";
    case "light":
    case "cupcake":
      return "github-light";
    case "dark":
    default:
      return "github-dark";
  }
}

const SUPPORTED_THEMES = [
  "catppuccin-latte",
  "catppuccin-frappe",
  "catppuccin-macchiato",
  "catppuccin-mocha",
  "github-light",
  "github-dark",
];

// Primary language ids only; aliases (js/py/sh/yml/md/...) are resolved by
// Shiki at call time and need not be pre-registered.
const BUNDLED_LANGS = [
  "markdown",
  "json",
  "jsonc",
  "javascript",
  "typescript",
  "vue",
  "html",
  "css",
  "go",
  "python",
  "bash",
  "zsh",
  "yaml",
  "dockerfile",
  "diff",
  "sql",
  "c",
  "cpp",
];

// Module-level singletons shared across all useShiki() consumers.
const highlighter = shallowRef<Highlighter | null>(null);
const isReady = ref(false);
const activeShikiTheme = ref<string>(
  typeof document !== "undefined"
    ? getShikiTheme(document.documentElement.getAttribute("data-theme"))
    : getShikiTheme(null),
);

let highlighterPromise: Promise<Highlighter> | null = null;

// Singleton theme observer, ref-counted so it lives only while some component
// is mounted.
let themeObserver: MutationObserver | null = null;
let observerRefCount = 0;

// Per-(theme, lang, code) highlight cache. Cleared on theme change and capped
// to avoid unbounded growth in long-lived chat sessions.
const CACHE_MAX = 256;
const highlightCache = new Map<string, string>();

function resetHighlightCache(): void {
  highlightCache.clear();
}

function cacheGet(key: string): string | undefined {
  if (!highlightCache.has(key)) return undefined;
  // Refresh insertion order so the entry is treated as most-recently-used.
  const value = highlightCache.get(key) as string;
  highlightCache.delete(key);
  highlightCache.set(key, value);
  return value;
}

function cacheSet(key: string, value: string): void {
  if (highlightCache.size >= CACHE_MAX) {
    const oldest = highlightCache.keys().next();
    if (!oldest.done) highlightCache.delete(oldest.value as string);
  }
  highlightCache.set(key, value);
}

export function getHighlighterSync(): Highlighter | null {
  return highlighter.value;
}

export async function getHighlighter(): Promise<Highlighter> {
  if (highlighter.value) return highlighter.value;
  if (!highlighterPromise) {
    isReady.value = false;
    highlighterPromise = createHighlighter({
      themes: SUPPORTED_THEMES,
      langs: BUNDLED_LANGS,
    })
      .then((h) => {
        highlighter.value = h;
        isReady.value = true;
        return h;
      })
      .catch((err) => {
        // Allow a subsequent call to retry instead of caching the rejection.
        highlighterPromise = null;
        throw err;
      });
  }
  return highlighterPromise;
}

function ensureThemeObserver(): void {
  observerRefCount += 1;
  if (themeObserver) return;
  const syncTheme = (): void => {
    const themeAttr =
      typeof document !== "undefined" ? document.documentElement.getAttribute("data-theme") : null;
    const next = getShikiTheme(themeAttr);
    if (next !== activeShikiTheme.value) {
      activeShikiTheme.value = next;
      resetHighlightCache();
    }
  };
  syncTheme();
  if (typeof document === "undefined") return;
  themeObserver = new MutationObserver(syncTheme);
  themeObserver.observe(document.documentElement, {
    attributes: true,
    attributeFilter: ["data-theme"],
  });
}

function releaseThemeObserver(): void {
  observerRefCount = Math.max(0, observerRefCount - 1);
  if (observerRefCount === 0 && themeObserver) {
    themeObserver.disconnect();
    themeObserver = null;
  }
}

/**
 * Render a code snippet with Shiki. Reads the reactive highlighter/theme
 * singletons so any reactive caller (computed/template) re-runs when Shiki
 * finishes loading or the theme changes. Returns "" until the highlighter is
 * ready.
 */
export const highlightToHtml = (code: string, lang: string = "text"): string => {
  const instance = highlighter.value;
  const theme = activeShikiTheme.value;
  if (!instance) return "";

  const key = `${theme}\u0000${lang}\u0000${code}`;
  const cached = cacheGet(key);
  if (cached !== undefined) return cached;

  let result: string;
  try {
    result = instance.codeToHtml(code, { lang, theme });
  } catch {
    try {
      result = instance.codeToHtml(code, { lang: "text", theme });
    } catch {
      return "";
    }
  }
  cacheSet(key, result);
  return result;
};

/** Add extra Tailwind classes onto the Shiki-generated <pre> element. */
function applyClassesToShikiPre(html: string, extraClasses: string[]): string | null {
  if (typeof document === "undefined") return null;
  const container = document.createElement("div");
  container.innerHTML = html;
  const shikiPre = container.querySelector("pre");
  if (!shikiPre) return null;
  if (extraClasses.length > 0) shikiPre.classList.add(...extraClasses);
  return container.innerHTML;
}

/**
 * Highlight a single code block and decorate the resulting <pre> with extra
 * classes. Returns "" if the highlighter isn't ready yet.
 */
export const highlightBlock = (code: string, lang: string, extraClasses: string[] = []): string => {
  const highlighted = highlightToHtml(code, lang);
  if (!highlighted) return "";
  return applyClassesToShikiPre(highlighted, extraClasses) ?? "";
};

/**
 * Replace every `<pre><code>` in a markdown-rendered HTML string with a
 * Shiki-highlighted <pre> decorated with extra classes. Safe to call in the
 * browser after DOMPurify has already sanitized `html`.
 */
export const highlightHtmlCodeBlocks = (html: string, blockClasses: string[] = []): string => {
  if (typeof document === "undefined") return html;
  const tempDiv = document.createElement("div");
  tempDiv.innerHTML = html;
  const codeBlocks = tempDiv.querySelectorAll("pre code");
  codeBlocks.forEach((codeEl) => {
    const langClass = Array.from(codeEl.classList).find((c) => c.startsWith("language-"));
    const lang = langClass ? langClass.replace("language-", "") : "text";
    const decorated = highlightBlock(codeEl.textContent || "", lang, blockClasses);
    if (!decorated) return;
    const parentPre = codeEl.parentElement;
    if (!parentPre) return;
    const wrapper = document.createElement("div");
    wrapper.innerHTML = decorated;
    const newPre = wrapper.querySelector("pre");
    if (newPre) parentPre.replaceWith(newPre);
  });
  return tempDiv.innerHTML;
};

export function useShiki() {
  onMounted(() => {
    ensureThemeObserver();
    if (!isReady.value && !highlighterPromise) {
      // Kick off loading; failures are swallowed so consumers fall back to a
      // plain <pre> while a later interaction can retry.
      getHighlighter().catch(() => {
        /* ignored */
      });
    }
  });
  onUnmounted(() => {
    releaseThemeObserver();
  });

  return {
    isReady: readonly(isReady),
    activeShikiTheme: readonly(activeShikiTheme),
    highlightToHtml,
    highlightBlock,
    highlightHtmlCodeBlocks,
    getHighlighter,
  };
}
