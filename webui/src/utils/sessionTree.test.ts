import { describe, it, expect } from "vitest";
import { flattenSessions, rootOf, subagentLabel } from "./sessionTree";
import type { ChatSession } from "../types";

const mk = (chatID: string, parentChatId?: string): ChatSession => ({
  chatID,
  title: chatID,
  currentAgent: "a",
  runDir: "/ws",
  parentChatId,
});

describe("sessionTree", () => {
  it("nests children under their parent, depth first", () => {
    const rows = flattenSessions([mk("c1", "p"), mk("p"), mk("g1", "c1"), mk("other")]);
    expect(rows.map((r) => [r.session.chatID, r.depth, r.childCount])).toEqual([
      ["p", 0, 1],
      ["c1", 1, 1],
      ["g1", 2, 0],
      ["other", 0, 0],
    ]);
  });

  it("hides the children of a collapsed session", () => {
    const rows = flattenSessions([mk("p"), mk("c1", "p"), mk("g1", "c1")], { p: true });
    expect(rows.map((r) => r.session.chatID)).toEqual(["p"]);
    expect(rows[0]).toMatchObject({ collapsed: true, childCount: 1 });
  });

  it("keeps a child whose parent is not listed as a root", () => {
    const rows = flattenSessions([mk("c1", "gone")]);
    expect(rows).toHaveLength(1);
    expect(rows[0].depth).toBe(0);
  });

  it("survives a parent cycle without looping", () => {
    const rows = flattenSessions([mk("a", "b"), mk("b", "a"), mk("z")]);
    expect(rows.map((r) => r.session.chatID)).toEqual(["z"]);
  });

  it("finds the root of a session", () => {
    const all = [mk("p"), mk("c", "p"), mk("g", "c")];
    const byId = new Map(all.map((s) => [s.chatID, s]));
    expect(rootOf(all[2], byId).chatID).toBe("p");
  });

  it("labels a subagent by label, then title", () => {
    expect(subagentLabel({ ...mk("c"), spawnLabel: "review" }, "x")).toBe("review");
    expect(subagentLabel({ ...mk("c"), title: "" }, "x")).toBe("x");
  });
});
