import DOMPurify, { type Config } from "dompurify";

export const MARKDOWN_DOMPURIFY_CONFIG = {
  USE_PROFILES: { html: true, mathMl: true, svg: true },
  ADD_TAGS: ["annotation", "semantics"],
  ADD_ATTR: ["aria-hidden"],
} satisfies Config;

export function sanitizeMarkdownHtml(rawHtml: string): string {
  return DOMPurify.sanitize(rawHtml, MARKDOWN_DOMPURIFY_CONFIG);
}
