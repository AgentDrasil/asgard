import { Marked } from "marked";
import { describe, it, expect } from "vitest";
import { markedKatex } from "./markedKatex";

function createMarked() {
  const marked = new Marked();
  marked.setOptions({ gfm: true, breaks: true });
  marked.use(markedKatex());
  return marked;
}

function findTokens(tokens: ReturnType<Marked["lexer"]>, type: string): any[] {
  const found: any[] = [];
  const walk = (list: typeof tokens) => {
    for (const token of list) {
      if (token.type === type) {
        found.push(token);
      }
      const nested = (token as unknown as { tokens?: typeof tokens }).tokens;
      if (Array.isArray(nested)) {
        walk(nested);
      }
      const items = (
        token as unknown as { items?: Array<{ tokens?: typeof tokens; items?: unknown[] }> }
      ).items;
      if (Array.isArray(items)) {
        for (const item of items) {
          if (Array.isArray(item.tokens)) walk(item.tokens);
        }
      }
    }
  };
  walk(tokens);
  return found;
}

describe("markedKatex", () => {
  const marked = createMarked();

  describe("inline formulas embedded mid-sentence", () => {
    it("parses $...$ inside a sentence", () => {
      const tokens = marked.lexer("质量能量关系 $E=mc^2$ 由爱因斯坦提出");
      const katex = findTokens(tokens, "inlineKatex");
      expect(katex).toHaveLength(1);
      expect(katex[0].text).toBe("E=mc^2");
      expect(katex[0].displayMode).toBe(false);
      const html = marked.parse("质量能量关系 $E=mc^2$ 由爱因斯坦提出") as string;
      expect(html).toContain('class="katex"');
    });

    it("parses \\(...\\) inside a sentence", () => {
      const html = marked.parse("先验概率 \\(P(A|B)\\) 定义如下") as string;
      expect(html).toContain('class="katex"');
      const tokens = marked.lexer("先验概率 \\(P(A|B)\\) 定义如下");
      const paragraphs = findTokens(tokens, "paragraph");
      expect(paragraphs).toHaveLength(1);
    });

    it("parses inline $$...$$ embedded in a sentence without splitting blocks", () => {
      const tokens = marked.lexer("质量 $$x^2$$ 密度");
      expect(findTokens(tokens, "blockKatex")).toHaveLength(0);
      const katex = findTokens(tokens, "inlineKatex");
      expect(katex).toHaveLength(1);
      expect(katex[0].text).toBe("x^2");
      expect(katex[0].displayMode).toBe(true);
    });

    it("parses inline \\[...\\] embedded in a sentence", () => {
      const tokens = marked.lexer("区间 \\[y^2\\] 结束");
      expect(findTokens(tokens, "blockKatex")).toHaveLength(0);
      const katex = findTokens(tokens, "inlineKatex");
      expect(katex).toHaveLength(1);
      expect(katex[0].displayMode).toBe(true);
    });

    it("renders mid-sentence $$..$$ inline without injecting <br>", () => {
      for (const source of [
        "句子 $$x^2$$ 结束",
        "若$$x>1$$则不成立",
        "a$$x^2$$b",
        "令 $$x$$ 为实数",
        "a $$x^2$$ b",
        "1 $$x$$ 件",
      ]) {
        const html = marked.parse(source) as string;
        expect(html).not.toContain("<br>");
        expect(html).toMatch(/<p>[^<]*<span class="katex-display">/);
      }
    });

    it("pairs $$..$$ and $..$ correctly when mixed in one sentence", () => {
      const source = "句子 $$x^2$$ 和 $y$ 共存";
      const html = marked.parse(source) as string;
      expect((html.match(/class="katex"/g) || []).length).toBe(2);
      expect(html).not.toContain("<br>");
    });
  });

  describe("codespan adjacent to formula", () => {
    it("does not swallow formula following a codespan", () => {
      const source = "计算 `f(x)` 可得 $x^2+1$ 个解";
      const tokens = marked.lexer(source);
      const codespans = findTokens(tokens, "codespan");
      expect(codespans).toHaveLength(1);
      expect(codespans[0].text).toBe("f(x)");
      const katex = findTokens(tokens, "inlineKatex");
      expect(katex).toHaveLength(1);
      expect(katex[0].text).toBe("x^2+1");
    });
  });

  describe("block formulas", () => {
    it("parses single-line $$...$$ as display block", () => {
      const tokens = marked.lexer("\n$$\\sum_{i=1}^n i$$\n");
      const blocks = findTokens(tokens, "blockKatex");
      expect(blocks).toHaveLength(1);
      expect(blocks[0].text).toBe("\\sum_{i=1}^n i");
      expect(blocks[0].displayMode).toBe(true);
      const html = marked.parse("\n$$\\sum_{i=1}^n i$$\n") as string;
      expect(html).toContain("katex-display-wrapper");
      expect(html).toContain("katex-display");
    });

    it("parses multi-line $$...$$ as display block", () => {
      const source = "$$\n\\sum_{i=1}^n i\n$$\n后续段落";
      const tokens = marked.lexer(source);
      const blocks = findTokens(tokens, "blockKatex");
      expect(blocks).toHaveLength(1);
      expect(blocks[0].text).toBe("\\sum_{i=1}^n i");
    });

    it("parses \\[...\\] on its own line as display block", () => {
      const source = "\\[\n\\int_0^\\infty e^{-x} dx\n\\]\n";
      const html = marked.parse(source) as string;
      expect(html).toContain("katex-display-wrapper");
      const tokens = marked.lexer(source);
      expect(findTokens(tokens, "blockKatex")).toHaveLength(1);
    });

    it("parses indented block $$...$$ after newline as display block", () => {
      const source = "正文\n  $$x$$\n正文";
      const tokens = marked.lexer(source);
      const blocks = findTokens(tokens, "blockKatex");
      expect(blocks).toHaveLength(1);
      expect(blocks[0].text).toBe("x");
    });
  });

  describe("currency symbols and escapes", () => {
    it("keeps escaped price and parses the real formula", () => {
      const source = "价格 \\$5 与公式 $x$ 相邻";
      const tokens = marked.lexer(source);
      const katex = findTokens(tokens, "inlineKatex");
      expect(katex).toHaveLength(1);
      expect(katex[0].text).toBe("x");
      const html = marked.parse(source) as string;
      expect(html).toContain("$5");
    });

    it("does not treat currency amounts as delimiters", () => {
      const source = "支付 $100 购买 $x^2$ 件商品";
      const tokens = marked.lexer(source);
      const katex = findTokens(tokens, "inlineKatex");
      expect(katex).toHaveLength(1);
      expect(katex[0].text).toBe("x^2");
      const html = marked.parse(source) as string;
      expect(html).toContain("$100 购买");
    });

    it("leaves unclosed dollar amounts as plain text", () => {
      const html = marked.parse("$49.99") as string;
      expect(html).toContain("$49.99");
      expect(html).not.toContain('class="katex"');
    });

    it("renders escaped dollars as literal text without math parsing", () => {
      for (const source of ["\\$100", "\\$x+y\\$"]) {
        const html = marked.parse(source) as string;
        expect(html).not.toContain('class="katex"');
        expect(html.replace(/<[^>]+>/g, "")).toContain("$");
      }
    });
  });

  describe("delimiters inside code are isolated", () => {
    it("keeps $...$ literal inside codespan", () => {
      const source = "`$x$`";
      const tokens = marked.lexer(source);
      expect(findTokens(tokens, "inlineKatex")).toHaveLength(0);
      const codespans = findTokens(tokens, "codespan");
      expect(codespans).toHaveLength(1);
      expect(codespans[0].text).toBe("$x$");
    });

    it("keeps $$...$$ literal inside fenced code block", () => {
      const source = "```math\n$$x$$\n```";
      const tokens = marked.lexer(source);
      expect(findTokens(tokens, "blockKatex")).toHaveLength(0);
      expect(findTokens(tokens, "inlineKatex")).toHaveLength(0);
      const code = findTokens(tokens, "code");
      expect(code).toHaveLength(1);
      expect((code[0] as { lang: string }).lang).toBe("math");
      expect((code[0] as { text: string }).text).toContain("$$x$$");
    });
  });

  describe("streaming tolerance for unclosed delimiters", () => {
    it("keeps unclosed single dollar as plain text without throwing", () => {
      const html = marked.parse("$x + y") as string;
      expect(html).toContain("$x + y");
      expect(html).not.toContain('class="katex"');
    });

    it("keeps unclosed double dollars as plain text without throwing", () => {
      const html = marked.parse("$$\\sum") as string;
      expect(html).not.toContain('class="katex"');
      expect(html).not.toContain("katex-display-wrapper");
    });

    it("keeps unclosed bracket delimiter as plain text", () => {
      const html = marked.parse("\\[y^2") as string;
      expect(html).not.toContain('class="katex"');
      expect(html).not.toContain("katex-display-wrapper");
    });
  });

  describe("invalid latex degrades gracefully", () => {
    it("renders error-styled katex markup instead of throwing by default", () => {
      const source = "$\\unknownMacro{abc}$";
      let html: string | null = null;
      expect(() => {
        html = marked.parse(source) as string;
      }).not.toThrow();
      expect(html).toContain('class="katex"');
      expect(html).toContain("#cc0000");
    });

    it("falls back to katex-error markup when katex throws", () => {
      const strictMarked = new Marked();
      strictMarked.use(markedKatex({ throwOnError: true }));
      const html = strictMarked.parse("$\\unknownMacro{abc}$") as string;
      expect(html).toContain("katex-error");
      expect(html).toContain("\\unknownMacro{abc}");
    });
  });
});
