import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { formatRelativeTime, formatQuotaResetRelative, parseTimestampToMs } from "./timeUtils";
import { setLocale } from "./index";

describe("timeUtils", () => {
  const baseTime = 1700000000000; // Fixed timestamp ms

  beforeEach(() => {
    vi.useFakeTimers();
    vi.setSystemTime(baseTime);
    setLocale("en", false);
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("parses different timestamp formats correctly", () => {
    expect(parseTimestampToMs(1700000000)).toBe(1700000000000);
    expect(parseTimestampToMs(1700000000000)).toBe(1700000000000);
    expect(parseTimestampToMs(new Date(1700000000000))).toBe(1700000000000);
    expect(isNaN(parseTimestampToMs(undefined))).toBe(true);
    expect(isNaN(parseTimestampToMs("invalid"))).toBe(true);
  });

  it("formats past relative time in English", () => {
    setLocale("en", false);

    // 30 seconds ago
    expect(formatRelativeTime(baseTime - 30 * 1000)).toBe("just now");

    // 5 minutes ago
    expect(formatRelativeTime(baseTime - 5 * 60 * 1000)).toBe("5m ago");

    // 3 hours ago
    expect(formatRelativeTime(baseTime - 3 * 3600 * 1000)).toBe("3h ago");

    // 2 days ago
    expect(formatRelativeTime(baseTime - 2 * 86400 * 1000)).toBe("2d ago");
  });

  it("formats past relative time in Chinese", () => {
    setLocale("zh-CN", false);

    // 30 seconds ago
    expect(formatRelativeTime(baseTime - 30 * 1000)).toBe("刚刚");

    // 5 minutes ago
    expect(formatRelativeTime(baseTime - 5 * 60 * 1000)).toBe("5分钟前");

    // 3 hours ago
    expect(formatRelativeTime(baseTime - 3 * 3600 * 1000)).toBe("3小时前");

    // 2 days ago
    expect(formatRelativeTime(baseTime - 2 * 86400 * 1000)).toBe("2天前");
  });

  it("formats future relative time in English", () => {
    setLocale("en", false);

    // 0s diff
    expect(formatRelativeTime(baseTime)).toBe("resets now");

    // 15 minutes in future
    expect(formatRelativeTime(baseTime + 15 * 60 * 1000)).toBe("in 15m");

    // 2 hours 10 minutes in future
    expect(formatRelativeTime(baseTime + (2 * 3600 + 10 * 60) * 1000)).toBe("in 2h 10m");

    // 2 days 5 hours in future
    expect(formatRelativeTime(baseTime + (2 * 86400 + 5 * 3600) * 1000)).toBe("in 2d 5h");
  });

  it("formats future relative time in Chinese", () => {
    setLocale("zh-CN", false);

    // 0s diff
    expect(formatRelativeTime(baseTime)).toBe("立即重置");

    // 15 minutes in future
    expect(formatRelativeTime(baseTime + 15 * 60 * 1000)).toBe("15分钟后");

    // 2 hours 10 minutes in future
    expect(formatRelativeTime(baseTime + (2 * 3600 + 10 * 60) * 1000)).toBe("2小时10分钟后");

    // 2 days 5 hours in future
    expect(formatRelativeTime(baseTime + (2 * 86400 + 5 * 3600) * 1000)).toBe("2天5小时后");
  });

  it("formats quota reset relative time wrapper", () => {
    setLocale("en", false);
    expect(formatQuotaResetRelative(undefined)).toBe("");
    expect(formatQuotaResetRelative(Math.floor(baseTime / 1000) + 15 * 60)).toBe("(in 15m)");

    setLocale("zh-CN", false);
    expect(formatQuotaResetRelative(Math.floor(baseTime / 1000) + 15 * 60)).toBe("(15分钟后)");
  });
});
