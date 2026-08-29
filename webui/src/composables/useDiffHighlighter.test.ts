import { describe, it, expect } from "vitest";
import { useDiffHighlighter, getDiffTheme } from "./useDiffHighlighter";
import { getShikiTheme, getHighlighter } from "./useShiki";
import { DiffFile } from "@git-diff-view/vue";

describe("useDiffHighlighter", () => {
  it("maps theme names correctly to DiffView theme mode (light/dark)", () => {
    expect(getDiffTheme("cupcake")).toBe("light");
    expect(getDiffTheme("light")).toBe("light");
    expect(getDiffTheme("latte")).toBe("light");
    expect(getDiffTheme("dark")).toBe("dark");
    expect(getDiffTheme("mocha")).toBe("dark");
    expect(getDiffTheme("macchiato")).toBe("dark");
    expect(getDiffTheme("frappe")).toBe("dark");
    expect(getDiffTheme(null)).toBe("dark");
  });

  it("maps theme names correctly to Shiki theme names", () => {
    expect(getShikiTheme("mocha")).toBe("catppuccin-mocha");
    expect(getShikiTheme("macchiato")).toBe("catppuccin-macchiato");
    expect(getShikiTheme("frappe")).toBe("catppuccin-frappe");
    expect(getShikiTheme("latte")).toBe("catppuccin-latte");
    expect(getShikiTheme("cupcake")).toBe("github-light");
    expect(getShikiTheme("light")).toBe("github-light");
    expect(getShikiTheme("dark")).toBe("github-dark");
    expect(getShikiTheme(null)).toBe("github-dark");
  });

  it("integrates with DiffFile for Shiki AST generation", async () => {
    await getHighlighter();
    const { diffHighlighter } = useDiffHighlighter();

    expect(diffHighlighter.value).toBeDefined();

    const df = DiffFile.createInstance({
      oldFile: { fileName: "test.ts", content: "const a: number = 1;" },
      newFile: { fileName: "test.ts", content: "const a: number = 2;" },
      hunks: ["@@ -1,1 +1,1 @@\n-const a: number = 1;\n+const a: number = 2;"],
    });

    df.initTheme("dark");
    df.initRaw();
    if (diffHighlighter.value) {
      df.initSyntax({ registerHighlighter: diffHighlighter.value });
    }
    df.buildSplitDiffLines();
    df.buildUnifiedDiffLines();

    const syntaxLine = df.getNewSyntaxLine(1);
    expect(syntaxLine).toBeDefined();
    expect(syntaxLine.template).toContain("const");
    expect(syntaxLine.template).toContain("style=");
  });
});
