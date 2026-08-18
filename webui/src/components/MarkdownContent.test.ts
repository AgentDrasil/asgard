import { describe, it, expect } from "vitest";
import { splitMarkdownTokens, isMermaidToken, type RawSegment } from "../utils/markdownSegments";
import { marked } from "marked";

describe("MarkdownContent token parsing logic", () => {
  it("splits mixed markdown content with mermaid and standard markdown blocks", () => {
    const markdown = `# Title

Here is a diagram:

\`\`\`mermaid
graph TD
  A --> B
\`\`\`

And some text after with \`code\`.`;

    const segments = splitMarkdownTokens(markdown);

    expect(segments).toHaveLength(3);
    expect(segments[0].type).toBe("markdown");
    expect(segments[1].type).toBe("mermaid");
    expect((segments[1] as { type: "mermaid"; code: string }).code.trim()).toBe(
      "graph TD\n  A --> B",
    );
    expect(segments[2].type).toBe("markdown");
  });

  it("handles markdown with multiple mermaid blocks and text in-between", () => {
    const markdown = `\`\`\`mermaid
graph LR
  X --> Y
\`\`\`

Middle text

\`\`\`mermaid
sequenceDiagram
  Alice->>Bob: Hello
\`\`\``;

    const segments = splitMarkdownTokens(markdown);

    expect(segments).toHaveLength(3);
    expect(segments[0].type).toBe("mermaid");
    expect((segments[0] as { type: "mermaid"; code: string }).code).toContain("X --> Y");
    expect(segments[1].type).toBe("markdown");
    expect(segments[2].type).toBe("mermaid");
    expect((segments[2] as { type: "mermaid"; code: string }).code).toContain("Alice->>Bob: Hello");
  });

  it("handles empty or purely textual markdown without mermaid blocks", () => {
    const emptySegments = splitMarkdownTokens("");
    expect(emptySegments).toHaveLength(0);

    const textSegments = splitMarkdownTokens("Just plain **bold** text.");
    expect(textSegments).toHaveLength(1);
    expect(textSegments[0].type).toBe("markdown");
  });

  it("extracts nested mermaid code blocks from list items as fallback", () => {
    const markdown = `1. Step 1: Initialize
   \`\`\`mermaid
   graph TD
     A --> B
   \`\`\`
2. Step 2: Conclude`;

    const segments = splitMarkdownTokens(markdown);

    expect(segments).toHaveLength(2);
    expect(segments[0].type).toBe("markdown");
    expect(segments[1].type).toBe("mermaid");
    expect((segments[1] as { type: "mermaid"; code: string }).code.trim()).toBe(
      "graph TD\n  A --> B",
    );

    // Verify the list is rendered in the markdown chunk without the raw mermaid code block
    const firstMarkdown = segments[0] as Extract<RawSegment, { type: "markdown" }>;
    const html = marked.parser(firstMarkdown.tokens);
    expect(html).toContain("Step 1: Initialize");
    expect(html).toContain("Step 2: Conclude");
    expect(html).not.toContain('<code class="language-mermaid">');
  });

  it("extracts nested mermaid code blocks from blockquotes", () => {
    const markdown = `> Note: see diagram below
> \`\`\`mermaid
> graph LR
>   C --> D
> \`\`\`
> End of quote.`;

    const segments = splitMarkdownTokens(markdown);

    expect(segments).toHaveLength(2);
    expect(segments[0].type).toBe("markdown");
    expect(segments[1].type).toBe("mermaid");
    expect((segments[1] as { type: "mermaid"; code: string }).code.trim()).toBe(
      "graph LR\n  C --> D",
    );

    const firstMarkdown = segments[0] as Extract<RawSegment, { type: "markdown" }>;
    const html = marked.parser(firstMarkdown.tokens);
    expect(html).toContain("Note: see diagram below");
    expect(html).not.toContain('<code class="language-mermaid">');
  });

  it("correctly identifies mermaid tokens via isMermaidToken helper", () => {
    const mermaidToken = { type: "code", lang: "mermaid", text: "graph TD" } as any;
    const uppercaseMermaid = { type: "code", lang: "  MERMAID  ", text: "graph TD" } as any;
    const jsToken = { type: "code", lang: "javascript", text: "console.log(1)" } as any;
    const textToken = { type: "text", text: "hello" } as any;

    expect(isMermaidToken(mermaidToken)).toBe(true);
    expect(isMermaidToken(uppercaseMermaid)).toBe(true);
    expect(isMermaidToken(jsToken)).toBe(false);
    expect(isMermaidToken(textToken)).toBe(false);
  });
});
