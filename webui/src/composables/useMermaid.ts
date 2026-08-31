import { onMounted, onUnmounted, readonly, ref, shallowRef } from "vue";
import type { MermaidConfig } from "mermaid";

type MermaidAPI = typeof import("mermaid").default;

export type MermaidTheme = "dark" | "default" | "forest" | "neutral" | "base";

/**
 * Mapping DaisyUI / Catppuccin themes to standard Mermaid themes.
 */
export function getMermaidTheme(docTheme: string | null): MermaidTheme {
  switch (docTheme) {
    case "latte":
    case "light":
    case "cupcake":
      return "default";
    case "frappe":
    case "macchiato":
    case "mocha":
    case "dark":
    default:
      return "dark";
  }
}

// Module-level singletons shared across all useMermaid() consumers.
const isInitialized = ref(false);
const mermaidInstance = shallowRef<MermaidAPI | null>(null);
let mermaidPromise: Promise<MermaidAPI> | null = null;

const activeMermaidTheme = ref<MermaidTheme>(
  typeof document !== "undefined"
    ? getMermaidTheme(document.documentElement.getAttribute("data-theme"))
    : getMermaidTheme(null),
);

let themeObserver: MutationObserver | null = null;
let observerRefCount = 0;
let idCounter = 0;

/**
 * Lazily dynamic-import the mermaid runtime so Vite code-splits it.
 */
async function loadMermaid(): Promise<MermaidAPI> {
  if (mermaidInstance.value) return mermaidInstance.value;
  if (!mermaidPromise) {
    mermaidPromise = import("mermaid")
      .then((mod) => {
        mermaidInstance.value = mod.default;
        return mod.default;
      })
      .catch((err) => {
        mermaidPromise = null; // allow retry instead of caching rejection
        throw err;
      });
  }
  return mermaidPromise;
}

/**
 * Initialize Mermaid global configuration asynchronously.
 */
export async function initMermaid(theme?: MermaidTheme): Promise<void> {
  const mermaid = await loadMermaid();
  const currentTheme = theme || activeMermaidTheme.value;
  const config: MermaidConfig = {
    startOnLoad: false,
    securityLevel: "loose",
    fontFamily: "inherit",
    theme: currentTheme,
    flowchart: {
      useMaxWidth: false,
      htmlLabels: true,
    },
    sequence: {
      useMaxWidth: false,
    },
    gantt: {
      useMaxWidth: false,
    },
  };
  mermaid.initialize(config);
  isInitialized.value = true;
}

function ensureThemeObserver(): void {
  observerRefCount += 1;
  if (themeObserver) return;
  const syncTheme = (): void => {
    const themeAttr =
      typeof document !== "undefined" ? document.documentElement.getAttribute("data-theme") : null;
    const next = getMermaidTheme(themeAttr);
    if (next !== activeMermaidTheme.value) {
      activeMermaidTheme.value = next;
      initMermaid(next).catch(() => {});
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

export interface RenderResult {
  svg?: string;
  error?: string;
  bindFunctions?: (element: Element) => void;
}

/**
 * Generate a unique ID for SVG element to prevent clipPath and marker collisions.
 */
export function generateUniqueId(prefix: string = "mermaid-svg"): string {
  idCounter += 1;
  return `${prefix}-${Date.now().toString(36)}-${idCounter.toString(36)}`;
}

/**
 * Safely render a Mermaid diagram code into an SVG string.
 * Catches syntax and rendering errors without throwing uncaught exceptions.
 */
export async function renderDiagram(code: string, idPrefix?: string): Promise<RenderResult> {
  const trimmed = code?.trim();
  if (!trimmed) {
    return {};
  }

  const mermaid = await loadMermaid();
  if (!isInitialized.value) {
    await initMermaid();
  }

  const id = generateUniqueId(idPrefix || "mermaid");

  try {
    const { svg, bindFunctions } = await mermaid.render(id, code);
    // Clean inline style max-width restrictions that mermaid injects to avoid blurry rasterized scaling
    const cleanedSvg = svg.replace(
      /<svg\b([^>]*)\bstyle="([^"]*)"([^>]*)>/i,
      (_match, p1, style, p3) => {
        const filteredStyle = style
          .split(";")
          .map((s: string) => s.trim())
          .filter((s: string) => !s.startsWith("max-width"))
          .join("; ");
        return `<svg ${p1} style="${filteredStyle}" ${p3}>`;
      },
    );
    return { svg: cleanedSvg, bindFunctions };
  } catch (err: unknown) {
    // If mermaid inserted an error element in the DOM before failing, clean it up if necessary
    if (typeof document !== "undefined") {
      const errorEl = document.getElementById(id);
      if (errorEl) {
        errorEl.remove();
      }
      const errDiagram = document.getElementById(`d${id}`);
      if (errDiagram) {
        errDiagram.remove();
      }
    }
    const errorMessage = err instanceof Error ? err.message : String(err);
    return { error: errorMessage };
  }
}

export function useMermaid() {
  onMounted(() => {
    ensureThemeObserver();
    if (!isInitialized.value) {
      initMermaid().catch(() => {});
    }
  });

  onUnmounted(() => {
    releaseThemeObserver();
  });

  return {
    isInitialized: readonly(isInitialized),
    activeMermaidTheme: readonly(activeMermaidTheme),
    getMermaidTheme,
    initMermaid,
    renderDiagram,
  };
}
