import { describe, it, expect, vi } from "vitest";
import { getAgents } from "./api";

describe("API Library", () => {
  it("should fallback to mock data on fetch error", async () => {
    // Mock global fetch to reject
    const fetchMock = vi.spyOn(globalThis, "fetch").mockRejectedValue(new Error("Network error"));

    const agents = await getAgents();
    expect(agents).toBeDefined();
    expect(agents[0].id).toBe("agent_father");

    fetchMock.mockRestore();
  });
});
