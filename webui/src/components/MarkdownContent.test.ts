// @vitest-environment jsdom
import { describe, it, expect } from "vitest";
import { splitMarkdownTokens, isMermaidToken, type RawSegment } from "../utils/markdownSegments";
import { sanitizeMarkdownHtml } from "../utils/markdownSanitize";
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

  describe("Markdown and LaTeX integration with DOMPurify sanitization", () => {
    it("renders mixed markdown with formulas, mermaid, and code blocks", () => {
      const markdown = `# Math and Diagrams

Inline formula $x^2 + y^2 = z^2$ and paren formula \\(a+b=c\\).

\`\`\`mermaid
graph TD
  Start --> Stop
\`\`\`

Display formula:
$$
E = mc^2
$$

\`\`\`typescript
const answer = 42;
\`\`\`
`;

      const segments = splitMarkdownTokens(markdown);
      expect(segments).toHaveLength(3);
      expect(segments[0].type).toBe("markdown");
      expect(segments[1].type).toBe("mermaid");
      expect(segments[2].type).toBe("markdown");

      const firstSeg = segments[0] as Extract<RawSegment, { type: "markdown" }>;
      const rawHtml1 = marked.parser(firstSeg.tokens);
      const cleanHtml1 = sanitizeMarkdownHtml(rawHtml1);
      expect(cleanHtml1).toContain('<span class="katex">');
      expect(cleanHtml1).toContain("<math");

      const thirdSeg = segments[2] as Extract<RawSegment, { type: "markdown" }>;
      const rawHtml3 = marked.parser(thirdSeg.tokens);
      const cleanHtml3 = sanitizeMarkdownHtml(rawHtml3);
      expect(cleanHtml3).toContain("katex-display-wrapper");
      expect(cleanHtml3).toContain('<span class="katex-display">');
      expect(cleanHtml3).toContain("<math");
      expect(cleanHtml3).toContain("<pre><code");
    });

    it("preserves MathML tags and aria attributes through sanitizeMarkdownHtml", () => {
      const rawHtml = marked.parse("$$\\int_0^\\infty e^{-x} dx = 1$$") as string;
      const cleanHtml = sanitizeMarkdownHtml(rawHtml);

      // Verify KaTeX display wrapper and katex span
      expect(cleanHtml).toContain("katex-display-wrapper");
      expect(cleanHtml).toContain('<span class="katex">');
      expect(cleanHtml).toContain('aria-hidden="true"');

      // Verify MathML and semantic annotation tags are retained
      expect(cleanHtml).toContain("<math");
      expect(cleanHtml).toContain("<semantics>");
      expect(cleanHtml).toContain("<annotation");
      expect(cleanHtml).toContain("</annotation>");
      expect(cleanHtml).toContain("</semantics>");
      expect(cleanHtml).toContain("</math>");
    });

    it("correctly parses and sanitizes formulas in tables", () => {
      const tableMarkdown = `
| 变量 | 表达式 | 说明 |
| :--- | :--- | :--- |
| 动能 | $E_k = \\frac{1}{2}mv^2$ | 经典力学公式 |
| 势能 | \\(E_p = mgh\\) | 重力势能 |
| 价格 | \\$100 | 非公式纯文本 |
`;

      const segments = splitMarkdownTokens(tableMarkdown);
      expect(segments).toHaveLength(1);

      const seg = segments[0] as Extract<RawSegment, { type: "markdown" }>;
      const html = sanitizeMarkdownHtml(marked.parser(seg.tokens));

      expect(html).toContain("<table>");
      expect(html).toContain("变量</th>");
      expect(html).toContain("动能</td>");
      expect(html).toContain('<span class="katex">');
      expect(html).toContain("<math");
      expect(html).toContain("$100");
    });

    it("correctly parses and sanitizes formulas in list items", () => {
      const listMarkdown = `
- 动能：$E_k = \\frac{1}{2}mv^2$ 经典力学
- 势能：\\(E_p = mgh\\) 重力势能
- 价格：\\$100 非公式纯文本

1. 第一项 $a+b$
2. 第二项 \\(c+d\\)
`;
      const segments = splitMarkdownTokens(listMarkdown);
      expect(segments).toHaveLength(1);

      const seg = segments[0] as Extract<RawSegment, { type: "markdown" }>;
      const html = sanitizeMarkdownHtml(marked.parser(seg.tokens));

      expect(html).toContain("<ul>");
      expect(html).toContain("<ol>");
      expect(html).toContain('<span class="katex">');
      expect(html).toContain("<math");
      expect(html).toContain("$100");
    });
  });
});
