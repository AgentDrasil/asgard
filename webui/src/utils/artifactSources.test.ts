import { describe, it, expect } from "vitest";
import {
  isSharedArtifactPath,
  mergeSubagentArtifacts,
  parseArtifactKey,
  sourceName,
  subagentArtifactEntries,
  subagentArtifactKey,
} from "./artifactSources";
import type { SubagentArtifacts } from "../types";

describe("artifactSources", () => {
  it("keys per-chat artifacts by chat and /shared artifacts by path", () => {
    expect(subagentArtifactKey("c1", "/tmp/a.md")).toBe("subagent:c1:/tmp/a.md");
    expect(subagentArtifactKey("c1", "/session/n.md")).toBe("subagent:c1:/session/n.md");
    expect(subagentArtifactKey("c1", "/shared/r.md")).toBe("/shared/r.md");
    expect(isSharedArtifactPath("/shared")).toBe(true);
    expect(isSharedArtifactPath("/sharedx/a")).toBe(false);
  });

  it("parses keys back into session id and path", () => {
    expect(parseArtifactKey("subagent:c1:/tmp/a:b.md")).toEqual({
      sessionId: "c1",
      path: "/tmp/a:b.md",
    });
    expect(parseArtifactKey("/tmp/a.md")).toEqual({ sessionId: null, path: "/tmp/a.md" });
    expect(parseArtifactKey("subagent:broken")).toEqual({
      sessionId: null,
      path: "subagent:broken",
    });
    expect(parseArtifactKey(null)).toEqual({ sessionId: null, path: "" });
  });

  it("names a source by label, agent, then short id", () => {
    expect(sourceName({ label: "plan", agent: "a", chatId: "abcdefghij" })).toBe("plan");
    expect(sourceName({ label: "", agent: "coder", chatId: "abcdefghij" })).toBe("coder");
    expect(sourceName({ label: "", chatId: "abcdefghij" })).toBe("abcdefgh");
  });

  it("flattens descendants' artifacts, deduplicating /shared and own entries", () => {
    const items: SubagentArtifacts[] = [
      { chatId: "c1", label: "one", agent: "coder", artifacts: ["/tmp/a", "/shared/x", "/tmp/a"] },
      { chatId: "c2", label: "", agent: "tester", artifacts: ["/tmp/a", "/shared/x", "/shared/y"] },
    ];
    const { keys, sources } = subagentArtifactEntries(items, ["/shared/y"]);
    expect(keys).toEqual(["subagent:c1:/tmp/a", "/shared/x", "subagent:c2:/tmp/a"]);
    expect(sources["/shared/x"]).toEqual({ chatId: "c1", label: "one", agent: "coder" });
    expect(sources["subagent:c2:/tmp/a"].label).toBe("tester");
    expect(subagentArtifactEntries(undefined).keys).toEqual([]);
  });

  it("merges subagent_artifact events", () => {
    let list = mergeSubagentArtifacts(undefined, {
      id: "c1",
      label: "one",
      agent: "coder",
      artifacts: ["/tmp/a", "/shared/x"],
    });
    expect(list).toEqual([
      { chatId: "c1", label: "one", agent: "coder", artifacts: ["/tmp/a", "/shared/x"] },
    ]);
    list = mergeSubagentArtifacts(list, { id: "c1", artifacts: ["/tmp/a", "/tmp/b"] });
    expect(list).toHaveLength(1);
    expect(list[0].artifacts).toEqual(["/tmp/a", "/shared/x", "/tmp/b"]);
    expect(list[0].label).toBe("one");
    list = mergeSubagentArtifacts(list, { id: "c2", label: "two", artifacts: ["/tmp/z"] });
    expect(list.map((s) => s.chatId)).toEqual(["c1", "c2"]);
    // Malformed payloads change nothing.
    expect(mergeSubagentArtifacts(list, { id: "", artifacts: ["/x"] })).toBe(list);
    expect(mergeSubagentArtifacts(list, { id: "c1", artifacts: [] })).toBe(list);
    expect(mergeSubagentArtifacts(list, undefined)).toBe(list);
  });
});
