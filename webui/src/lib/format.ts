/**
 * Formats a number into a human-friendly representation with base 1024.
 * e.g., 500 -> "500", 20600 -> "20.1K", 1048576 -> "1.0M".
 */
export function humanfriendly(num: number): string {
  if (num < 0) return "0";
  if (num < 1024) {
    return num.toString();
  }
  if (num < 1024 * 1024) {
    const k = num / 1024;
    return `${k.toFixed(1)}K`;
  }
  const m = num / (1024 * 1024);
  return `${m.toFixed(1)}M`;
}

/**
 * Formats context usage string e.g. "20% (20.1K / 1.0M)".
 */
export function formatContextUsage(inputTokens: number, maxTokens: number): string {
  if (!maxTokens || maxTokens <= 0) return "";
  const pct = Math.round((inputTokens / maxTokens) * 100);
  return `${pct}% (${humanfriendly(inputTokens)} / ${humanfriendly(maxTokens)})`;
}

/**
 * Returns Tailwind text color class based on context usage ratio:
 * - < 60%: default color (text-base-content/60)
 * - >= 60% && < 80%: yellow (text-warning font-semibold)
 * - >= 80%: red (text-error font-bold)
 */
export function getContextColorClass(inputTokens: number, maxTokens: number): string {
  if (!maxTokens || maxTokens <= 0) return "text-base-content/60";
  const ratio = inputTokens / maxTokens;
  if (ratio >= 0.8) {
    return "text-error font-bold";
  }
  if (ratio >= 0.6) {
    return "text-warning font-semibold";
  }
  return "text-base-content/60";
}
