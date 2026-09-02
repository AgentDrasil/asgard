/**
 * Parses options from an ask_user message content.
 * Expected format in content: "Options: Option1 / Option2 / ..."
 *
 * Options are separated by " / " (slash surrounded by spaces) so labels may
 * contain bare slashes, e.g. model IDs like "zai-coding-plan/glm-5.3-flash/high".
 */
export function parseOptions(content: string): string[] {
  if (!content) return [];
  const match = content.match(/Options:\s*([^\n\r]+)/i);
  if (!match || !match[1]) return [];
  return match[1]
    .split(" / ")
    .map((s) => s.trim())
    .filter(Boolean);
}
