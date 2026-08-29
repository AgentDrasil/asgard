import { computed } from "vue";
import { processAST, type DiffFileHighlighter } from "@git-diff-view/vue";
import { useShiki, getShikiTheme, getHighlighterSync } from "./useShiki";

/**
 * Maps DaisyUI / Catppuccin document themes to "@git-diff-view" light/dark theme prop.
 */
export function getDiffTheme(docTheme: string | null): "light" | "dark" {
  switch (docTheme) {
    case "light":
    case "cupcake":
    case "latte":
      return "light";
    case "dark":
    case "mocha":
    case "macchiato":
    case "frappe":
    default:
      return "dark";
  }
}

/**
 * Creates a DiffHighlighter adapter connected to the shared Shiki highlighter instance.
 * Updates dynamically with the active document theme.
 */
export function useDiffHighlighter() {
  const { activeShikiTheme, isReady } = useShiki();

  const diffHighlighter = computed<DiffFileHighlighter | undefined>(() => {
    // When Shiki is not ready yet, return undefined so DiffView can fallback gracefully
    if (!isReady.value) {
      return undefined;
    }

    const currentTheme = activeShikiTheme.value;

    return {
      get name() {
        return `shiki-${currentTheme}`;
      },
      type: "style",
      maxLineToIgnoreSyntax: 2000,
      setMaxLineToIgnoreSyntax: () => {},
      ignoreSyntaxHighlightList: [],
      setIgnoreSyntaxHighlightList: () => {},
      getAST: (raw: string, _fileName?: string, lang?: string) => {
        const instance = getHighlighterSync();
        if (!instance) return undefined as any;
        try {
          return instance.codeToHast(raw, { lang: lang || "text", theme: currentTheme });
        } catch {
          try {
            return instance.codeToHast(raw, { lang: "text", theme: currentTheme });
          } catch {
            return undefined as any;
          }
        }
      },
      processAST: (ast: any) => {
        return processAST(ast);
      },
      hasRegisteredCurrentLang: (_lang: string) => true,
    };
  });

  return {
    diffHighlighter,
    isReady,
    activeShikiTheme,
    getShikiTheme,
  };
}
