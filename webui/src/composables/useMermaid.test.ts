import { describe, it, expect } from "vitest";
import { generateUniqueId, renderDiagram } from "./useMermaid";

describe("useMermaid composable (pele)", () => {
  it("generates unique IDs", () => {
    const id1 = generateUniqueId("diagram");
    const id2 = generateUniqueId("diagram");
    expect(id1).not.toBe(id2);
    expect(id1.startsWith("diagram-")).toBe(true);
  });

  it("handles empty or whitespace-only code gracefully", async () => {
    const resultEmpty = await renderDiagram("");
    expect(resultEmpty.svg).toBeUndefined();
    expect(resultEmpty.error).toBeUndefined();

    const resultWhitespace = await renderDiagram("   \n\t  ");
    expect(resultWhitespace.svg).toBeUndefined();
    expect(resultWhitespace.error).toBeUndefined();
  });

  it("renders a valid mermaid diagram successfully", async () => {
    const code = `graph TD\n  A[Start] --> B[End]`;
    const result = await renderDiagram(code);
    expect(result.error).toBeUndefined();
    expect(result.svg).toContain("<svg");
    expect(result.svg).toContain("pele");
  });

  it("renders complex CJK subgraph diagrams correctly", async () => {
    const code = `graph TD\n  subgraph S["useSessionStore.ts - 会话状态与消息发送"]\n    A["POST /api/agents/:id/message"]\n  end\n  B["2. 发送带附件的消息"] --> S`;
    const result = await renderDiagram(code);
    expect(result.error).toBeUndefined();
    expect(result.svg).toContain("<svg");
    expect(result.svg).toContain("useSessionStore.ts");
  });

  it("gracefully catches syntax errors in mermaid diagrams", async () => {
    const invalidCode = `graph INVALID SYNTAX {{{{`;
    const result = await renderDiagram(invalidCode);
    expect(result.svg).toBeUndefined();
    expect(result.error).toBeTruthy();
  });
});
