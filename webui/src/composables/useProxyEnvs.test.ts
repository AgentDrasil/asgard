import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

// The composable keeps module-level cache state, so re-import it fresh per test.
async function importFresh() {
  vi.resetModules();
  const api = await import("../lib/api");
  const mod = await import("./useProxyEnvs");
  return { api, useProxyEnvs: mod.useProxyEnvs };
}

describe("useProxyEnvs", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.spyOn(console, "warn").mockImplementation(() => {});
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.restoreAllMocks();
  });

  it("fetches on first load and serves from cache within TTL", async () => {
    const { api, useProxyEnvs } = await importFresh();
    const spy = vi.spyOn(api, "getBackendConfig").mockResolvedValue({
      proxy_envs: ["GEMINI_API_KEY"],
    });

    const { proxyEnvs, load } = useProxyEnvs();
    expect(await load()).toEqual(["GEMINI_API_KEY"]);
    expect(await load()).toEqual(["GEMINI_API_KEY"]);
    expect(spy).toHaveBeenCalledTimes(1);

    // Still inside the 10s TTL: no refetch
    vi.advanceTimersByTime(9_999);
    await load();
    expect(spy).toHaveBeenCalledTimes(1);
    expect(proxyEnvs.value).toEqual(["GEMINI_API_KEY"]);
  });

  it("refetches after TTL expiry to pick up proxy hot-reloads", async () => {
    const { api, useProxyEnvs } = await importFresh();
    const spy = vi
      .spyOn(api, "getBackendConfig")
      .mockResolvedValueOnce({ proxy_envs: ["OLD_KEY"] })
      .mockResolvedValueOnce({ proxy_envs: ["NEW_KEY"] });

    const { load } = useProxyEnvs();
    expect(await load()).toEqual(["OLD_KEY"]);

    vi.advanceTimersByTime(10_001);
    expect(await load()).toEqual(["NEW_KEY"]);
    expect(spy).toHaveBeenCalledTimes(2);
  });

  it("shares one in-flight request between concurrent loads", async () => {
    const { api, useProxyEnvs } = await importFresh();
    const spy = vi.spyOn(api, "getBackendConfig").mockResolvedValue({
      proxy_envs: ["GEMINI_API_KEY"],
    });

    const { load } = useProxyEnvs();
    const [a, b] = await Promise.all([load(), load()]);
    expect(a).toEqual(["GEMINI_API_KEY"]);
    expect(b).toEqual(["GEMINI_API_KEY"]);
    expect(spy).toHaveBeenCalledTimes(1);
  });

  it("rate-limits retries after a failed fetch", async () => {
    const { api, useProxyEnvs } = await importFresh();
    const spy = vi
      .spyOn(api, "getBackendConfig")
      .mockRejectedValueOnce(new Error("network down"))
      .mockResolvedValue({ proxy_envs: ["GEMINI_API_KEY"] });

    const { load } = useProxyEnvs();
    expect(await load()).toEqual([]);

    // Immediate retry is suppressed by the TTL
    await load();
    expect(spy).toHaveBeenCalledTimes(1);

    // After the TTL the fetch is retried and succeeds
    vi.advanceTimersByTime(10_001);
    expect(await load()).toEqual(["GEMINI_API_KEY"]);
    expect(spy).toHaveBeenCalledTimes(2);
  });
});
