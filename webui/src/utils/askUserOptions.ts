/**
 * Parses options from an ask_user message content.
 * Expected format in content: "Options: Option1 / Option2 / ..."
 */
export function parseOptions(content: string): string[] {
  if (!content) return [];
  const match = content.match(/Options:\s*([^\n\r]+)/i);
  if (!match || !match[1]) return [];
  return match[1]
    .split("/")
    .map((s) => s.trim())
    .filter(Boolean);
}
