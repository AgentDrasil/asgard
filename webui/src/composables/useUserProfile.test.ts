import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

// The composable keeps module-level state, so re-import it fresh per test.
async function importFresh(authEnabled: boolean) {
  vi.resetModules();
  const api = await import("../lib/api");
  vi.spyOn(api, "isAuthEnabled").mockReturnValue(authEnabled);
  const mod = await import("./useUserProfile");
  return { api, useUserProfile: mod.useUserProfile };
}

describe("useUserProfile", () => {
  beforeEach(() => {
    vi.spyOn(console, "warn").mockImplementation(() => {});
  });
  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("returns null without fetching when auth is disabled", async () => {
    const { api, useUserProfile } = await importFresh(false);
    const spy = vi.spyOn(api, "apiFetch");
    expect(await useUserProfile().load()).toBeNull();
    expect(spy).not.toHaveBeenCalled();
  });

  it("dedupes concurrent loads and caches the result", async () => {
    const { api, useUserProfile } = await importFresh(true);
    const spy = vi
      .spyOn(api, "apiFetch")
      .mockResolvedValue(new Response(JSON.stringify({ subject: "s", name: "Ada" })));
    const { load, profile } = useUserProfile();
    await Promise.all([load(), load()]);
    await load();
    expect(spy).toHaveBeenCalledTimes(1);
    expect(profile.value?.name).toBe("Ada");
  });

  it("keeps the profile null on a non-ok response", async () => {
    const { api, useUserProfile } = await importFresh(true);
    vi.spyOn(api, "apiFetch").mockResolvedValue(new Response("{}", { status: 404 }));
    const { load, profile } = useUserProfile();
    expect(await load()).toBeNull();
    expect(profile.value).toBeNull();
  });
});
