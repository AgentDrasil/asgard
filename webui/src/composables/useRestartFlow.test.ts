// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach } from "vitest";
import { useRestartFlow } from "./useRestartFlow";
import * as api from "../lib/api";

describe("useRestartFlow", () => {
  beforeEach(() => {
    vi.restoreAllMocks();
  });

  it("manages confirmation modal open and close states", () => {
    const flow = useRestartFlow();
    expect(flow.isRestartConfirmOpen.value).toBe(false);

    flow.openRestartConfirm();
    expect(flow.isRestartConfirmOpen.value).toBe(true);

    flow.closeRestartConfirm();
    expect(flow.isRestartConfirmOpen.value).toBe(false);
  });

  it("handles restart rejection gracefully", async () => {
    vi.spyOn(api, "restartServer").mockResolvedValue(false);

    const flow = useRestartFlow();
    flow.openRestartConfirm();

    await flow.triggerRestartWorkflow();

    expect(flow.isRestartConfirmOpen.value).toBe(false);
    expect(flow.isRestarting.value).toBe(false);
  });
});
