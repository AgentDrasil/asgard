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
 * Formats byte size into human readable string with units (B, KB, MB, GB).
 * e.g., 500 -> "500 B", 12697 -> "12.4 KB", 1048576 -> "1.0 MB".
 */
export function formatFileSize(bytes?: number): string {
  if (bytes === undefined || bytes === null || isNaN(bytes) || bytes < 0) return "0 B";
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) {
    return `${(bytes / 1024).toFixed(1)} KB`;
  }
  if (bytes < 1024 * 1024 * 1024) {
    return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
  }
  return `${(bytes / (1024 * 1024 * 1024)).toFixed(1)} GB`;
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

/**
 * Formats a timestamp (epoch ms or s) into "YYYY-MM-DD HH:mm:ss.TZ".
 * e.g. "2026-08-19 03:45:21.EDT" or "2026-08-19 03:45:21.UTC"
 */
export function formatTimestamp(timestamp?: number | string | Date): string {
  if (!timestamp) return "";
  let date: Date;
  if (timestamp instanceof Date) {
    date = timestamp;
  } else if (typeof timestamp === "number") {
    // If timestamp is in seconds (< 10^11), convert to milliseconds
    date = new Date(timestamp < 1e11 ? timestamp * 1000 : timestamp);
  } else {
    const num = Number(timestamp);
    if (!isNaN(num)) {
      date = new Date(num < 1e11 ? num * 1000 : num);
    } else {
      date = new Date(timestamp);
    }
  }

  if (isNaN(date.getTime())) return "";

  const pad = (n: number) => n.toString().padStart(2, "0");
  const year = date.getFullYear();
  const month = pad(date.getMonth() + 1);
  const day = pad(date.getDate());
  const hours = pad(date.getHours());
  const minutes = pad(date.getMinutes());
  const seconds = pad(date.getSeconds());

  let tz = "";
  try {
    const parts = new Intl.DateTimeFormat(undefined, { timeZoneName: "short" }).formatToParts(date);
    const tzPart = parts.find((p) => p.type === "timeZoneName");
    if (tzPart && tzPart.value) {
      tz = tzPart.value;
    }
  } catch {
    // fallback
  }

  if (!tz) {
    const offset = -date.getTimezoneOffset();
    const sign = offset >= 0 ? "+" : "-";
    const absOffset = Math.abs(offset);
    const offsetHours = pad(Math.floor(absOffset / 60));
    const offsetMins = pad(absOffset % 60);
    tz = `UTC${sign}${offsetHours}:${offsetMins}`;
  }

  return `${year}-${month}-${day} ${hours}:${minutes}:${seconds}.${tz}`;
}
