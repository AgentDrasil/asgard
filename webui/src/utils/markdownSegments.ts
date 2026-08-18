import { marked, type Token, type TokensList } from "marked";

marked.setOptions({
  gfm: true,
  breaks: true,
});

export type RawSegment =
  | { type: "markdown"; tokens: Token[]; links?: TokensList["links"] }
  | { type: "mermaid"; code: string };

export function isMermaidToken(
  token: Token,
): token is Token & { type: "code"; text: string; lang: string } {
  return (
    token.type === "code" &&
    typeof (token as { lang?: string }).lang === "string" &&
    (token as { lang: string }).lang.trim().toLowerCase() === "mermaid"
  );
}

export function extractNestedMermaid(token: Token): string[] {
  const mermaid: string[] = [];
  const tokenObj = token as unknown as {
    tokens?: Token[];
    items?: Array<{ tokens?: Token[]; items?: unknown[] }>;
  };

  const tokenArrays: Token[][] = [];

  if (Array.isArray(tokenObj.tokens)) {
    tokenArrays.push(tokenObj.tokens);
  }

  if (Array.isArray(tokenObj.items)) {
    for (const item of tokenObj.items) {
      if (Array.isArray(item.tokens)) {
        tokenArrays.push(item.tokens);
      }
      if (Array.isArray(item.items)) {
        const nested = extractNestedMermaid(item as unknown as Token);
        mermaid.push(...nested);
      }
    }
  }

  for (const arr of tokenArrays) {
    for (let i = 0; i < arr.length; i++) {
      const t = arr[i];
      if (!t) continue;
      if (isMermaidToken(t)) {
        mermaid.push(t.text);
        arr.splice(i, 1);
        i--;
      } else {
        const nested = extractNestedMermaid(t);
        mermaid.push(...nested);
      }
    }
  }

  return mermaid;
}

export function splitMarkdownTokens(content: string): RawSegment[] {
  if (!content) return [];

  const tokens = marked.lexer(content);
  const segments: RawSegment[] = [];
  let currentTokens: Token[] = [];

  const flushMarkdown = () => {
    if (currentTokens.length > 0) {
      segments.push({
        type: "markdown",
        tokens: currentTokens,
        links: tokens.links,
      });
      currentTokens = [];
    }
  };

  for (const token of tokens) {
    if (isMermaidToken(token)) {
      flushMarkdown();
      segments.push({
        type: "mermaid",
        code: token.text,
      });
    } else {
      const extractedMermaid = extractNestedMermaid(token);
      currentTokens.push(token);
      if (extractedMermaid.length > 0) {
        flushMarkdown();
        for (const code of extractedMermaid) {
          segments.push({
            type: "mermaid",
            code,
          });
        }
      }
    }
  }

  flushMarkdown();
  return segments;
}
