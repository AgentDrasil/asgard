import { describe, it, expect } from "vitest";
import { humanfriendly, formatContextUsage, getContextColorClass, formatTimestamp } from "./format";

describe("humanfriendly format tests", () => {
  it("formats numbers smaller than 1024", () => {
    expect(humanfriendly(0)).toBe("0");
    expect(humanfriendly(500)).toBe("500");
    expect(humanfriendly(1023)).toBe("1023");
  });

  it("formats numbers in K range (1024 base)", () => {
    expect(humanfriendly(1024)).toBe("1.0K");
    expect(humanfriendly(20582)).toBe("20.1K");
    expect(humanfriendly(524288)).toBe("512.0K");
  });

  it("formats numbers in M range (1024*1024 base)", () => {
    expect(humanfriendly(1048576)).toBe("1.0M");
    expect(humanfriendly(2097152)).toBe("2.0M");
  });
});

describe("formatContextUsage tests", () => {
  it("formats percentage and token usage", () => {
    expect(formatContextUsage(20582, 1048576)).toBe("2% (20.1K / 1.0M)");
    expect(formatContextUsage(209715, 1048576)).toBe("20% (204.8K / 1.0M)");
  });
});

describe("getContextColorClass tests", () => {
  it("returns correct color classes based on threshold ratios", () => {
    // < 60%
    expect(getContextColorClass(500, 1000)).toBe("text-base-content/60");
    // >= 60% && < 80%
    expect(getContextColorClass(600, 1000)).toBe("text-warning font-semibold");
    expect(getContextColorClass(750, 1000)).toBe("text-warning font-semibold");
    // >= 80%
    expect(getContextColorClass(800, 1000)).toBe("text-error font-bold");
    expect(getContextColorClass(950, 1000)).toBe("text-error font-bold");
  });
});

describe("formatTimestamp tests", () => {
  it("returns empty string for missing or invalid timestamps", () => {
    expect(formatTimestamp(undefined)).toBe("");
    expect(formatTimestamp(0)).toBe("");
    expect(formatTimestamp("invalid")).toBe("");
  });

  it("formats epoch timestamp into YYYY-MM-DD HH:mm:ss.TZ format", () => {
    const fixedDate = new Date("2026-08-19T03:45:21Z");
    const formatted = formatTimestamp(fixedDate.getTime());
    expect(formatted).toMatch(/^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}\..+$/);
  });
});
