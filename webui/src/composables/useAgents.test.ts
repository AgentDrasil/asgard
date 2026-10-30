import { describe, expect, it, vi, beforeEach } from "vitest";
import type { AgentInfo } from "../types";

const getAgents = vi.fn<(runner?: string) => Promise<AgentInfo[]>>();
vi.mock("../lib/api", () => ({ getAgents: (r?: string) => getAgents(r) }));

import { useAgents } from "./useAgents";

const agent = (id: string): AgentInfo => ({ id, name: id, run_dirs: [] }) as unknown as AgentInfo;

function deferred<T>() {
  let resolve!: (v: T) => void;
  const promise = new Promise<T>((r) => (resolve = r));
  return { promise, resolve };
}

describe("useAgents.loadAgents", () => {
  beforeEach(() => getAgents.mockReset());

  it("ignores a stale response for a superseded runner", async () => {
    const a = deferred<AgentInfo[]>();
    const b = deferred<AgentInfo[]>();
    getAgents.mockReturnValueOnce(a.promise).mockReturnValueOnce(b.promise);
    const { agents, loadAgents } = useAgents();
    const p1 = loadAgents("A");
    const p2 = loadAgents("B");
    b.resolve([agent("b1")]);
    await p2;
    a.resolve([agent("a1")]);
    await p1;
    expect(agents.value.map((x) => x.id)).toEqual(["b1"]);
  });

  it("shares one request between concurrent loads for the same runner", async () => {
    const a = deferred<AgentInfo[]>();
    getAgents.mockReturnValueOnce(a.promise);
    const { agents, loadAgents } = useAgents();
    const p1 = loadAgents("A");
    const p2 = loadAgents("A");
    a.resolve([agent("a1")]);
    await Promise.all([p1, p2]);
    expect(getAgents).toHaveBeenCalledTimes(1);
    expect(agents.value).toHaveLength(1);
  });
});
