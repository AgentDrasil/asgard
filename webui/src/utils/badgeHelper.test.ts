import { describe, it, expect } from "vitest";
import { resolveBadgeClass } from "./badgeHelper";

describe("badgeHelper", () => {
  it("resolves financial rating badges", () => {
    expect(resolveBadgeClass("AAA")).toContain("badge-tag-emerald");
    expect(resolveBadgeClass("A+")).toContain("badge-tag-emerald");
    expect(resolveBadgeClass("BBB")).toContain("badge-tag-teal");
    expect(resolveBadgeClass("BB")).toContain("badge-tag-amber");
    expect(resolveBadgeClass("D")).toContain("badge-tag-rose");
  });

  it("resolves tax regime badges", () => {
    expect(resolveBadgeClass("TFSA")).toContain("badge-tag-emerald");
    expect(resolveBadgeClass("RRSP")).toContain("badge-tag-sky");
    expect(resolveBadgeClass("401k")).toContain("badge-tag-sky");
    expect(resolveBadgeClass("Taxable")).toContain("badge-tag-amber");
  });

  it("resolves asset class badges", () => {
    expect(resolveBadgeClass("US Equities")).toContain("badge-tag-emerald");
    expect(resolveBadgeClass("International Equities")).toContain("badge-tag-cyan");
    expect(resolveBadgeClass("Cash & Equivalents")).toContain("badge-tag-amber");
    expect(resolveBadgeClass("Digital Assets")).toContain("badge-tag-purple");
    expect(resolveBadgeClass("Fixed Income")).toContain("badge-tag-indigo");
  });

  it("resolves action / sentiment badges", () => {
    expect(resolveBadgeClass("Buy")).toContain("badge-tag-emerald");
    expect(resolveBadgeClass("Hold")).toContain("badge-tag-sky");
    expect(resolveBadgeClass("Sell")).toContain("badge-tag-amber");
    expect(resolveBadgeClass("Critical")).toContain("badge-tag-rose");
  });

  it("uses explicit badgeColorMap when provided", () => {
    const col = {
      badgeColorMap: {
        Special: "badge-tag-custom-green",
      },
    };
    expect(resolveBadgeClass("Special", col)).toBe("badge-tag-custom-green");
  });

  it("does not false positive on words containing substrings like VIRAL, MIRAGE, METHOD, SOIL", () => {
    expect(resolveBadgeClass("VIRAL")).toContain("badge-tag-slate");
    expect(resolveBadgeClass("MIRAGE")).toContain("badge-tag-slate");
    expect(resolveBadgeClass("METHOD")).toContain("badge-tag-slate");
    expect(resolveBadgeClass("SOIL")).toContain("badge-tag-slate");
    expect(resolveBadgeClass("Traditional IRA")).toContain("badge-tag-sky");
    expect(resolveBadgeClass("ETH-USD")).toContain("badge-tag-purple");
  });

  it("returns fallback for unknown text or null", () => {
    expect(resolveBadgeClass("")).toBe("badge-tag-slate");
    expect(resolveBadgeClass(null)).toBe("badge-tag-slate");
    expect(resolveBadgeClass("SomethingRandom123")).toContain("badge-tag-slate");
  });
});
