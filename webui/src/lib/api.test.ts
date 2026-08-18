import { describe, it, expect, vi } from "vitest";
import { getAgents } from "./api";

describe("API Library", () => {
  it("should return empty array and log error on fetch error", async () => {
    const consoleErrorSpy = vi.spyOn(console, "error").mockImplementation(() => {});
    // Mock global fetch to reject
    const fetchMock = vi.spyOn(globalThis, "fetch").mockRejectedValue(new Error("Network error"));

    const agents = await getAgents();
    expect(agents).toEqual([]);
    expect(consoleErrorSpy).toHaveBeenCalledWith("getAgents error:", expect.any(Error));

    fetchMock.mockRestore();
    consoleErrorSpy.mockRestore();
  });
});
