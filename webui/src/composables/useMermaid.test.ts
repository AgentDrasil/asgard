import { describe, it, expect, vi } from "vitest";
import mermaid from "mermaid";
import {
  getMermaidTheme,
  generateUniqueId,
  renderDiagram,
  sanitizeMermaidCode,
} from "./useMermaid";

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

  it("sanitizes legacy graph directives to flowchart for engine consistency", () => {
    expect(sanitizeMermaidCode("graph TD\n  A --> B")).toBe("flowchart TD\n  A --> B");
    expect(sanitizeMermaidCode("graph LR\n  X --> Y")).toBe("flowchart LR\n  X --> Y");
    expect(sanitizeMermaidCode("graph TB\n  A --> B")).toBe("flowchart TB\n  A --> B");
    expect(sanitizeMermaidCode("graph BT\n  A --> B")).toBe("flowchart BT\n  A --> B");
    expect(sanitizeMermaidCode("graph RL\n  A --> B")).toBe("flowchart RL\n  A --> B");
    expect(sanitizeMermaidCode("%% initial comment\n  graph TD\n  A --> B")).toBe(
      "%% initial comment\n  flowchart TD\n  A --> B",
    );
    expect(sanitizeMermaidCode("sequenceDiagram\n  Alice->>Bob: Hi")).toBe(
      "sequenceDiagram\n  Alice->>Bob: Hi",
    );
    // Preserves unknown direction
    expect(sanitizeMermaidCode("graph UNKNOWN\n  A --> B")).toBe("graph UNKNOWN\n  A --> B");
    // Does NOT alter occurrences of graph inside multiline node labels
    const multilineLabelCode = `flowchart TD\n  A["First line\ngraph TD inside label\nThird line"] --> B`;
    expect(sanitizeMermaidCode(multilineLabelCode)).toBe(multilineLabelCode);
  });

  it("generates unique IDs", () => {
    const id1 = generateUniqueId("diagram");
    const id2 = generateUniqueId("diagram");
    expect(id1).not.toBe(id2);
    expect(id1.startsWith("diagram-")).toBe(true);
  });

  it("handles empty or whitespace-only code gracefully", async () => {
    const renderSpy = vi.spyOn(mermaid, "render");
    const resultEmpty = await renderDiagram("");
    expect(resultEmpty.svg).toBeUndefined();
    expect(resultEmpty.error).toBeUndefined();

    const resultWhitespace = await renderDiagram("   \n\t  ");
    expect(resultWhitespace.svg).toBeUndefined();
    expect(resultWhitespace.error).toBeUndefined();

    expect(renderSpy).not.toHaveBeenCalled();
    renderSpy.mockRestore();
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
    expect(renderSpy).toHaveBeenCalledWith(
      expect.stringMatching(/^mermaid-/),
      "flowchart TD\n  A[Start] --> B[End]",
    );

    renderSpy.mockRestore();
  });

  it("renders complex CJK subgraph diagrams correctly", async () => {
    const renderSpy = vi.spyOn(mermaid, "render").mockResolvedValue({
      svg: "<svg><g class='subgraph'>mock subgraph</g></svg>",
      diagramType: "flowchart",
      bindFunctions: undefined,
    });

    const code = `graph TD\n  subgraph S["useSessionStore.ts - session state and message sending"]\n    A["POST /api/agents/:id/message"]\n  end\n  B["2. Send message with attachments"] --> S`;
    const result = await renderDiagram(code);
    expect(result.error).toBeUndefined();
    expect(result.svg).toContain("mock subgraph");
    expect(renderSpy).toHaveBeenCalledWith(
      expect.stringMatching(/^mermaid-/),
      `flowchart TD\n  subgraph S["useSessionStore.ts - session state and message sending"]\n    A["POST /api/agents/:id/message"]\n  end\n  B["2. Send message with attachments"] --> S`,
    );

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
