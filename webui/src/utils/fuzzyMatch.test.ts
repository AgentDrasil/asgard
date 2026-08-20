import { describe, it, expect } from "vitest";
import { fuzzyMatch, filterCommands } from "./fuzzyMatch";
import type { CommandItem } from "../types";

describe("fuzzyMatch", () => {
  it("returns match with score 0 for empty query", () => {
    const res = fuzzyMatch("", "Switch to chat view");
    expect(res.matches).toBe(true);
    expect(res.score).toBe(0);
  });

  it("returns matches: false when target does not contain characters", () => {
    const res = fuzzyMatch("xyz", "Switch to chat view");
    expect(res.matches).toBe(false);
  });

  it("handles case-insensitive matching", () => {
    const res = fuzzyMatch("CHAT", "Switch to chat view");
    expect(res.matches).toBe(true);
    expect(res.score).toBeGreaterThan(0);
  });

  it("matches subsequence / abbreviations", () => {
    const res = fuzzyMatch("tc", "Toggle chat");
    expect(res.matches).toBe(true);
    expect(res.score).toBeGreaterThan(0);
  });

  it("scores exact match higher than prefix match", () => {
    const exact = fuzzyMatch("Terminal", "Terminal");
    const prefix = fuzzyMatch("Terminal", "Terminal Settings");
    expect(exact.score).toBeGreaterThan(prefix.score);
  });

  it("scores prefix match higher than distant subsequence match", () => {
    const prefix = fuzzyMatch("git", "Git: Commit changes");
    const distant = fuzzyMatch("git", "Organize imports and format");
    expect(prefix.score).toBeGreaterThan(distant.score);
  });

  it("scores consecutive subsequence matches higher than non-consecutive matches", () => {
    const consecutive = fuzzyMatch("git", "git status");
    const nonConsecutive = fuzzyMatch("git", "go into terminal");
    expect(consecutive.score).toBeGreaterThan(nonConsecutive.score);
  });
});

describe("filterCommands", () => {
  const dummyAction = () => {};

  const sampleCommands: CommandItem[] = [
    { id: "1", title: "Switch to chat view", category: "View", action: dummyAction },
    { id: "2", title: "Toggle terminal", category: "Terminal", action: dummyAction },
    {
      id: "3",
      title: "Toggle left panel",
      category: "View",
      keywords: ["sidebar", "explorer"],
      action: dummyAction,
    },
    {
      id: "4",
      title: "Open file search",
      category: "File",
      shortcut: "Ctrl+P",
      action: dummyAction,
    },
    { id: "5", title: "Git: Commit changes", category: "Git", action: dummyAction },
  ];

  it("returns shallow copy of all commands when query is empty or whitespace", () => {
    const emptyRes = filterCommands(sampleCommands, "");
    expect(emptyRes).toEqual(sampleCommands);
    expect(emptyRes).not.toBe(sampleCommands);

    const wsRes = filterCommands(sampleCommands, "   ");
    expect(wsRes).toEqual(sampleCommands);
  });

  it("filters case-insensitively", () => {
    const res = filterCommands(sampleCommands, "CHAT");
    expect(res).toHaveLength(1);
    expect(res[0].id).toBe("1");
  });

  it("filters by abbreviation or subsequence", () => {
    const res = filterCommands(sampleCommands, "tt");
    expect(res.map((c) => c.id)).toContain("2");
  });

  it("matches against keywords", () => {
    const res = filterCommands(sampleCommands, "sidebar");
    expect(res).toHaveLength(1);
    expect(res[0].id).toBe("3");
  });

  it("returns empty array when nothing matches", () => {
    const res = filterCommands(sampleCommands, "zzzznonexistent");
    expect(res).toHaveLength(0);
  });

  it("sorts results by relevance score and preserves stable order for ties", () => {
    const customList: CommandItem[] = [
      { id: "sub", title: "Open sub directory", action: dummyAction },
      { id: "tie1", title: "Open doc A", action: dummyAction },
      { id: "tie2", title: "Open doc B", action: dummyAction },
      { id: "exact", title: "Open", action: dummyAction },
      { id: "prefix", title: "Open file", action: dummyAction },
    ];
    const res = filterCommands(customList, "Open");
    expect(res[0].id).toBe("exact");
    expect(res[1].id).toBe("prefix");
    expect(res[2].id).toBe("tie1");
    expect(res[3].id).toBe("tie2");
    expect(res[4].id).toBe("sub");
  });
});
