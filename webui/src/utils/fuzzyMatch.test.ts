import { describe, it, expect } from "vitest";
import { filterCommands } from "./fuzzyMatch";
import type { CommandItem } from "../types";

describe("filterCommands", () => {
  const dummyAction = () => {};

  const sampleCommands: CommandItem[] = [
    { id: "1", title: "Switch to chat view", category: "View", action: dummyAction },
    { id: "2", title: "Toggle terminal", category: "Terminal", action: dummyAction },
    { id: "3", title: "Toggle left panel", category: "View", action: dummyAction },
    {
      id: "4",
      title: "Open file search",
      category: "File",
      shortcut: "Ctrl+P",
      action: dummyAction,
    },
    { id: "5", title: "Switch to vcs view", category: "View", action: dummyAction },
  ];

  it("returns shallow copy of all commands when query is empty or whitespace", () => {
    const emptyRes = filterCommands(sampleCommands, "");
    expect(emptyRes).toEqual(sampleCommands);
    expect(emptyRes).not.toBe(sampleCommands);

    const wsRes = filterCommands(sampleCommands, "   ");
    expect(wsRes).toEqual(sampleCommands);
  });

  it("filters case-insensitively and prioritizes title matches", () => {
    const res = filterCommands(sampleCommands, "CHAT");
    expect(res.length).toBeGreaterThanOrEqual(1);
    expect(res[0].id).toBe("1");
  });

  it("filters matching commands accurately", () => {
    const res = filterCommands(sampleCommands, "vcs");
    expect(res).toHaveLength(1);
    expect(res[0].id).toBe("5");
  });

  it("returns empty array when nothing matches", () => {
    const res = filterCommands(sampleCommands, "zzzznonexistent");
    expect(res).toHaveLength(0);
  });

  it("sorts results by relevance score", () => {
    const customList: CommandItem[] = [
      { id: "sub", title: "Open sub directory", action: dummyAction },
      { id: "exact", title: "Open", action: dummyAction },
      { id: "prefix", title: "Open file", action: dummyAction },
    ];
    const res = filterCommands(customList, "Open");
    expect(res[0].id).toBe("exact");
    expect(res[1].id).toBe("prefix");
    expect(res[2].id).toBe("sub");
  });
});
