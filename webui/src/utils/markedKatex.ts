import katex from "katex";
import type { MarkedExtension, Tokens } from "marked";

export interface MarkedKatexOptions {
  /**
   * When true, invalid LaTeX is rendered as error text by KaTeX itself;
   * when false (default), KaTeX still renders errors gracefully and any
   * unexpected internal exception is caught by a renderer fallback.
   */
  throwOnError?: boolean;
}

interface KatexToken extends Tokens.Generic {
  text: string;
  displayMode: boolean;
}

// Inline $...$: single line, no whitespace right after opening / before closing.
const INLINE_DOLLAR_RULE = /^\$((?:\\.|[^$\\\n])+)\$/;
// Inline \(...\): single line.
const INLINE_PAREN_RULE = /^\\\(((?:\\.|[^\\\n])+?)\\\)/;
// Inline $$...$$ and \[...\]: multi-line allowed, trimmed content required.
const INLINE_DOUBLE_DOLLAR_RULE = /^\$\$([\s\S]+?)\$\$/;
const INLINE_BRACKET_RULE = /^\\\[([\s\S]+?)\\\]/;

// Block-level $$...$$ and \[...\]: anchored at the current position,
// consume one trailing newline so consecutive blocks do not emit extra spaces.
const BLOCK_DOUBLE_DOLLAR_RULE = /^\$\$([\s\S]+?)\$\$(?:\n|$)/;
const BLOCK_BRACKET_RULE = /^\\\[([\s\S]+?)\\\](?:\n|$)/;

function escapeHtml(value: string): string {
  return value
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;");
}

function renderKatex(tex: string, displayMode: boolean, throwOnError: boolean): string {
  try {
    return katex.renderToString(tex, { displayMode, throwOnError });
  } catch {
    // Last-resort fallback: keep the raw LaTeX visible instead of crashing.
    return `<span class="katex-error">${escapeHtml(tex)}</span>`;
  }
}

/**
 * Report the index of the next candidate inline delimiter ($, \( or \[).
 * Required by the marked Lexer to truncate `inlineText` tokens so formulas
 * embedded mid-sentence are tokenized instead of being swallowed as text.
 */
function inlineStart(src: string): number {
  return src.search(/\$|\\\(|\\\[/);
}

/**
 * Report the index of the next block-level delimiter ($$ or \[) that starts
 * its own line. Delimiters appearing mid-line are left to the inline
 * tokenizer so sentences like "前缀 $$x^2$$ 后缀" stay within one paragraph.
 */
function blockStart(src: string): number {
  const pattern = /\$\$|\\\[/g;
  let match: RegExpExecArray | null;
  while ((match = pattern.exec(src)) !== null) {
    if (match.index === 0) continue; // tempSrc[0] follows an arbitrary src char, never '\n' here

    let atLineStart = false;
    for (let i = match.index - 1; i >= 0; i--) {
      const ch = src[i];
      if (ch === "\n") {
        atLineStart = true;
        break;
      }
      if (ch !== " " && ch !== "\t") {
        break;
      }
    }
    if (atLineStart) return match.index;
  }
  return -1;
}

function inlineTokenizer(src: string): KatexToken | undefined {
  let match = INLINE_DOUBLE_DOLLAR_RULE.exec(src);
  if (match) {
    const content = match[1].trim();
    if (content.length > 0) {
      return { type: "inlineKatex", raw: match[0], text: content, displayMode: true };
    }
  }

  match = INLINE_PAREN_RULE.exec(src);
  if (match && match[1].trim().length > 0) {
    return { type: "inlineKatex", raw: match[0], text: match[1], displayMode: false };
  }

  match = INLINE_BRACKET_RULE.exec(src);
  if (match && match[1].trim().length > 0) {
    return { type: "inlineKatex", raw: match[0], text: match[1], displayMode: true };
  }

  match = INLINE_DOLLAR_RULE.exec(src);
  if (match) {
    const content = match[1];
    // Currency guard: "$100", "$ x $" or trailing-space "$x $" stay plain text.
    if (!/^\s|\s$/.test(content)) {
      return { type: "inlineKatex", raw: match[0], text: content, displayMode: false };
    }
  }

  return undefined;
}

function blockTokenizer(src: string): KatexToken | undefined {
  let match = BLOCK_DOUBLE_DOLLAR_RULE.exec(src);
  if (match) {
    const content = match[1].trim();
    if (content.length > 0) {
      return { type: "blockKatex", raw: match[0], text: content, displayMode: true };
    }
  }

  match = BLOCK_BRACKET_RULE.exec(src);
  if (match) {
    const content = match[1].trim();
    if (content.length > 0) {
      return { type: "blockKatex", raw: match[0], text: content, displayMode: true };
    }
  }

  return undefined;
}

export function markedKatex(options: MarkedKatexOptions = {}): MarkedExtension {
  const throwOnError = options.throwOnError ?? false;

  return {
    extensions: [
      {
        name: "inlineKatex",
        level: "inline",
        start: inlineStart,
        tokenizer: inlineTokenizer,
        renderer(token) {
          const katexToken = token as unknown as KatexToken;
          return renderKatex(katexToken.text, katexToken.displayMode, throwOnError);
        },
      },
      {
        name: "blockKatex",
        level: "block",
        start: blockStart,
        tokenizer: blockTokenizer,
        renderer(token) {
          const katexToken = token as unknown as KatexToken;
          const html = renderKatex(katexToken.text, katexToken.displayMode, throwOnError);
          return `<div class="katex-display-wrapper">${html}</div>`;
        },
      },
    ],
  };
}
