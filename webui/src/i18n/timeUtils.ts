import { t as defaultT } from "./index";

export type TranslateFn = (key: string, values?: Record<string, any>) => string;

/**
 * Parses any supported timestamp (epoch in seconds, epoch in ms, ISO string, Date object)
 * into numeric epoch milliseconds. Returns NaN if invalid.
 */
export function parseTimestampToMs(timestamp?: number | string | Date): number {
  if (timestamp === undefined || timestamp === null) return NaN;
  if (timestamp instanceof Date) return timestamp.getTime();
  if (typeof timestamp === "number") {
    return timestamp < 1e11 ? timestamp * 1000 : timestamp;
  }
  const num = Number(timestamp);
  if (!isNaN(num)) {
    return num < 1e11 ? num * 1000 : num;
  }
  const parsed = Date.parse(timestamp);
  return isNaN(parsed) ? NaN : parsed;
}

/**
 * Formats a timestamp into an internationalized relative time string.
 * Supports both past relative time ("just now", "5m ago") and future relative time ("in 2h 15m").
 */
export function formatRelativeTime(
  timestamp?: number | string | Date,
  customT?: TranslateFn,
): string {
  const epochMs = parseTimestampToMs(timestamp);
  if (isNaN(epochMs)) return "";

  const t: TranslateFn = customT || ((k: string, v?: Record<string, any>) => defaultT(k, v || {}));
  const now = Date.now();
  const diff = epochMs - now;

  if (diff >= 0) {
    // Future timestamp
    const futureSec = Math.floor(diff / 1000);
    if (futureSec <= 0) {
      return t("time.resetsNow");
    }
    const hours = Math.floor(futureSec / 3600);
    const minutes = Math.floor((futureSec % 3600) / 60);

    if (hours >= 24) {
      const days = Math.floor(hours / 24);
      return t("time.inDays", { n: days, h: hours % 24 });
    }
    if (hours > 0) {
      return t("time.inHours", { n: hours, m: minutes });
    }
    return t("time.inMinutes", { n: Math.max(1, minutes) });
  }

  // Past timestamp
  const pastSec = Math.floor(-diff / 1000);
  if (pastSec < 60) {
    return t("time.justNow");
  }
  if (pastSec < 3600) {
    return t("time.minutesAgo", { n: Math.max(1, Math.floor(pastSec / 60)) });
  }
  if (pastSec < 86400) {
    return t("time.hoursAgo", { n: Math.max(1, Math.floor(pastSec / 3600)) });
  }
  return t("time.daysAgo", { n: Math.max(1, Math.floor(pastSec / 86400)) });
}

/**
 * Formats quota reset relative timestamp (e.g. "(in 2h 15m)" or "(resets now)").
 * Returns empty string if timestamp is absent or invalid.
 */
export function formatQuotaResetRelative(timestamp?: number, customT?: TranslateFn): string {
  if (!timestamp) return "";
  const rel = formatRelativeTime(timestamp, customT);
  return rel ? `(${rel})` : "";
}
