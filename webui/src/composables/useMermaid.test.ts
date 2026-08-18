import { describe, it, expect, vi } from "vitest";
import mermaid from "mermaid";
import { getMermaidTheme, generateUniqueId, renderDiagram } from "./useMermaid";

describe("useMermaid composable", () => {
  it("maps DaisyUI and Catppuccin themes accurately", () => {
    expect(getMermaidTheme("latte")).toBe("default");
    expect(getMermaidTheme("light")).toBe("default");
    expect(getMermaidTheme("cupcake")).toBe("default");
    expect(getMermaidTheme("mocha")).toBe("dark");
    expect(getMermaidTheme("macchiato")).toBe("dark");
    expect(getMermaidTheme("frappe")).toBe("dark");
    expect(getMermaidTheme("dark")).toBe("dark");
    expect(getMermaidTheme(null)).toBe("dark");
    expect(getMermaidTheme("unknown")).toBe("dark");
  });

  it("generates unique IDs", () => {
    const id1 = generateUniqueId("diagram");
    const id2 = generateUniqueId("diagram");
    expect(id1).not.toBe(id2);
    expect(id1.startsWith("diagram-")).toBe(true);
  });

  it("renders a valid mermaid diagram successfully", async () => {
    const renderSpy = vi.spyOn(mermaid, "render").mockResolvedValue({
      svg: "<svg>mock svg</svg>",
      diagramType: "graph",
      bindFunctions: undefined,
    });

    const code = `graph TD\n  A[Start] --> B[End]`;
    const result = await renderDiagram(code);
    expect(result.error).toBeUndefined();
    expect(result.svg).toBe("<svg>mock svg</svg>");

    renderSpy.mockRestore();
  });

  it("gracefully catches syntax errors in mermaid diagrams", async () => {
    const renderSpy = vi
      .spyOn(mermaid, "render")
      .mockRejectedValue(new Error("Syntax error in diagram"));

    const invalidCode = `graph INVALID SYNTAX {{{{`;
    const result = await renderDiagram(invalidCode);
    expect(result.svg).toBeUndefined();
    expect(result.error).toBe("Syntax error in diagram");

    renderSpy.mockRestore();
  });
});
